import json
import math
import os
import re
import stat
from dataclasses import dataclass
from datetime import date, datetime, time, timedelta
from importlib.metadata import version
from pathlib import Path, PurePosixPath
from typing import BinaryIO, Literal, TypedDict
from xml.etree.ElementTree import Element
from zipfile import BadZipFile, ZipFile

import openpyxl
from defusedxml import ElementTree
from defusedxml.common import DefusedXmlException
from lxml import etree
from openpyxl import load_workbook
from openpyxl.utils.cell import column_index_from_string
from python_calamine import CalamineError, CalamineWorkbook, PasswordError

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.limits import (
    EncryptedDocument,
    InvalidDocument,
    LimitExceeded,
    Limits,
    UnsupportedDocument,
)
from tuskrag_parser.ooxml import preflight, reject_compound_file

MAIN = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
DOC_REL = "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}"
PACKAGE_REL = "{http://schemas.openxmlformats.org/package/2006/relationships}"
WORKBOOK = "xl/workbook.xml"
WORKBOOK_RELS = "xl/_rels/workbook.xml.rels"
MAX_METADATA_BYTES = 4 * 1024 * 1024
MAX_SHEETS = 2000
CELL_REFERENCE = re.compile(r"^[A-Z]{1,3}[1-9][0-9]{0,6}$")
XLSX_DRIVER = (
    f"python-calamine-{version('python-calamine')}+openpyxl-{openpyxl.__version__}"
)


@dataclass(frozen=True, slots=True)
class Sheet:
    name: str
    path: str
    rows: int
    columns: int
    merges: tuple[tuple[int, int, int, int], ...]


class HeadingBlock(TypedDict):
    type: Literal["heading"]
    level: int
    text: str


class TableBlock(TypedDict):
    type: Literal["table"]
    cells: list[list[str]]


class Signals(TypedDict):
    char_count: int


class ParsedPage(TypedDict):
    no: int
    blocks: list[HeadingBlock | TableBlock]
    signals: Signals


def metadata_xml(archive: ZipFile, name: str) -> Element:
    info = archive.getinfo(name)
    if info.file_size > MAX_METADATA_BYTES:
        raise LimitExceeded
    return ElementTree.fromstring(archive.read(info), forbid_dtd=True)


def workbook_parts(archive: ZipFile, limits: Limits) -> list[tuple[str, str]]:
    workbook = metadata_xml(archive, WORKBOOK)
    relationships = metadata_xml(archive, WORKBOOK_RELS)
    if (
        workbook.tag != MAIN + "workbook"
        or relationships.tag != PACKAGE_REL + "Relationships"
    ):
        raise InvalidDocument
    for protection in (
        workbook.find(MAIN + "workbookProtection"),
        workbook.find(MAIN + "fileSharing"),
    ):
        if protection is not None and any(
            value not in {"0", "false"} for value in protection.attrib.values()
        ):
            raise EncryptedDocument
    relation_map: dict[str, tuple[str, str, str]] = {}
    for item in relationships:
        if item.tag != PACKAGE_REL + "Relationship":
            raise InvalidDocument
        ident, kind, target = item.get("Id"), item.get("Type"), item.get("Target")
        if not ident or not kind or not target or ident in relation_map:
            raise InvalidDocument
        relation_map[ident] = (kind, target, item.get("TargetMode", "Internal"))
    groups = workbook.findall(MAIN + "sheets")
    if len(groups) != 1 or not groups[0]:
        raise InvalidDocument
    if len(groups[0]) > MAX_SHEETS:
        raise LimitExceeded
    parts: list[tuple[str, str]] = []
    names: set[str] = set()
    paths: set[str] = set()
    sheet_ids: set[int] = set()
    for sheet in groups[0]:
        if sheet.tag != MAIN + "sheet":
            raise InvalidDocument
        name, ident = sheet.get("name"), sheet.get(DOC_REL + "id")
        if not name or not ident or name.casefold() in names:
            raise InvalidDocument
        sheet_id = sheet.get("sheetId", "")
        if not sheet_id.isascii() or not sheet_id.isdecimal():
            raise InvalidDocument
        number = int(sheet_id)
        if number <= 0 or number in sheet_ids:
            raise InvalidDocument
        sheet_ids.add(number)
        if len(name) > limits.max_field_chars:
            raise LimitExceeded
        if sheet.get("state", "visible") != "visible":
            raise UnsupportedDocument
        kind, target, mode = relation_map.get(ident, ("", "", ""))
        if not kind.endswith("/worksheet") or mode != "Internal":
            raise UnsupportedDocument
        path = PurePosixPath(
            target.lstrip("/") if target.startswith("/") else "xl/" + target
        )
        if (
            "\\" in target
            or ".." in path.parts
            or len(path.parts) != 3
            or path.parts[:2] != ("xl", "worksheets")
            or path.suffix != ".xml"
            or str(path).casefold() in paths
        ):
            raise InvalidDocument
        names.add(name.casefold())
        paths.add(str(path).casefold())
        parts.append((name, str(path)))
    return parts


