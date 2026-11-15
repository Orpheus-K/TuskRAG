"""Bounded native PDF extraction; ambiguous structure fails explicitly."""

import itertools
import json
import math
import os
import stat
import unicodedata
from collections import Counter
from contextlib import closing
from dataclasses import dataclass
from importlib.metadata import version
from pathlib import Path
from statistics import median
from typing import Any

import pdfplumber
import pypdfium2 as pdfium  # type: ignore[import-untyped]
import pypdfium2.raw as pdfium_raw  # type: ignore[import-untyped]
from pdfminer.pdfexceptions import PDFException
from pdfminer.psexceptions import PSException

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.limits import (
    EncryptedDocument,
    InvalidDocument,
    LimitExceeded,
    Limits,
    UnsupportedDocument,
)

PDF_DRIVER = f"pdfplumber-{version('pdfplumber')}+pypdfium2-{version('pypdfium2')}"
MAX_PAGE_CHARS = 1_000_000
MAX_PAGE_IMAGES = 1_024


@dataclass(slots=True)
class Line:
    text: str
    box: tuple[float, float, float, float]
    size: float
    weight: int
    heading_level: int | None = None


@dataclass(slots=True)
class Table:
    cells: list[list[str]]
    box: tuple[float, float, float, float]


@dataclass(slots=True)
class Page:
    width: float
    height: float
    lines: list[Line]
    tables: list[Table]
    image_coverage: float
    table_suspect: bool


