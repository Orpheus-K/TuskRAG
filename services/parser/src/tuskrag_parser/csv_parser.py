import csv
import io
import json
import os
import stat
from pathlib import Path

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.limits import InvalidDocument, LimitExceeded, Limits


def parse_csv(input_path: Path, output_path: Path, limits: Limits) -> None:
    with input_path.open("rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size <= 0:
            raise InvalidDocument
        if info.st_size > limits.max_input_bytes:
            raise LimitExceeded
        prefix = source.read(512).removeprefix(b"\xef\xbb\xbf").lstrip()
        if (
            prefix.startswith(
                (
                    b"PK\x03\x04",
                    b"PK\x05\x06",
                    b"PK\x07\x08",
                    b"%PDF-",
                    b"<?xml",
                    b"<!DOCTYPE",
                )
            )
            or b"\x00" in prefix
        ):
            raise InvalidDocument
        source.seek(0)
        csv.field_size_limit(limits.max_field_chars)
        rows: list[list[str]] = []
        cells = 0
        characters = 0
        with io.TextIOWrapper(source, encoding="utf-8-sig", newline="") as text:
            try:
                for row in csv.reader(text, strict=True):
                    cells += len(row)
                    if (
                        len(rows) >= limits.max_rows
                        or len(row) > limits.max_columns
                        or cells > limits.max_cells
                    ):
                        raise LimitExceeded
                    if any("\x00" in cell for cell in row):
                        raise InvalidDocument
                    characters += sum(
                        not character.isspace() for cell in row for character in cell
                    )
                    rows.append(row)
            except csv.Error as error:
                if str(error).startswith("field larger than field limit"):
                    raise LimitExceeded from error
                raise InvalidDocument from error
        if not rows:
            raise InvalidDocument
    suspect = any(len(row) != len(rows[0]) for row in rows)
    document = {
        "schema_version": SCHEMA_VERSION,
        "pages": [
            {
                "no": 1,
                "blocks": [{"type": "table", "cells": rows}],
                "signals": {"char_count": characters, "table_suspect": suspect},
            }
        ],
        "meta": {"parser_profile": f"tuskrag-parse/{__version__};csv=python-stdlib"},
    }
    payload = json.dumps(document, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    if len(payload) > min(limits.max_output_bytes, info.st_size * 20):
        raise LimitExceeded
    (output_path / "parsed.json").write_bytes(payload)
