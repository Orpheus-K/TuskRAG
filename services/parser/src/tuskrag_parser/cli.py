import argparse
import csv
import errno
import json
import sys
from pathlib import Path

import docx

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.csv_parser import parse_csv
from tuskrag_parser.docx_parser import parse_docx
from tuskrag_parser.html_parser import HTML_DRIVER, parse_html
from tuskrag_parser.limits import (
    EncryptedDocument,
    InvalidDocument,
    InvalidLimits,
    LimitExceeded,
    UnsupportedDocument,
    parse_limits,
)
from tuskrag_parser.pptx_parser import PPTX_DRIVER, parse_pptx
from tuskrag_parser.xlsx_parser import XLSX_DRIVER, parse_xlsx

DOCX_MIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
XLSX_MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
PPTX_MIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
HTML_MIME = "text/html"
XHTML_MIME = "application/xhtml+xml"
PDF_MIME = "application/pdf"


def main() -> int:
    parser = argparse.ArgumentParser(prog="tuskrag-parse")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("version")
    parse = commands.add_parser("parse")
    parse.add_argument("--input", required=True)
    parse.add_argument("--mime", required=True)
    parse.add_argument("--out", required=True)
    parse.add_argument("--limits", required=True)
    render = commands.add_parser("render")
    render.add_argument("--input", required=True)
    render.add_argument("--pages", required=True)
    render.add_argument("--dpi", type=int, default=150)
    render.add_argument("--out-dir", required=True)
    render.add_argument("--limits", required=True)
    args = parser.parse_args()

    if args.command == "version":
        from tuskrag_parser.pdf_parser import PDF_DRIVER

        print(
            json.dumps(
                {
                    "tool": "tuskrag-parse",
                    "version": __version__,
                    "schema_version": SCHEMA_VERSION,
                    "drivers": {
                        "csv": f"python-stdlib-{sys.version_info.major}.{sys.version_info.minor}",
                        "docx": f"python-docx-{docx.__version__}",
                        "xlsx": XLSX_DRIVER,
                        "pptx": PPTX_DRIVER,
                        "html": HTML_DRIVER,
                        "pdf": PDF_DRIVER,
                    },
                }
            )
        )
        return 0

    if args.command == "parse" and args.mime not in {
        "text/csv",
        DOCX_MIME,
        XLSX_MIME,
        PPTX_MIME,
        HTML_MIME,
        XHTML_MIME,
        PDF_MIME,
    }:
        print("unsupported format", file=sys.stderr)
        return 10
    try:
        limits = parse_limits(args.limits)
        if args.command == "render":
            from tuskrag_parser.render import render_pdf_pages

            pages = render_pdf_pages(
                Path(args.input),
                Path(args.out_dir),
                args.pages,
                args.dpi,
                limits,
            )
            print(json.dumps({"pages": pages}))
        elif args.mime == "text/csv":
            parse_csv(Path(args.input), Path(args.out), limits)
        elif args.mime == DOCX_MIME:
            parse_docx(Path(args.input), Path(args.out), limits)
        elif args.mime == PPTX_MIME:
            parse_pptx(Path(args.input), Path(args.out), limits)
        elif args.mime in {HTML_MIME, XHTML_MIME}:
            parse_html(Path(args.input), Path(args.out), limits, mime=args.mime)
        elif args.mime == PDF_MIME:
            from tuskrag_parser.pdf_parser import parse_pdf

            parse_pdf(Path(args.input), Path(args.out), limits)
        else:
            parse_xlsx(Path(args.input), Path(args.out), limits)
    except InvalidLimits:
        print("invalid parser limits", file=sys.stderr)
        return 1
    except UnsupportedDocument:
        print("unsupported document content", file=sys.stderr)
        return 10
    except EncryptedDocument:
        print("encrypted document", file=sys.stderr)
        return 11
    except (LimitExceeded, MemoryError):
        print("document exceeds parser limits", file=sys.stderr)
        return 12
    except (InvalidDocument, UnicodeError, csv.Error):
        print("invalid document", file=sys.stderr)
        return 13
    except OSError as error:
        if error.errno in {errno.EFBIG, errno.ENOSPC}:
            print("document exceeds parser limits", file=sys.stderr)
            return 12
        print("parser I/O failure", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