def parse_pdf(input_path: Path, output_path: Path, limits: Limits) -> None:
    with input_path.open("rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size <= 0:
            raise InvalidDocument
        if info.st_size > limits.max_input_bytes:
            raise LimitExceeded
        if source.read(5) != b"%PDF-":
            raise InvalidDocument

    try:
        document = pdfium.PdfDocument(input_path)
    except pdfium.PdfiumError as error:
        if pdfium_raw.FPDF_GetLastError() == pdfium_raw.FPDF_ERR_PASSWORD:
            raise EncryptedDocument from error
        raise InvalidDocument from error

    try:
        with document:
            if pdfium_raw.FPDF_GetSecurityHandlerRevision(document.raw) >= 0:
                raise EncryptedDocument
            page_count = len(document)
            if page_count <= 0:
                raise InvalidDocument
            if page_count > limits.max_pages:
                raise LimitExceeded
            bookmarks = read_bookmarks(document, page_count, limits)
            with pdfplumber.open(input_path) as parsed:
                if parsed.doc.encryption is not None:
                    raise EncryptedDocument
                if not parsed.doc.is_extractable or len(parsed.pages) != page_count:
                    raise UnsupportedDocument
                pages: list[Page] = []
                total_blocks = total_rows = total_cells = 0
                for index, page in enumerate(parsed.pages):
                    with closing(document[index]) as native_page:
                        extracted = read_page(page, native_page, limits)
                    page.close()
                    total_blocks += len(extracted.lines) + len(extracted.tables)
                    total_rows += sum(len(table.cells) for table in extracted.tables)
                    total_cells += sum(
                        len(row) for table in extracted.tables for row in table.cells
                    )
                    if (
                        total_blocks > limits.max_blocks
                        or total_rows > limits.max_rows
                        or total_cells > limits.max_cells
                    ):
                        raise LimitExceeded
                    pages.append(extracted)
                assign_headings(pages, bookmarks)
                result = build_document(pages, limits)
    except (
        pdfium.PdfiumError,
        PDFException,
        PSException,
        ValueError,
        TypeError,
        KeyError,
        IndexError,
        OverflowError,
    ) as error:
        raise InvalidDocument from error
    payload = json.dumps(result, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    if len(payload) > min(limits.max_output_bytes, info.st_size * 20):
        raise LimitExceeded
    (output_path / "parsed.json").write_bytes(payload)


def read_bookmarks(
    document: Any, page_count: int, limits: Limits
) -> list[tuple[int, str, int]]:
    result: list[tuple[int, str, int]] = []
    for bookmark in document.get_toc():
        if len(result) >= limits.max_blocks:
            raise LimitExceeded
        title = str(bookmark.get_title()).strip()
        destination = bookmark.get_dest()
        index = destination.get_index() if destination is not None else None
        level = bookmark.level + 1
        if len(title) > limits.max_field_chars:
            raise LimitExceeded
        if (
            not title
            or index is None
            or not 0 <= index < page_count
            or not 1 <= level <= 6
        ):
            raise UnsupportedDocument
        result.append((index, normalize_line(title), level))
    return result


def _is_link_annotation(annot: dict[str, Any]) -> bool:
    """Allow harmless hyperlink annotations (/Link) while keeping other annotations blocked."""
    subtype = annot.get("data", {}).get("Subtype")
    name = getattr(subtype, "name", None)
    if isinstance(name, str):
        return name == "Link"
    return str(subtype).lstrip("/'") == "Link"


def read_page(page: Any, native_page: Any, limits: Limits) -> Page:
    width, height = float(page.width), float(page.height)
    if (
        not math.isfinite(width)
        or not math.isfinite(height)
        or width <= 0
        or height <= 0
    ):
        raise InvalidDocument
    if width > 1_000_000 or height > 1_000_000:
        raise LimitExceeded
    if page.rotation % 360 != 0 or tuple(page.bbox) != (0, 0, width, height):
        raise UnsupportedDocument
    chars = page.chars
    if len(chars) > MAX_PAGE_CHARS:
        raise LimitExceeded
    if any(not _is_link_annotation(annot) for annot in (page.annots or ())):
        raise UnsupportedDocument
    tables = read_tables(page, limits)
    boxes = [table.box for table in tables]
    for index, box in enumerate(boxes):
        if not within_page(box, width, height):
            raise UnsupportedDocument
        if any(overlap(box, other) for other in boxes[index + 1 :]):
            raise UnsupportedDocument

    def outside_tables(obj: dict[str, Any]) -> bool:
        if obj.get("object_type") != "char":
            return True
        x = (float(obj["x0"]) + float(obj["x1"])) / 2
        y = (float(obj["top"]) + float(obj["bottom"])) / 2
        return not any(
            left <= x < right and top <= y < bottom
            for left, top, right, bottom in boxes
        )

    lines: list[Line] = []
    for line in page.filter(outside_tables).extract_text_lines(return_chars=True):
        text = str(line["text"]).strip()
        line_chars = line["chars"]
        if not text:
            continue
        if len(text) > limits.max_field_chars or len(lines) >= limits.max_blocks:
            raise LimitExceeded
        if has_column_gap(line_chars, width):
            raise UnsupportedDocument
        sizes = [
            float(char["size"]) for char in line_chars if str(char["text"]).strip()
        ]
        if not sizes or any(not math.isfinite(size) or size <= 0 for size in sizes):
            raise InvalidDocument
        box = get_box(line)
        if not within_page(box, width, height):
            raise UnsupportedDocument
        lines.append(Line(text, box, median(sizes), len(sizes)))
    if has_staggered_columns(lines, width):
        raise UnsupportedDocument
    extracted = "".join(str(char["text"]) for char in chars)
    reconstructed = "".join(line.text for line in lines) + "".join(
        cell for table in tables for row in table.cells for cell in row
    )
    with closing(native_page.get_textpage()) as text_page:
        native = text_page.get_text_bounded()
    if len(native) > MAX_PAGE_CHARS or len(extracted) > MAX_PAGE_CHARS:
        raise LimitExceeded
    if characters(extracted) != characters(native) or characters(
        extracted
    ) != characters(reconstructed):
        raise UnsupportedDocument
    horizontal = sum(edge.get("orientation") == "h" for edge in page.edges)
    vertical = sum(edge.get("orientation") == "v" for edge in page.edges)
    return Page(
        width,
        height,
        lines,
        tables,
        image_coverage(page.images, width, height),
        not tables and horizontal >= 2 and vertical >= 2,
    )


def read_tables(page: Any, limits: Limits) -> list[Table]:
    result: list[Table] = []
    total_rows = total_cells = 0
    for table in page.find_tables():
        rows = table.extract()
        if not rows:
            raise UnsupportedDocument
        if len(rows) > limits.max_rows - total_rows:
            raise LimitExceeded
        total_rows += len(rows)
        width = len(rows[0])
        if width > limits.max_columns:
            raise LimitExceeded
        if width <= 0 or any(len(row) != width for row in rows):
            raise UnsupportedDocument
        cells: list[list[str]] = []
        for row in rows:
            if any(value is None for value in row):
                raise UnsupportedDocument
            values = [str(value) for value in row]
            if any(len(value) > limits.max_field_chars for value in values):
                raise LimitExceeded
            total_cells += len(values)
            if total_cells > limits.max_cells:
                raise LimitExceeded
            cells.append(values)
        result.append(Table(cells, box_tuple(table.bbox)))
        if len(result) > limits.max_blocks:
            raise LimitExceeded
    return result


def get_box(value: dict[str, Any]) -> tuple[float, float, float, float]:
    return box_tuple((value["x0"], value["top"], value["x1"], value["bottom"]))


def box_tuple(values: Any) -> tuple[float, float, float, float]:
    box = tuple(float(value) for value in values)
    if (
        len(box) != 4
        or not all(math.isfinite(value) for value in box)
        or box[0] > box[2]
        or box[1] > box[3]
    ):
        raise InvalidDocument
    return box[0], box[1], box[2], box[3]


def overlap(
    first: tuple[float, float, float, float], second: tuple[float, float, float, float]
) -> bool:
    return (
        first[0] < second[2]
        and second[0] < first[2]
        and first[1] < second[3]
        and second[1] < first[3]
    )


def within_page(
    box: tuple[float, float, float, float], width: float, height: float
) -> bool:
    return 0 <= box[0] < box[2] <= width and 0 <= box[1] < box[3] <= height


def characters(value: str) -> Counter[str]:
    return Counter(
        character
        for character in unicodedata.normalize("NFC", value)
        if not character.isspace()
    )


def normalize_line(value: str) -> str:
    return " ".join(unicodedata.normalize("NFC", value).split())


def has_column_gap(chars: list[dict[str, Any]], width: float) -> bool:
    ordered = sorted(chars, key=lambda char: float(char["x0"]))
    for index in range(1, len(ordered)):
        gap = float(ordered[index]["x0"]) - float(ordered[index - 1]["x1"])
        if gap > max(60.0, width * 0.15) and index >= 4 and len(ordered) - index >= 4:
            return True
    return False


def has_staggered_columns(lines: list[Line], width: float) -> bool:
    if len(lines) < 4:
        return False
    ordered = sorted(lines, key=lambda line: line.box[0])
    gap, split = max(
        (ordered[index].box[0] - ordered[index - 1].box[0], index)
        for index in range(1, len(ordered))
    )
    if gap <= max(60.0, width * 0.15) or split < 2 or len(ordered) - split < 2:
        return False
    boundary = ordered[split].box[0] - max(10.0, width * 0.02)
    left = [line for line in ordered[:split] if line.box[2] <= boundary]
    right = ordered[split:]
    if len(left) < 2:
        return False
    return max(
        min(line.box[1] for line in left), min(line.box[1] for line in right)
    ) < min(max(line.box[3] for line in left), max(line.box[3] for line in right))


def image_coverage(images: list[dict[str, Any]], width: float, height: float) -> float:
    if len(images) > MAX_PAGE_IMAGES:
        raise LimitExceeded
    boxes = []
    for image in images:
        left, top, right, bottom = get_box(image)
        clipped = (
            max(0.0, left),
            max(0.0, top),
            min(width, right),
            min(height, bottom),
        )
        if clipped[0] < clipped[2] and clipped[1] < clipped[3]:
            boxes.append(clipped)
    edges = sorted({edge for box in boxes for edge in (box[0], box[2])})
    area = 0.0
    for left, right in itertools.pairwise(edges):
        intervals = sorted(
            (box[1], box[3]) for box in boxes if box[0] < right and box[2] > left
        )
        covered = 0.0
        start = end = 0.0
        for top, bottom in intervals:
            if top > end:
                covered += end - start
                start, end = top, bottom
            else:
                end = max(end, bottom)
        covered += end - start
        area += (right - left) * covered
    return min(1.0, area / (width * height))


def assign_headings(pages: list[Page], bookmarks: list[tuple[int, str, int]]) -> None:
    if bookmarks:
        for page_index, title, level in bookmarks:
            matches = [
                line
                for line in pages[page_index].lines
                if normalize_line(line.text) == title
            ]
            if len(matches) != 1 or matches[0].heading_level is not None:
                raise UnsupportedDocument
            matches[0].heading_level = level
        return
    lines = [line for page in pages for line in page.lines]
    if not lines:
        return
    weights: Counter[float] = Counter()
    for line in lines:
        weights[round(line.size, 1)] += line.weight
    body = weights.most_common(1)[0][0]
    sizes = sorted(
        {round(line.size, 1) for line in lines if line.size > body * 1.2}, reverse=True
    )
    if len(sizes) > 6:
        raise UnsupportedDocument
    for line in lines:
        size = round(line.size, 1)
        if (
            size in sizes
            and len(line.text) <= 120
            and not line.text.endswith(("。", ".", "！", "？", "!", "?"))
        ):
            line.heading_level = sizes.index(size) + 1


def build_document(pages: list[Page], limits: Limits) -> dict[str, Any]:
    output: list[dict[str, Any]] = []
    total_blocks = total_rows = total_cells = 0
    for index, page in enumerate(pages):
        blocks: list[tuple[tuple[float, float], dict[str, Any]]] = []
        for line in page.lines:
            block: dict[str, Any] = {
                "type": "heading" if line.heading_level is not None else "paragraph",
                "text": line.text,
                "bbox": line.box,
            }
            if line.heading_level is not None:
                block["level"] = line.heading_level
            blocks.append(((line.box[1], line.box[0]), block))
        for table in page.tables:
            total_rows += len(table.cells)
            total_cells += sum(len(row) for row in table.cells)
            blocks.append(
                (
                    (table.box[1], table.box[0]),
                    {"type": "table", "cells": table.cells, "bbox": table.box},
                )
            )
        total_blocks += len(blocks)
        if (
            total_blocks > limits.max_blocks
            or total_rows > limits.max_rows
            or total_cells > limits.max_cells
        ):
            raise LimitExceeded
        blocks.sort(key=lambda item: item[0])
        content = [block for _, block in blocks]
        texts = [block["text"] for block in content if "text" in block]
        texts.extend(
            cell
            for block in content
            if "cells" in block
            for row in block["cells"]
            for cell in row
        )
        output.append(
            {
                "no": index + 1,
                "width": page.width,
                "height": page.height,
                "blocks": content,
                "signals": {
                    "char_count": sum(
                        not char.isspace() for text in texts for char in text
                    ),
                    "image_coverage": page.image_coverage,
                    "table_suspect": page.table_suspect,
                },
            }
        )
    return {
        "schema_version": SCHEMA_VERSION,
        "pages": output,
        "meta": {"parser_profile": f"tuskrag-parse/{__version__};pdf={PDF_DRIVER}"},
    }
