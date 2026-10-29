import json
import os
import stat
from pathlib import Path
from typing import Any, Literal, NotRequired, TypedDict, cast
from zipfile import BadZipFile, ZipFile

from defusedxml import ElementTree
from defusedxml.common import DefusedXmlException
from docx import Document
from docx.document import Document as WordDocument
from docx.oxml.ns import qn
from docx.table import Table
from docx.text.hyperlink import Hyperlink
from docx.text.paragraph import Paragraph
from docx.text.run import Run
from lxml import etree

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.limits import (
    InvalidDocument,
    LimitExceeded,
    Limits,
    UnsupportedDocument,
)
from tuskrag_parser.ooxml import preflight, reject_compound_file

ALLOWED_PARAGRAPH = frozenset(
    qn(name)
    for name in (
        "w:pPr",
        "w:r",
        "w:hyperlink",
        "w:bookmarkStart",
        "w:bookmarkEnd",
        "w:proofErr",
    )
)
ALLOWED_RUN = frozenset(
    qn(name)
    for name in (
        "w:rPr",
        "w:t",
        "w:tab",
        "w:ptab",
        "w:br",
        "w:cr",
        "w:noBreakHyphen",
        "w:lastRenderedPageBreak",
    )
)
ALLOWED_TABLE = frozenset(qn(name) for name in ("w:tblPr", "w:tblGrid", "w:tr"))
ALLOWED_ROW = frozenset(qn(name) for name in ("w:trPr", "w:tblPrEx", "w:tc"))
ALLOWED_CELL = frozenset(qn(name) for name in ("w:tcPr", "w:p"))


class TextBlock(TypedDict):
    type: Literal["heading", "paragraph"]
    text: str
    level: NotRequired[int]


class TableBlock(TypedDict):
    type: Literal["table"]
    cells: list[list[str]]


type Block = TextBlock | TableBlock


