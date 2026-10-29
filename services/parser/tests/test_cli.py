import json
import subprocess
import sys
from pathlib import Path

import docx

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.pdf_parser import PDF_DRIVER
from tuskrag_parser.xlsx_parser import XLSX_DRIVER


def test_version_is_machine_readable() -> None:
    result = subprocess.run(
        [sys.executable, "-m", "tuskrag_parser.cli", "version"],
        capture_output=True,
        check=True,
        text=True,
    )
    assert json.loads(result.stdout) == {
        "tool": "tuskrag-parse",
        "version": __version__,
        "schema_version": SCHEMA_VERSION,
        "drivers": {
            "csv": f"python-stdlib-{sys.version_info.major}.{sys.version_info.minor}",
            "docx": f"python-docx-{docx.__version__}",
            "xlsx": XLSX_DRIVER,
            "pdf": PDF_DRIVER,
        },
    }


def test_unsupported_format_does_not_create_output(tmp_path: Path) -> None:
    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "tuskrag_parser.cli",
            "parse",
            "--input",
            str(tmp_path / "input"),
            "--mime",
            "application/unsupported",
            "--out",
            str(tmp_path),
            "--limits",
            "{}",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == 10
    assert not (tmp_path / "parsed.json").exists()