def coordinate(value: str, limits: Limits) -> tuple[int, int]:
    if not CELL_REFERENCE.fullmatch(value):
        raise InvalidDocument
    letters = value.rstrip("0123456789")
    column = column_index_from_string(letters)
    row = int(value[len(letters) :])
    if row > limits.max_rows or column > limits.max_columns:
        raise LimitExceeded
    return row, column


def check_visibility(element: Element) -> None:
    hidden = element.get("hidden")
    if hidden in {"1", "true"}:
        raise UnsupportedDocument
    if hidden not in {None, "0", "false"}:
        raise InvalidDocument


def sheet_bounds(archive: ZipFile, name: str, path: str, limits: Limits) -> Sheet:
    rows = columns = 0
    references: set[str] = set()
    merged_cells: set[tuple[int, int]] = set()
    merges: list[tuple[int, int, int, int]] = []
    first = True
    with archive.open(path) as source:
        for event, element in ElementTree.iterparse(
            source, events=("start", "end"), forbid_dtd=True
        ):
            if event == "end":
                element.clear()
                continue
            if first:
                if element.tag != MAIN + "worksheet":
                    raise InvalidDocument
                first = False
            if element.tag == MAIN + "row":
                check_visibility(element)
                row_number = element.get("r")
                if row_number is not None:
                    if (
                        not row_number.isascii()
                        or not row_number.isdecimal()
                        or int(row_number) <= 0
                    ):
                        raise InvalidDocument
                    rows = max(rows, int(row_number))
                    if rows > limits.max_rows:
                        raise LimitExceeded
            elif element.tag == MAIN + "col":
                check_visibility(element)
            elif element.tag == MAIN + "c":
                ref = element.get("r", "")
                row, column = coordinate(ref, limits)
                if ref in references:
                    raise InvalidDocument
                references.add(ref)
                rows, columns = max(rows, row), max(columns, column)
                if element.get("t") == "e":
                    raise UnsupportedDocument
            elif element.tag in {
                MAIN + "f",
                MAIN + "drawing",
                MAIN + "legacyDrawing",
                MAIN + "headerFooter",
            }:
                raise UnsupportedDocument
            elif element.tag == MAIN + "sheetProtection":
                raise EncryptedDocument
            elif element.tag == MAIN + "mergeCell":
                ref = element.get("ref", "")
                parts = ref.split(":")
                if len(parts) != 2:
                    raise InvalidDocument
                first_row, first_col = coordinate(parts[0], limits)
                last_row, last_col = coordinate(parts[1], limits)
                if last_row < first_row or last_col < first_col:
                    raise InvalidDocument
                area = (last_row - first_row + 1) * (last_col - first_col + 1)
                if area <= 1:
                    raise InvalidDocument
                if area > limits.max_cells - len(merged_cells):
                    raise LimitExceeded
                for row in range(first_row, last_row + 1):
                    for column in range(first_col, last_col + 1):
                        if (row, column) in merged_cells:
                            raise InvalidDocument
                        merged_cells.add((row, column))
                rows, columns = max(rows, last_row), max(columns, last_col)
                merges.append((first_row, first_col, last_row, last_col))
            if rows * columns > limits.max_cells:
                raise LimitExceeded
    if first:
        raise InvalidDocument
    return Sheet(name, path, rows, columns, tuple(sorted(merges)))


def cell_text(value: object, limits: Limits) -> str:
    if isinstance(value, bool):
        text = "TRUE" if value else "FALSE"
    elif isinstance(value, str):
        text = value
    elif isinstance(value, int):
        text = str(value)
    elif isinstance(value, float):
        if not math.isfinite(value):
            raise InvalidDocument
        text = str(value)
    elif isinstance(value, (datetime, date, time)):
        text = value.isoformat()
    elif isinstance(value, timedelta):
        raise UnsupportedDocument
    else:
        raise InvalidDocument
    if "\x00" in text:
        raise InvalidDocument
    if len(text) > limits.max_field_chars:
        raise LimitExceeded
    return text


def same_value(python_value: object, rust_value: object) -> bool:
    if python_value is None:
        return rust_value == ""
    if isinstance(python_value, bool):
        return isinstance(rust_value, bool) and python_value == rust_value
    if isinstance(python_value, (int, float)):
        return (
            not isinstance(rust_value, bool)
            and isinstance(rust_value, (int, float))
            and python_value == rust_value
        )
    if (
        isinstance(python_value, datetime)
        and isinstance(rust_value, date)
        and not isinstance(rust_value, datetime)
    ):
        return python_value.time() == time.min and python_value.date() == rust_value
    return type(python_value) is type(rust_value) and python_value == rust_value


