import json
import subprocess
import sys
from pathlib import Path
from typing import Any

import pytest
from jsonschema import Draft202012Validator, FormatChecker

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.limits import UnsupportedDocument, parse_limits
from tuskrag_parser.pdf_parser import (
    PDF_DRIVER,
    Line,
    Page,
    assign_headings,
    image_coverage,
    read_page,
)

FIXTURES = Path(__file__).parent / "fixtures" / "pdf"
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
            "application/pdf",
            "--out",
            str(output),
            "--limits",
            limits,
        ],
        capture_output=True,
        check=False,
        text=True,
        encoding="utf-8",
    )
    return result, output / "parsed.json"


def test_pdf_policy_structure_and_pages(tmp_path: Path) -> None:
    data = (FIXTURES / "policy.pdf").read_bytes()
    result, output = run_parse(tmp_path, data)
    assert result.returncode == 0, result.stderr
    assert result.stdout == ""
    document = json.loads(output.read_text(encoding="utf-8"))
    Draft202012Validator(SCHEMA, format_checker=FormatChecker()).validate(document)
    assert document["schema_version"] == SCHEMA_VERSION
    assert (
        document["meta"]["parser_profile"]
        == f"tuskrag-parse/{__version__};pdf={PDF_DRIVER}"
    )
    assert len(document["pages"]) == 2
    expected: list[list[dict[str, Any]]] = [
        [
            {"type": "heading", "level": 1, "text": "差旅管理办法"},
            {"type": "paragraph", "text": "出差前须提出申请。"},
            {
                "type": "table",
                "cells": [["城市", "住宿上限"], ["北京", "600"], ["上海", "450"]],
            },
        ],
        [
            {"type": "heading", "level": 2, "text": "报销流程"},
            {"type": "paragraph", "text": "员工应提交票据并完成审核。"},
        ],
    ]
    for index, page in enumerate(document["pages"]):
        assert page["no"] == index + 1
        assert page["width"] == 595 and page["height"] == 842
        assert page["signals"]["image_coverage"] == 0
        assert page["signals"]["table_suspect"] is False
        assert [
            {key: value for key, value in block.items() if key != "bbox"}
            for block in page["blocks"]
        ] == expected[index]
        for block in page["blocks"]:
            x0, top, x1, bottom = block["bbox"]
            assert 0 <= x0 < x1 <= page["width"] and 0 <= top < bottom <= page["height"]
    assert [page["signals"]["char_count"] for page in document["pages"]] == [31, 17]
    assert output.stat().st_size <= len(data) * 20


def test_scanned_page_reports_image_coverage(tmp_path: Path) -> None:
    result, output = run_parse(tmp_path, (FIXTURES / "scanned.pdf").read_bytes())
    assert result.returncode == 0, result.stderr
    document = json.loads(output.read_text(encoding="utf-8"))
    Draft202012Validator(SCHEMA, format_checker=FormatChecker()).validate(document)
    page = document["pages"][0]
    assert page["blocks"] == []
    assert page["signals"]["char_count"] == 0
    assert page["signals"]["image_coverage"] == 1


@pytest.mark.parametrize(
    ("fixture", "code"),
    [("columns.pdf", 10), ("columns-staggered.pdf", 10), ("encrypted.pdf", 11)],
)
def test_pdf_rejects_ambiguous_columns_and_encryption(
    tmp_path: Path, fixture: str, code: int
) -> None:
    result, output = run_parse(tmp_path, (FIXTURES / fixture).read_bytes())
    assert result.returncode == code
    assert not output.exists()


@pytest.mark.parametrize(
    "data", [b"not a PDF", b"%PDF-1.7\ntruncated", b"PK\x03\x04not a PDF"]
)
def test_invalid_pdf_fails_without_output(tmp_path: Path, data: bytes) -> None:
    result, output = run_parse(tmp_path, data)
    assert result.returncode == 13
    assert not output.exists()


@pytest.mark.parametrize(
    "limits",
    [
        '{"max_pages":1}',
        '{"max_input_bytes":1}',
        '{"max_output_bytes":1}',
        '{"max_blocks":1}',
        '{"max_rows":2}',
        '{"max_columns":1}',
        '{"max_cells":3}',
        '{"max_field_chars":3}',
    ],
)
def test_pdf_limits_fail_without_output(tmp_path: Path, limits: str) -> None:
    result, output = run_parse(tmp_path, (FIXTURES / "policy.pdf").read_bytes(), limits)
    assert result.returncode == 12, result.stderr
    assert not output.exists()


def test_image_coverage_counts_union_once() -> None:
    images = [
        {"x0": 0, "top": 0, "x1": 50, "bottom": 100},
        {"x0": 25, "top": 0, "x1": 75, "bottom": 100},
    ]
    assert image_coverage(images, 100, 100) == 0.75


def test_headings_without_bookmarks_follow_font_sizes() -> None:
    page = Page(
        100,
        100,
        [
            Line("标题", (0, 0, 50, 10), 20, 2),
            Line("普通正文。", (0, 20, 80, 30), 12, 5),
        ],
        [],
        0,
        False,
    )
    assign_headings([page], [])
    assert page.lines[0].heading_level == 1
    assert page.lines[1].heading_level is None


class _FakePDFPage:
    width = 595.0
    height = 842.0
    rotation = 0
    bbox = (0, 0, 595.0, 842.0)

    def __init__(self, annots: list[dict[str, Any]]) -> None:
        self.chars: list[dict[str, Any]] = []
        self.images: list[dict[str, Any]] = []
        self.objects: dict[str, list[dict[str, Any]]] = {}
        self.rects: list[dict[str, Any]] = []
        self.curves: list[dict[str, Any]] = []
        self.lines: list[dict[str, Any]] = []
        self.edges: list[dict[str, Any]] = []
        self.annots = annots

    def find_tables(self, *args: Any, **kwargs: Any) -> list[Any]:
        return []

    def filter(self, *args: Any, **kwargs: Any) -> "_FakePDFPage":
        return self

    def extract_text_lines(self, *args: Any, **kwargs: Any) -> list[Any]:
        return []


class _FakeTextPage:
    def get_text_bounded(self) -> str:
        return ""

    def close(self) -> None:
        pass


class _FakeNativePage:
    def get_textpage(self) -> _FakeTextPage:
        return _FakeTextPage()


def test_read_page_allows_link_annotations_and_rejects_others() -> None:
    limits = parse_limits("{}")
    link_page = _FakePDFPage([{"data": {"Subtype": "/Link"}}])
    extracted = read_page(link_page, _FakeNativePage(), limits)
    assert extracted.lines == []

    widget_page = _FakePDFPage([{"data": {"Subtype": "/Widget"}}])
    with pytest.raises(UnsupportedDocument):
        read_page(widget_page, _FakeNativePage(), limits)
