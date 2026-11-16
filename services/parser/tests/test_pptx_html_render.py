import json
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile

import olefile
import pypdfium2 as pdfium  # type: ignore[import-untyped]
import pytest
from jsonschema import Draft202012Validator, FormatChecker

from tuskrag_parser.html_parser import parse_html
from tuskrag_parser.limits import (
    InvalidDocument,
    InvalidLimits,
    LimitExceeded,
    Limits,
)
from tuskrag_parser.pptx_parser import parse_pptx
from tuskrag_parser.render import parse_page_spec, render_pdf_pages

SCHEMA = json.loads(
    (
        Path(__file__).resolve().parents[3]
        / "contracts"
        / "parsed-document.schema.json"
    ).read_text(encoding="utf-8")
)
VALIDATOR = Draft202012Validator(SCHEMA, format_checker=FormatChecker())


def _build_minimal_pptx(path: Path, slide_xml: str) -> None:
    content_types = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
  <Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
</Types>"""
    presentation = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst>
</p:presentation>"""
    rels = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>"""
    with ZipFile(path, "w", compression=ZIP_DEFLATED) as zf:
        zf.writestr("[Content_Types].xml", content_types)
        zf.writestr("ppt/presentation.xml", presentation)
        zf.writestr("ppt/_rels/presentation.xml.rels", rels)
        zf.writestr("ppt/slides/slide1.xml", slide_xml)


def test_parse_pptx_valid_and_schema(tmp_path: Path) -> None:
    slide = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree><p:sp><p:txBody>
    <a:p><a:r><a:t>Q3 Review</a:t></a:r></a:p>
    <a:p><a:r><a:t>Revenue grew 25%.</a:t></a:r></a:p>
  </p:txBody></p:sp>
  <p:graphicFrame><a:tbl>
    <a:tr><a:tc><a:txBody><a:p><a:r><a:t>Region</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>ARR</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
    <a:tr><a:tc><a:txBody><a:p><a:r><a:t>APAC</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>10M</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
  </a:tbl></p:graphicFrame></p:spTree></p:cSld>
</p:sld>"""
    pptx_file = tmp_path / "deck.pptx"
    out_dir = tmp_path / "out"
    _build_minimal_pptx(pptx_file, slide)
    parse_pptx(pptx_file, out_dir, Limits())
    doc = json.loads((out_dir / "parsed.json").read_text(encoding="utf-8"))
    VALIDATOR.validate(doc)
    assert len(doc["pages"]) == 1
    assert doc["pages"][0]["blocks"][0] == {
        "type": "heading",
        "level": 1,
        "text": "Q3 Review",
    }
    assert doc["pages"][0]["blocks"][1] == {
        "type": "paragraph",
        "text": "Revenue grew 25%.",
    }
    assert doc["pages"][0]["blocks"][2] == {
        "type": "table",
        "cells": [["Region", "ARR"], ["APAC", "10M"]],
    }


def test_parse_html_strips_scripts_and_validates(tmp_path: Path) -> None:
    html_file = tmp_path / "doc.html"
    out_dir = tmp_path / "out"
    html_file.write_text(
        """<!doctype html>
<html>
<head><title>Ignored Title</title><style>.x{color:red}</style></head>
<body>
  <script>alert('xss')</script>
  <h1>Travel Policy</h1>
  <p>Hotel cap is 500 RMB.</p>
  <iframe>evil</iframe>
  <table><tr><th>City</th><th>Cap</th></tr><tr><td>Beijing</td><td>700</td></tr></table>
</body>
</html>""",
        encoding="utf-8",
    )
    parse_html(html_file, out_dir, Limits())
    doc = json.loads((out_dir / "parsed.json").read_text(encoding="utf-8"))
    VALIDATOR.validate(doc)
    blocks = doc["pages"][0]["blocks"]
    texts = json.dumps(blocks)
    assert "alert" not in texts
    assert "color:red" not in texts
    assert "evil" not in texts
    assert blocks[0] == {"type": "heading", "level": 1, "text": "Travel Policy"}
    assert blocks[1] == {"type": "paragraph", "text": "Hotel cap is 500 RMB."}
    assert blocks[2] == {
        "type": "table",
        "cells": [["City", "Cap"], ["Beijing", "700"]],
    }