def parse_docx(input_path: Path, output_path: Path, limits: Limits) -> None:
    with input_path.open("rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size <= 0:
            raise InvalidDocument
        if info.st_size > limits.max_input_bytes:
            raise LimitExceeded
        reject_compound_file(source)
        try:
            with ZipFile(source) as archive:
                preflight(archive, limits)
            source.seek(0)
            document = Document(source)
            blocks, characters = read_body(document, limits)
        except (
            BadZipFile,
            etree.XMLSyntaxError,
            ElementTree.ParseError,
            DefusedXmlException,
            KeyError,
            ValueError,
            IndexError,
        ) as error:
            raise InvalidDocument from error
    if not blocks:
        raise InvalidDocument
    parsed = {
        "schema_version": SCHEMA_VERSION,
        "pages": [{"no": 1, "blocks": blocks, "signals": {"char_count": characters}}],
        "meta": {
            "parser_profile": f"tuskrag-parse/{__version__};docx=python-docx-1.2.0"
        },
    }
    payload = json.dumps(parsed, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    if len(payload) > min(limits.max_output_bytes, info.st_size * 20):
        raise LimitExceeded
    (output_path / "parsed.json").write_bytes(payload)


def read_body(document: WordDocument, limits: Limits) -> tuple[list[Block], int]:
    body = document.element.body
    if any(child.tag not in {qn("w:p"), qn("w:tbl"), qn("w:sectPr")} for child in body):
        raise UnsupportedDocument
    unsupported = (
        ".//w:sdt | .//w:customXml | .//w:smartTag | .//w:ins | .//w:del | "
        ".//w:moveFrom | .//w:moveTo | .//w:altChunk | .//w:object | .//w:drawing | "
        ".//w:pict | .//w:footnoteReference | .//w:endnoteReference | .//w:fldSimple | "
        ".//w:fldChar | .//w:instrText | .//w:sym | .//w:tc/w:tbl | "
        ".//w:commentRangeStart | .//w:commentRangeEnd | .//w:rPr/w:vanish | .//w:rPr/w:webHidden"
    )
    if body.xpath(unsupported):
        raise UnsupportedDocument
    unsupported_parts = {"header", "footer", "footnotes", "endnotes", "comments"}
    if any(
        rel.reltype.rsplit("/", 1)[-1] in unsupported_parts
        for rel in document.part.rels.values()
    ):
        raise UnsupportedDocument
    for paragraph in body.xpath(".//w:p"):
        validate_paragraph(cast(etree._Element, paragraph))
    blocks: list[Block] = []
    characters = 0
    cells = 0
    table_rows = 0
    for item in document.iter_inner_content():
        if len(blocks) >= limits.max_blocks:
            raise LimitExceeded
        if isinstance(item, Paragraph):
            level = heading_level(item)
            text = item.text
            if len(text) > limits.max_field_chars:
                raise LimitExceeded
            if not text:
                continue
            block: TextBlock = {
                "type": "heading" if level is not None else "paragraph",
                "text": text,
            }
            if level is not None:
                block["level"] = level
            blocks.append(block)
            characters += sum(not character.isspace() for character in text)
        elif isinstance(item, Table):
            rows, count = read_table(item, limits, cells, table_rows)
            cells += count
            table_rows += len(rows)
            blocks.append({"type": "table", "cells": rows})
            characters += sum(
                not character.isspace()
                for row in rows
                for cell in row
                for character in cell
            )
        else:
            raise UnsupportedDocument
    return blocks, characters


def validate_paragraph(paragraph: etree._Element) -> None:
    for child in paragraph:
        if child.tag not in ALLOWED_PARAGRAPH:
            raise UnsupportedDocument
        if child.tag == qn("w:r"):
            runs = [child]
        elif child.tag == qn("w:hyperlink"):
            runs = list(child)
            if any(run.tag != qn("w:r") for run in runs):
                raise UnsupportedDocument
        else:
            continue
        if any(part.tag not in ALLOWED_RUN for run in runs for part in run):
            raise UnsupportedDocument


def heading_level(paragraph: Paragraph) -> int | None:
    properties = paragraph._p.pPr
    if properties is not None and properties.xpath("./w:numPr"):
        raise UnsupportedDocument
    direct_level: list[Any] = []
    if properties is not None:
        direct_level = properties.xpath("./w:outlineLvl/@w:val")
    style = paragraph.style
    seen: set[str] = set()
    inherited_level: list[Any] = []
    while style is not None:
        if style.style_id in seen or len(seen) >= 50:
            raise InvalidDocument
        seen.add(style.style_id)
        if style._element.xpath(
            "./w:pPr/w:numPr | ./w:rPr/w:vanish | ./w:rPr/w:webHidden"
        ):
            raise UnsupportedDocument
        if not inherited_level:
            inherited_level = style._element.xpath("./w:pPr/w:outlineLvl/@w:val")
        style = style.base_style
    for item in paragraph.iter_inner_content():
        runs = (
            [item]
            if isinstance(item, Run)
            else item.runs
            if isinstance(item, Hyperlink)
            else []
        )
        for run in runs:
            character_style = run.style
            seen.clear()
            while character_style is not None:
                if character_style.style_id in seen or len(seen) >= 50:
                    raise InvalidDocument
                seen.add(character_style.style_id)
                if character_style._element.xpath(
                    "./w:rPr/w:vanish | ./w:rPr/w:webHidden"
                ):
                    raise UnsupportedDocument
                character_style = character_style.base_style
    if direct_level:
        return outline_level(str(direct_level[0]))
    if inherited_level:
        return outline_level(str(inherited_level[0]))
    return None


def outline_level(value: str) -> int | None:
    level = int(value)
    if level == 9:
        return None
    if not 0 <= level <= 8:
        raise InvalidDocument
    return level + 1


def read_table(
    table: Table, limits: Limits, prior_cells: int, prior_rows: int
) -> tuple[list[list[str]], int]:
    for child in table._tbl:
        if child.tag not in ALLOWED_TABLE:
            raise UnsupportedDocument
        if child.tag == qn("w:tr"):
            for row_child in child:
                if row_child.tag not in ALLOWED_ROW:
                    raise UnsupportedDocument
                if row_child.tag == qn("w:tc") and any(
                    cell_child.tag not in ALLOWED_CELL for cell_child in row_child
                ):
                    raise UnsupportedDocument
    columns = len(table.columns)
    if (
        columns <= 0
        or columns > limits.max_columns
        or len(table.rows) > limits.max_rows - prior_rows
    ):
        raise LimitExceeded
    for raw in table._tbl.xpath(
        ".//w:gridSpan/@w:val | .//w:gridBefore/@w:val | .//w:gridAfter/@w:val"
    ):
        if not 0 <= int(str(raw)) <= limits.max_columns:
            raise LimitExceeded
    rows: list[list[str]] = []
    cells = prior_cells
    for row in table.rows:
        row_cells = row.cells
        if row.grid_cols_before + len(row_cells) + row.grid_cols_after != columns:
            raise InvalidDocument
        cells += columns
        if cells > limits.max_cells:
            raise LimitExceeded
        values = [""] * row.grid_cols_before
        for cell in row_cells:
            for paragraph in cell.paragraphs:
                heading_level(paragraph)
            value = cell.text
            if len(value) > limits.max_field_chars:
                raise LimitExceeded
            values.append(value)
        values.extend([""] * row.grid_cols_after)
        rows.append(values)
    if not rows:
        raise InvalidDocument
    return rows, cells - prior_cells
