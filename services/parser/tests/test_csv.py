import json
import subprocess
import sys
from pathlib import Path
from typing import Any

import pytest
from jsonschema import Draft202012Validator, FormatChecker

from tuskrag_parser import SCHEMA_VERSION, __version__

FIXTURES = Path(__file__).parent / "fixtures" / "csv"
CASES: list[dict[str, Any]] = json.loads(
    (FIXTURES / "golden.json").read_text(encoding="utf-8")
)
SCHEMA = json.loads(
    (
        Path(__file__).resolve().parents[3]
        / "contracts"
        / "parsed-document.schema.json"
    ).read_text(encoding="utf-8")
)


def run_parse(
    tmp_path: Path, data: bytes, limits: str = "{}", mime: str = "text/csv"
) -> tuple[subprocess.CompletedProcess[str], Path]:
    source = tmp_path / "source"
    output = tmp_path / "output"
    source.write_bytes(data)
    output.mkdir()
    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "tuskrag_parser.cli",
            "parse",
            "--input",
            str(source),
            "--mime",
            mime,
            "--out",
            str(output),
            "--limits",
            limits,
        ],
        capture_output=True,
        check=False,
        text=True,
    )
    return result, output / "parsed.json"


@pytest.mark.parametrize("case", CASES, ids=lambda case: str(case["file"]))
def test_csv_golden(tmp_path: Path, case: dict[str, Any]) -> None:
    data = (FIXTURES / case["file"]).read_bytes()
    result, output = run_parse(tmp_path, data)
    assert result.returncode == 0, result.stderr
    assert result.stdout == ""
    assert output.stat().st_size <= len(data) * 20
    document = json.loads(output.read_text(encoding="utf-8"))
    Draft202012Validator(SCHEMA, format_checker=FormatChecker()).validate(document)
    assert document["schema_version"] == SCHEMA_VERSION
    assert (
        document["meta"]["parser_profile"]
        == f"tuskrag-parse/{__version__};csv=python-stdlib"
    )
    assert len(document["pages"]) == 1
    page = document["pages"][0]
    assert page["no"] == 1
    assert page["blocks"] == [{"type": "table", "cells": case["cells"]}]
    assert page["signals"]["table_suspect"] == case["table_suspect"]
    assert page["signals"]["char_count"] == sum(
        not character.isspace()
        for row in case["cells"]
        for cell in row
        for character in cell
    )


@pytest.mark.parametrize(
    "data",
    [
        b"",
        b"PK\x03\x04not-a-csv",
        b"%PDF-1.7\n",
        b"<?xml version='1.0'?><data/>",
        b"id,name\n1,\xff\n",
        b"id,name\n1,secret\x00value\n",
        b'id,name\n1,"secret unclosed\n',
    ],
)
def test_invalid_or_disguised_data_fails_without_output(
    tmp_path: Path, data: bytes
) -> None:
    result, output = run_parse(tmp_path, data)
    assert result.returncode == 13
    assert not output.exists()
    assert "secret" not in result.stderr


@pytest.mark.parametrize(
    "limits",
    [
        '{"max_rows":1}',
        '{"max_columns":2}',
        '{"max_cells":2}',
        '{"max_field_chars":2}',
        '{"max_input_bytes":1}',
        '{"max_output_bytes":1}',
    ],
)
def test_csv_limits_fail_without_output(tmp_path: Path, limits: str) -> None:
    result, output = run_parse(tmp_path, (FIXTURES / "policy.csv").read_bytes(), limits)
    assert result.returncode == 12
    assert not output.exists()


@pytest.mark.parametrize(
    "limits",
    [
        "[]",
        '{"max_rows":0}',
        '{"max_rows":true}',
        '{"max_rows":100001}',
        '{"unknown":1}',
        "{",
    ],
)
def test_invalid_limits_are_not_silently_ignored(tmp_path: Path, limits: str) -> None:
    result, output = run_parse(tmp_path, (FIXTURES / "policy.csv").read_bytes(), limits)
    assert result.returncode == 1
    assert not output.exists()


def test_tiny_input_cannot_bypass_output_ratio(tmp_path: Path) -> None:
    result, output = run_parse(tmp_path, b"a")
    assert result.returncode == 12
    assert not output.exists()