def read_workbook(
    source: BinaryIO, sheets: list[Sheet], limits: Limits, output_limit: int
) -> list[ParsedPage]:
    source.seek(0)
    workbook = load_workbook(source, data_only=False, keep_links=False)
    try:
        source.seek(0)
        with CalamineWorkbook.from_filelike(source) as calamine:
            if (
                workbook.sheetnames != [sheet.name for sheet in sheets]
                or calamine.sheet_names != workbook.sheetnames
            ):
                raise InvalidDocument
            if len(workbook.worksheets) != len(sheets):
                raise UnsupportedDocument
            pages: list[ParsedPage] = []
            encoded_size = 0
            for index, sheet in enumerate(sheets):
                python_sheet = workbook.worksheets[index]
                rust_sheet = calamine.get_sheet_by_index(index)
                python_merges = sorted(
                    (item.min_row, item.min_col, item.max_row, item.max_col)
                    for item in python_sheet.merged_cells.ranges
                )
                rust_merges = rust_sheet.merged_cell_ranges
                if (
                    rust_merges is None
                    or tuple(python_merges) != sheet.merges
                    or tuple(
                        sorted(
                            (start[0] + 1, start[1] + 1, end[0] + 1, end[1] + 1)
                            for start, end in rust_merges
                        )
                    )
                    != sheet.merges
                ):
                    raise InvalidDocument
                raw = rust_sheet.to_python(skip_empty_area=False)
                if len(raw) > sheet.rows or any(
                    len(raw_row) > sheet.columns for raw_row in raw
                ):
                    raise InvalidDocument
                for python_row in python_sheet.iter_rows():
                    for cell in python_row:
                        if cell.data_type == "f" or cell.comment is not None:
                            raise UnsupportedDocument
                        if (
                            cell.data_type == "n"
                            and cell.value is not None
                            and cell.number_format != "General"
                        ):
                            raise UnsupportedDocument
                        python_row_index, python_column_index = cell.row, cell.column
                        if python_row_index is None or python_column_index is None:
                            raise InvalidDocument
                        row_index, column_index = (
                            python_row_index - 1,
                            python_column_index - 1,
                        )
                        rust_value: object = ""
                        if row_index < len(raw) and column_index < len(raw[row_index]):
                            rust_value = raw[row_index][column_index]
                        if not same_value(cell.value, rust_value):
                            raise InvalidDocument
                cells = (
                    [["" for _ in range(sheet.columns)] for _ in range(sheet.rows)]
                    if sheet.columns
                    else []
                )
                for row_index, raw_row in enumerate(raw):
                    for column_index, value in enumerate(raw_row):
                        cells[row_index][column_index] = cell_text(value, limits)
                for first_row, first_col, last_row, last_col in sheet.merges:
                    anchor = cells[first_row - 1][first_col - 1]
                    for row in range(first_row - 1, last_row):
                        for column in range(first_col - 1, last_col):
                            if (row, column) != (
                                first_row - 1,
                                first_col - 1,
                            ) and cells[row][column] != "":
                                raise InvalidDocument
                            cells[row][column] = anchor
                encoded_size += len(
                    json.dumps(sheet.name, ensure_ascii=False).encode("utf-8")
                )
                for output_row in cells:
                    for output_cell in output_row:
                        encoded_size += (
                            len(
                                json.dumps(output_cell, ensure_ascii=False).encode(
                                    "utf-8"
                                )
                            )
                            + 1
                        )
                        if encoded_size > output_limit:
                            raise LimitExceeded
                blocks: list[HeadingBlock | TableBlock] = [
                    {"type": "heading", "level": 1, "text": sheet.name}
                ]
                if cells:
                    blocks.append({"type": "table", "cells": cells})
                characters = sum(not char.isspace() for char in sheet.name)
                characters += sum(
                    not char.isspace() for row in cells for cell in row for char in cell
                )
                pages.append(
                    {
                        "no": index + 1,
                        "blocks": blocks,
                        "signals": {"char_count": characters},
                    }
                )
            return pages
    finally:
        workbook.close()


def parse_xlsx(input_path: Path, output_path: Path, limits: Limits) -> None:
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
                parts = workbook_parts(archive, limits)
                sheets = [
                    sheet_bounds(archive, name, path, limits) for name, path in parts
                ]
                if (
                    sum(sheet.rows for sheet in sheets) > limits.max_rows
                    or sum(sheet.rows * sheet.columns for sheet in sheets)
                    > limits.max_cells
                ):
                    raise LimitExceeded
                if (
                    sum(1 + bool(sheet.rows and sheet.columns) for sheet in sheets)
                    > limits.max_blocks
                ):
                    raise LimitExceeded
            output_limit = min(limits.max_output_bytes, info.st_size * 20)
            pages = read_workbook(source, sheets, limits, output_limit)
        except PasswordError as error:
            raise EncryptedDocument from error
        except (
            BadZipFile,
            CalamineError,
            etree.XMLSyntaxError,
            ElementTree.ParseError,
            DefusedXmlException,
            KeyError,
            ValueError,
            IndexError,
            TypeError,
        ) as error:
            raise InvalidDocument from error
    parsed = {
        "schema_version": SCHEMA_VERSION,
        "pages": pages,
        "meta": {"parser_profile": f"tuskrag-parse/{__version__};xlsx={XLSX_DRIVER}"},
    }
    payload = json.dumps(parsed, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    if len(payload) > output_limit:
        raise LimitExceeded
    (output_path / "parsed.json").write_bytes(payload)