def test_v07_malicious_inputs(tmp_path: Path) -> None:
    # 1. Path traversal inside PPTX zip
    trav = tmp_path / "trav.pptx"
    with ZipFile(trav, "w") as zf:
        zf.writestr("../../evil.xml", "<a/>")
    with pytest.raises(InvalidDocument):
        parse_pptx(trav, tmp_path / "o1", Limits())

    # 2. XXE / DOCTYPE inside PPTX XML
    xxe = tmp_path / "xxe.pptx"
    xxe_slide = """<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><p:sld/>"""
    _build_minimal_pptx(xxe, xxe_slide)
    with pytest.raises(InvalidDocument):
        parse_pptx(xxe, tmp_path / "o2", Limits())

    # 3. Encrypted OLE2 compound header
    enc = tmp_path / "enc.pptx"
    enc.write_bytes(olefile.MAGIC + b"\x00" * 512)
    with pytest.raises(InvalidDocument):
        parse_pptx(enc, tmp_path / "o3", Limits())

    # 4. Zip bomb (high compression ratio)
    bomb = tmp_path / "bomb.pptx"
    big_xml = (
        "<p:sld xmlns:p='http://schemas.openxmlformats.org/presentationml/2006/main'>"
        + (" " * 500_000)
        + "</p:sld>"
    )
    _build_minimal_pptx(bomb, big_xml)
    with pytest.raises(LimitExceeded):
        parse_pptx(bomb, tmp_path / "o4", Limits(max_compression_ratio=5))

    # 5. Oversized HTML
    big_html = tmp_path / "big.html"
    big_html.write_text("<p>" + ("a" * 1024) + "</p>", encoding="utf-8")
    with pytest.raises(LimitExceeded):
        parse_html(big_html, tmp_path / "o5", Limits(max_input_bytes=100))


def test_render_pdf_pages_and_spec(tmp_path: Path) -> None:
    assert parse_page_spec("1,3-4,2", 10) == [1, 3, 4, 2]
    with pytest.raises(InvalidLimits):
        parse_page_spec("0", 10)
    with pytest.raises(LimitExceeded):
        parse_page_spec("1-5", 3)

    pdf = pdfium.PdfDocument.new()
    pdf.new_page(200, 200)
    pdf.new_page(200, 200)
    pdf_path = tmp_path / "sample.pdf"
    pdf.save(str(pdf_path))
    pdf.close()

    rendered = render_pdf_pages(pdf_path, tmp_path / "imgs", "1-2", 72, Limits())
    assert len(rendered) == 2
    assert (tmp_path / "imgs" / "page-1.png").is_file()
    assert (tmp_path / "imgs" / "page-2.png").is_file()


def test_xberg_html_keeps_chinese_div_text_between_heading_and_table(
    tmp_path: Path,
) -> None:
    source = tmp_path / "sample.html"
    source.write_text(
        "<h1>差旅标准</h1><div>上海住宿上限 600 元。</div>"
        "<table><tr><td>北京</td><td>700</td></tr></table>",
        encoding="utf-8",
    )
    parse_html(source, tmp_path, Limits())
    doc = json.loads((tmp_path / "parsed.json").read_text(encoding="utf-8"))
    VALIDATOR.validate(doc)
    blocks = doc["pages"][0]["blocks"]
    assert blocks == [
        {"type": "heading", "level": 1, "text": "差旅标准"},
        {"type": "paragraph", "text": "上海住宿上限 600 元。"},
        {"type": "table", "cells": [["北京", "700"]]},
    ]
