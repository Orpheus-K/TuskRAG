import io
import json
import struct
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path
from typing import Any
from zipfile import ZIP_DEFLATED, ZipFile

import pytest
from jsonschema import Draft202012Validator, FormatChecker
from openpyxl import Workbook
from openpyxl.comments import Comment

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.cli import XLSX_MIME
from tuskrag_parser.xlsx_parser import XLSX_DRIVER

FIXTURES = Path(__file__).parent / "fixtures" / "xlsx"
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
    tmp_path: Path, data: bytes, limits: str = "{}"
) -> tuple[subprocess.CompletedProcess[str], Path]:
    tmp_path.mkdir(parents=True, exist_ok=True)
    source, output = tmp_path / "source", tmp_path / "output"
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
            XLSX_MIME,
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
def test_xlsx_golden(tmp_path: Path, case: dict[str, Any]) -> None:
    source = (FIXTURES / case["file"]).read_bytes()
    result, output = run_parse(tmp_path, source)
    assert result.returncode == 0, result.stderr
    assert result.stdout == ""
    assert output.stat().st_size <= len(source) * 20
    document = json.loads(output.read_text(encoding="utf-8"))
    Draft202012Validator(SCHEMA, format_checker=FormatChecker()).validate(document)
    assert document["schema_version"] == SCHEMA_VERSION
    assert (
        document["meta"]["parser_profile"]
        == f"tuskrag-parse/{__version__};xlsx={XLSX_DRIVER}"
    )
    assert len(document["pages"]) == len(case["pages"])
    for index, expected in enumerate(case["pages"]):
        page = document["pages"][index]
        assert page["no"] == index + 1
        assert page["blocks"] == expected
        texts = [block["text"] for block in expected if "text" in block]
        texts.extend(
            cell
            for block in expected
            if "cells" in block
            for row in block["cells"]
            for cell in row
        )
        assert page["signals"]["char_count"] == sum(
            not char.isspace() for text in texts for char in text
        )


@pytest.mark.parametrize(
    "limits",
    [
        '{"max_rows":2}',
        '{"max_columns":1}',
        '{"max_cells":3}',
        '{"max_field_chars":2}',
        '{"max_blocks":1}',
        '{"max_archive_entries":1}',
        '{"max_uncompressed_bytes":10}',
        '{"max_compression_ratio":1}',
        '{"max_xml_nodes":1}',
        '{"max_input_bytes":1}',
        '{"max_output_bytes":1}',
    ],
)
def test_xlsx_limits_fail_without_output(tmp_path: Path, limits: str) -> None:
    result, output = run_parse(
        tmp_path, (FIXTURES / "travel.xlsx").read_bytes(), limits
    )
    assert result.returncode == 12, result.stderr
    assert not output.exists()


def workbook_bytes(edit: Callable[[Workbook], None]) -> bytes:
    workbook = Workbook()
    assert workbook.active is not None
    workbook.active.append(["heading", "value"])
    workbook.active.append(["row", 1])
    edit(workbook)
    result = io.BytesIO()
    workbook.save(result)
    return result.getvalue()


def set_formula(workbook: Workbook) -> None:
    assert workbook.active is not None
    workbook.active["B2"] = "=1+1"


def hide_row(workbook: Workbook) -> None:
    assert workbook.active is not None
    workbook.active.row_dimensions[2].hidden = True


def hide_column(workbook: Workbook) -> None:
    assert workbook.active is not None
    workbook.active.column_dimensions["B"].hidden = True


def hide_sheet(workbook: Workbook) -> None:
    workbook.create_sheet("hidden").sheet_state = "hidden"


def set_comment(workbook: Workbook) -> None:
    assert workbook.active is not None
    workbook.active["A1"].comment = Comment("private comment", "author")


def set_number_format(workbook: Workbook) -> None:
    assert workbook.active is not None
    workbook.active["B2"].number_format = "0000"


def set_error(workbook: Workbook) -> None:
    assert workbook.active is not None
    workbook.active["B2"] = "#DIV/0!"


@pytest.mark.parametrize(
    "edit",
    [
        set_formula,
        hide_row,
        hide_column,
        hide_sheet,
        set_comment,
        set_number_format,
        set_error,
    ],
)
def test_unsupported_xlsx_content_is_not_silently_ignored(
    tmp_path: Path, edit: Callable[[Workbook], None]
) -> None:
    result, output = run_parse(tmp_path, workbook_bytes(edit))
    assert result.returncode == 10, result.stderr
    assert not output.exists()
    assert "private comment" not in result.stderr


def rewrite_xlsx(
    original: bytes,
    replacements: dict[str, bytes],
    extra: tuple[str, bytes] | None = None,
) -> bytes:
    result = io.BytesIO()
    with ZipFile(io.BytesIO(original)) as before, ZipFile(result, "w") as after:
        for info in before.infolist():
            after.writestr(info, replacements.get(info.filename, before.read(info)))
        if extra is not None:
            after.writestr(*extra)
    return result.getvalue()


