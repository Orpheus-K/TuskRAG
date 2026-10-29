import io
import json
import struct
import subprocess
import sys
from pathlib import Path
from typing import Any
from zipfile import ZIP_DEFLATED, ZipFile

import pytest
from docx import Document
from docx.enum.style import WD_STYLE_TYPE
from docx.oxml import OxmlElement
from docx.oxml.ns import qn
from jsonschema import Draft202012Validator, FormatChecker

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.cli import DOCX_MIME

FIXTURES = Path(__file__).parent / "fixtures" / "docx"
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
            DOCX_MIME,
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
def test_docx_golden(tmp_path: Path, case: dict[str, Any]) -> None:
    data = (FIXTURES / case["file"]).read_bytes()
    result, output = run_parse(tmp_path, data)
    assert result.returncode == 0, result.stderr
    assert result.stdout == ""
    document = json.loads(output.read_text(encoding="utf-8"))
    Draft202012Validator(SCHEMA, format_checker=FormatChecker()).validate(document)
    assert document["schema_version"] == SCHEMA_VERSION
    assert (
        document["meta"]["parser_profile"]
        == f"tuskrag-parse/{__version__};docx=python-docx-1.2.0"
    )
    assert len(document["pages"]) == 1
    page = document["pages"][0]
    assert page["no"] == 1
    assert page["blocks"] == case["blocks"]
    expected_text = [block["text"] for block in case["blocks"] if "text" in block]
    expected_text.extend(
        cell
        for block in case["blocks"]
        if "cells" in block
        for row in block["cells"]
        for cell in row
    )
    assert page["signals"]["char_count"] == sum(
        not character.isspace() for text in expected_text for character in text
    )


@pytest.mark.parametrize(
    "limits",
    [
        '{"max_rows":2}',
        '{"max_columns":1}',
        '{"max_cells":3}',
        '{"max_field_chars":3}',
        '{"max_blocks":1}',
        '{"max_archive_entries":1}',
        '{"max_uncompressed_bytes":10}',
        '{"max_compression_ratio":1}',
        '{"max_xml_nodes":1}',
        '{"max_input_bytes":1}',
        '{"max_output_bytes":1}',
    ],
)
def test_docx_limits_fail_without_output(tmp_path: Path, limits: str) -> None:
    result, output = run_parse(
        tmp_path, (FIXTURES / "policy.docx").read_bytes(), limits
    )
    assert result.returncode == 12, result.stderr
    assert not output.exists()


def test_docx_row_limit_applies_across_tables(tmp_path: Path) -> None:
    document = Document()
    document.add_table(rows=1, cols=1).cell(0, 0).text = "一"
    document.add_table(rows=1, cols=1).cell(0, 0).text = "二"
    data = io.BytesIO()
    document.save(data)
    result, output = run_parse(tmp_path, data.getvalue(), '{"max_rows":1}')
    assert result.returncode == 12
    assert not output.exists()


def rewrite_docx(
    original: bytes,
    replacements: dict[str, bytes],
    extra: tuple[str, bytes] | None = None,
) -> bytes:
    source, target = io.BytesIO(original), io.BytesIO()
    with ZipFile(source) as before, ZipFile(target, "w") as after:
        for info in before.infolist():
            after.writestr(info, replacements.get(info.filename, before.read(info)))
        if extra is not None:
            after.writestr(*extra)
    return target.getvalue()


def test_docx_rejects_xml_entities_and_traversal(tmp_path: Path) -> None:
    original = (FIXTURES / "policy.docx").read_bytes()
    with ZipFile(io.BytesIO(original)) as archive:
        xml = archive.read("word/document.xml")
    declaration_end = xml.index(b"?>") + 2
    malicious = (
        xml[:declaration_end]
        + b'<!DOCTYPE document [<!ENTITY xxe SYSTEM "file:///secret">]>'
        + xml[declaration_end:]
    )
    result, output = run_parse(
        tmp_path / "entity", rewrite_docx(original, {"word/document.xml": malicious})
    )
    assert result.returncode == 13
    assert not output.exists()
    assert "secret" not in result.stderr
    result, output = run_parse(
        tmp_path / "traversal", rewrite_docx(original, {}, ("../secret", b"content"))
    )
    assert result.returncode == 13
    assert not output.exists()


