import json
import re
from pathlib import Path
from xml.etree.ElementTree import Element
from zipfile import BadZipFile, ZipFile

from defusedxml import ElementTree
from defusedxml.common import DefusedXmlException

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.limits import InvalidDocument, LimitExceeded, Limits
from tuskrag_parser.ooxml import preflight, reject_compound_file

PPTX_DRIVER = "defusedxml-ooxml"
P_NS = "{http://schemas.openxmlformats.org/presentationml/2006/main}"
A_NS = "{http://schemas.openxmlformats.org/drawingml/2006/main}"
R_NS = "{http://schemas.openxmlformats.org/officeDocument/2006/relationships}"
REL_NS = "{http://schemas.openxmlformats.org/package/2006/relationships}"


def _read_xml(archive: ZipFile, name: str, max_bytes: int) -> Element:
    info = archive.getinfo(name)
    if info.file_size > max_bytes:
        raise LimitExceeded
    data = archive.read(info)
    if len(data) > max_bytes:
        raise LimitExceeded
    return ElementTree.fromstring(data, forbid_dtd=True)


def _slide_sort_key(name: str) -> tuple[int, str]:
    match = re.search(r"slide(\d+)\.xml$", name)
    return (int(match.group(1)) if match else 0, name)


def _resolve_slide_paths(archive: ZipFile, limits: Limits) -> list[str]:
    names = set(archive.namelist())
    if "ppt/presentation.xml" in names and "ppt/_rels/presentation.xml.rels" in names:
        try:
            pres = _read_xml(archive, "ppt/presentation.xml", limits.max_uncompressed_bytes)
            rels = _read_xml(
                archive, "ppt/_rels/presentation.xml.rels", limits.max_uncompressed_bytes
            )
            rel_map: dict[str, str] = {}
            for rel in rels.findall(f"{REL_NS}Relationship"):
                rid = rel.attrib.get("Id")
                target = rel.attrib.get("Target", "")
                if rid and target:
                    normalized = target.lstrip("/")
                    if not normalized.startswith("ppt/"):
                        normalized = f"ppt/{normalized.lstrip('./')}"
                    rel_map[rid] = normalized
            ordered: list[str] = []
            for sld in pres.findall(f".//{P_NS}sldIdLst/{P_NS}sldId"):
                rid = sld.attrib.get(f"{R_NS}id")
                if rid and rid in rel_map and rel_map[rid] in names:
                    ordered.append(rel_map[rid])
            if ordered:
                return ordered
        except (DefusedXmlException, KeyError):
            pass
    slides = [
        n
        for n in names
        if n.startswith("ppt/slides/slide") and n.endswith(".xml")
    ]
    slides.sort(key=_slide_sort_key)
    return slides


def _extract_paragraphs(root: Element) -> list[str]:
    lines: list[str] = []
    for para in root.findall(f".//{P_NS}sp/{P_NS}txBody/{A_NS}p"):
        runs = [t.text for t in para.findall(f".//{A_NS}t") if t.text]
        text = "".join(runs).strip()
        if text:
            lines.append(text)
    return lines


def _extract_tables(root: Element, limits: Limits) -> list[list[list[str]]]:
    tables: list[list[list[str]]] = []
    for tbl in root.findall(f".//{A_NS}tbl"):
        rows: list[list[str]] = []
        cells_total = 0
        for tr in tbl.findall(f"{A_NS}tr"):
            cells: list[str] = []
            for tc in tr.findall(f"{A_NS}tc"):
                parts = [
                    "".join(t.text for t in p.findall(f".//{A_NS}t") if t.text).strip()
                    for p in tc.findall(f".//{A_NS}p")
                ]
                cells.append(" ".join(p for p in parts if p))
            if len(cells) > limits.max_columns:
                raise LimitExceeded
            cells_total += len(cells)
            if len(rows) >= limits.max_rows or cells_total > limits.max_cells:
                raise LimitExceeded
            if any(cells):
                rows.append(cells)
        if rows:
            tables.append(rows)
    return tables


def parse_pptx(source: Path, output_path: Path, limits: Limits) -> None:
    size = source.stat().st_size
    if size <= 0:
        raise InvalidDocument
    if size > limits.max_input_bytes:
        raise LimitExceeded
    with source.open("rb") as handle:
        reject_compound_file(handle)
        try:
            with ZipFile(handle) as archive:
                try:
                    preflight(archive, limits)
                except DefusedXmlException as error:
                    raise InvalidDocument from error
                slide_paths = _resolve_slide_paths(archive, limits)
                if not slide_paths:
                    raise InvalidDocument("no slides in pptx")
                if len(slide_paths) > limits.max_pages:
                    raise LimitExceeded

                pages: list[dict[str, object]] = []
                total_blocks = 0
                for idx, slide_path in enumerate(slide_paths, start=1):
                    try:
                        root = _read_xml(
                            archive, slide_path, limits.max_uncompressed_bytes
                        )
                    except (DefusedXmlException, KeyError) as error:
                        raise InvalidDocument from error

                    paragraphs = _extract_paragraphs(root)
                    tables = _extract_tables(root, limits)
                    blocks: list[dict[str, object]] = []
                    char_count = 0
                    for p_idx, para in enumerate(paragraphs):
                        total_blocks += 1
                        if total_blocks > limits.max_blocks:
                            raise LimitExceeded
                        char_count += sum(not c.isspace() for c in para)
                        if p_idx == 0:
                            blocks.append({"type": "heading", "level": 1, "text": para[:200]})
                        else:
                            blocks.append({"type": "paragraph", "text": para})
                    for table in tables:
                        total_blocks += 1
                        if total_blocks > limits.max_blocks:
                            raise LimitExceeded
                        for row in table:
                            for cell in row:
                                char_count += sum(not c.isspace() for c in cell)
                        blocks.append({"type": "table", "cells": table})
                    if not blocks:
                        heading = f"Slide {idx}"
                        char_count = len(heading)
                        blocks.append({"type": "heading", "level": 1, "text": heading})
                    pages.append(
                        {
                            "no": idx,
                            "blocks": blocks,
                            "signals": {"char_count": char_count},
                        }
                    )

                document = {
                    "schema_version": SCHEMA_VERSION,
                    "pages": pages,
                    "meta": {
                        "parser_profile": f"tuskrag-parse/{__version__};pptx={PPTX_DRIVER}"
                    },
                }
                payload = json.dumps(
                    document, ensure_ascii=False, separators=(",", ":")
                ).encode("utf-8")
                if len(payload) > limits.max_output_bytes:
                    raise LimitExceeded
                output_path.mkdir(parents=True, exist_ok=True)
                (output_path / "parsed.json").write_bytes(payload)
        except BadZipFile as error:
            raise InvalidDocument from error