def test_xlsx_rejects_xml_entities_traversal_and_zip_bomb(tmp_path: Path) -> None:
    original = (FIXTURES / "travel.xlsx").read_bytes()
    with ZipFile(io.BytesIO(original)) as archive:
        xml = archive.read("xl/worksheets/sheet1.xml")
    malicious = b'<!DOCTYPE worksheet [<!ENTITY xxe SYSTEM "file:///secret">]>' + xml
    result, output = run_parse(
        tmp_path / "entity",
        rewrite_xlsx(original, {"xl/worksheets/sheet1.xml": malicious}),
    )
    assert result.returncode == 13
    assert not output.exists()
    assert "secret" not in result.stderr
    result, output = run_parse(
        tmp_path / "traversal", rewrite_xlsx(original, {}, ("../secret", b"content"))
    )
    assert result.returncode == 13
    assert not output.exists()
    bomb = io.BytesIO()
    with ZipFile(bomb, "w", compression=ZIP_DEFLATED) as archive:
        archive.writestr("bomb.xml", b"x" * 1_000_000)
    result, output = run_parse(tmp_path / "bomb", bomb.getvalue())
    assert result.returncode == 12
    assert not output.exists()


def test_xlsx_rejects_encryption_and_bad_formats(tmp_path: Path) -> None:
    encrypted = bytearray((FIXTURES / "travel.xlsx").read_bytes())
    central = encrypted.index(b"PK\x01\x02")
    flags = struct.unpack_from("<H", encrypted, central + 8)[0]
    struct.pack_into("<H", encrypted, central + 8, flags | 1)
    result, output = run_parse(tmp_path / "encrypted-zip", bytes(encrypted))
    assert result.returncode == 11
    assert not output.exists()
    encrypted_ooxml = Path(__file__).parent / "fixtures" / "docx" / "encrypted.docx"
    result, output = run_parse(
        tmp_path / "encrypted-ooxml", encrypted_ooxml.read_bytes()
    )
    assert result.returncode == 11
    assert not output.exists()
    for index, data in enumerate(
        [
            b"not a spreadsheet",
            (Path(__file__).parent / "fixtures" / "docx" / "policy.docx").read_bytes(),
        ]
    ):
        result, output = run_parse(tmp_path / f"invalid-{index}", data)
        assert result.returncode == 13, result.stderr
        assert not output.exists()


def test_xlsx_rejects_sparse_coordinates_before_loading(tmp_path: Path) -> None:
    original = (FIXTURES / "travel.xlsx").read_bytes()
    with ZipFile(io.BytesIO(original)) as archive:
        xml = archive.read("xl/worksheets/sheet1.xml")
    modified = xml.replace(b'r="A1"', b'r="XFD1048576"', 1)
    assert modified != xml
    result, output = run_parse(
        tmp_path, rewrite_xlsx(original, {"xl/worksheets/sheet1.xml": modified})
    )
    assert result.returncode == 12, result.stderr
    assert not output.exists()


def test_xlsx_rejects_values_that_the_readers_disagree_on(tmp_path: Path) -> None:
    original = workbook_bytes(lambda _: None)
    with ZipFile(io.BytesIO(original)) as archive:
        xml = archive.read("xl/worksheets/sheet1.xml")
    modified = xml.replace(b"<v>1</v>", b"<v>9007199254740993</v>", 1)
    assert modified != xml
    result, output = run_parse(
        tmp_path, rewrite_xlsx(original, {"xl/worksheets/sheet1.xml": modified})
    )
    assert result.returncode == 13, result.stderr
    assert not output.exists()


def test_xlsx_rejects_overlapping_merges(tmp_path: Path) -> None:
    original = (FIXTURES / "travel.xlsx").read_bytes()
    with ZipFile(io.BytesIO(original)) as archive:
        xml = archive.read("xl/worksheets/sheet2.xml")
    modified = xml.replace(
        b"</mergeCells>", b'<mergeCell ref="A2:B2"/></mergeCells>', 1
    )
    assert modified != xml
    result, output = run_parse(
        tmp_path, rewrite_xlsx(original, {"xl/worksheets/sheet2.xml": modified})
    )
    assert result.returncode == 13, result.stderr
    assert not output.exists()


def test_xlsx_rejects_invalid_visibility_and_duplicate_sheet_ids(
    tmp_path: Path,
) -> None:
    original = (FIXTURES / "travel.xlsx").read_bytes()
    with ZipFile(io.BytesIO(original)) as archive:
        sheet = archive.read("xl/worksheets/sheet1.xml")
        workbook = archive.read("xl/workbook.xml")
    invalid_row = sheet.replace(b'<row r="1"', b'<row hidden="TRUE" r="1"', 1)
    duplicate_sheet = workbook.replace(b'sheetId="2"', b'sheetId="1"', 1)
    assert invalid_row != sheet
    assert duplicate_sheet != workbook
    for name, replacements in [
        ("visibility", {"xl/worksheets/sheet1.xml": invalid_row}),
        ("duplicate", {"xl/workbook.xml": duplicate_sheet}),
    ]:
        result, output = run_parse(
            tmp_path / name, rewrite_xlsx(original, replacements)
        )
        assert result.returncode == 13, result.stderr
        assert not output.exists()


def test_xlsx_rejects_protected_workbook_and_sheet(tmp_path: Path) -> None:
    def protect_workbook(workbook: Workbook) -> None:
        workbook.security.lockStructure = True
        workbook.security.set_workbook_password("test")

    def protect_sheet(workbook: Workbook) -> None:
        assert workbook.active is not None
        workbook.active.protection.sheet = True

    for index, edit in enumerate([protect_workbook, protect_sheet]):
        result, output = run_parse(tmp_path / str(index), workbook_bytes(edit))
        assert result.returncode == 11, result.stderr
        assert not output.exists()