def test_docx_rejects_zip_bomb_and_encrypted_entry(tmp_path: Path) -> None:
    bomb = io.BytesIO()
    with ZipFile(bomb, "w", compression=ZIP_DEFLATED) as archive:
        archive.writestr("bomb.xml", b"x" * 1_000_000)
    result, output = run_parse(tmp_path / "bomb", bomb.getvalue())
    assert result.returncode == 12
    assert not output.exists()
    encrypted = bytearray((FIXTURES / "policy.docx").read_bytes())
    central = encrypted.index(b"PK\x01\x02")
    flags = struct.unpack_from("<H", encrypted, central + 8)[0]
    struct.pack_into("<H", encrypted, central + 8, flags | 1)
    result, output = run_parse(tmp_path / "encrypted", bytes(encrypted))
    assert result.returncode == 11
    assert not output.exists()


def test_docx_rejects_office_encrypted_package(tmp_path: Path) -> None:
    result, output = run_parse(tmp_path, (FIXTURES / "encrypted.docx").read_bytes())
    assert result.returncode == 11
    assert not output.exists()


@pytest.mark.parametrize(
    "feature",
    [
        "list",
        "numbered_heading",
        "nested_table",
        "drawing",
        "change",
        "header",
        "unrecognized_run",
        "unknown_cell",
        "hidden_style",
        "hidden_table_style",
    ],
)
def test_docx_unsupported_content_is_explicit(tmp_path: Path, feature: str) -> None:
    document = Document()
    paragraph = document.add_paragraph("正文")
    if feature == "list":
        document.add_paragraph("列表项", style="List Bullet")
    elif feature == "numbered_heading":
        numbered = document.add_paragraph("编号标题", style="List Bullet")
        outline = OxmlElement("w:outlineLvl")
        outline.set(qn("w:val"), "0")
        numbered._p.get_or_add_pPr().append(outline)
    elif feature == "nested_table":
        document.add_table(rows=1, cols=1).cell(0, 0).add_table(rows=1, cols=1)
    elif feature == "drawing":
        paragraph.add_run()._r.append(OxmlElement("w:drawing"))
    elif feature == "change":
        paragraph._p.append(OxmlElement("w:ins"))
    elif feature == "header":
        document.sections[0].header.paragraphs[0].text = "页眉"
    elif feature == "unknown_cell":
        document.add_table(rows=1, cols=1).cell(0, 0)._tc.append(
            OxmlElement("w:unknown")
        )
    elif feature in {"hidden_style", "hidden_table_style"}:
        style = document.styles.add_style("HiddenCharacter", WD_STYLE_TYPE.CHARACTER)
        style.font.hidden = True
        target = (
            paragraph
            if feature == "hidden_style"
            else document.add_table(rows=1, cols=1).cell(0, 0).paragraphs[0]
        )
        target.add_run("隐藏正文", style=style)
    else:
        paragraph.add_run()._r.append(OxmlElement("w:softHyphen"))
    data = io.BytesIO()
    document.save(data)
    result, output = run_parse(tmp_path, data.getvalue())
    assert result.returncode == 10, result.stderr
    assert not output.exists()


@pytest.mark.parametrize(
    "data",
    [
        b"",
        b"not a document",
        b"%PDF-1.7\n",
        b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1" + bytes(2048),
    ],
)
def test_docx_invalid_data_fails(tmp_path: Path, data: bytes) -> None:
    result, output = run_parse(tmp_path, data)
    assert result.returncode == 13
    assert not output.exists()
