import json
from html.parser import HTMLParser
from pathlib import Path

from tuskrag_parser import SCHEMA_VERSION, __version__
from tuskrag_parser.limits import InvalidDocument, LimitExceeded, Limits

HTML_DRIVER = "python-stdlib-htmlparser"
SKIP_TAGS = {"script", "style", "noscript", "template", "iframe", "svg"}
HEADING_TAGS = {"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}
BLOCK_TAGS = {"p", "li", "blockquote", "pre", "dt", "dd"}


class _DocHTMLParser(HTMLParser):
    def __init__(self, limits: Limits) -> None:
        super().__init__(convert_charrefs=True)
        self.limits = limits
        self.skip_depth = 0
        self.in_title = False
        self.title_parts: list[str] = []
        self.current_heading_level: int | None = None
        self.heading_parts: list[str] = []
        self.in_block = False
        self.block_parts: list[str] = []
        self.in_table = False
        self.in_row = False
        self.in_cell = False
        self.cell_parts: list[str] = []
        self.current_row: list[str] = []
        self.current_table: list[list[str]] = []
        self.blocks: list[dict[str, object]] = []
        self.char_count = 0

    def _add_block(self, block: dict[str, object]) -> None:
        if len(self.blocks) >= self.limits.max_blocks:
            raise LimitExceeded
        self.blocks.append(block)

    def _count_chars(self, text: str) -> None:
        self.char_count += sum(not c.isspace() for c in text)

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        t = tag.lower()
        if t in SKIP_TAGS:
            self.skip_depth += 1
            return
        if self.skip_depth > 0:
            return
        if t == "title":
            self.in_title = True
        elif t in HEADING_TAGS:
            self.current_heading_level = HEADING_TAGS[t]
            self.heading_parts = []
        elif t in BLOCK_TAGS:
            self.in_block = True
            self.block_parts = []
        elif t == "table":
            self.in_table = True
            self.current_table = []
        elif t == "tr" and self.in_table:
            self.in_row = True
            self.current_row = []
        elif t in {"td", "th"} and self.in_row:
            self.in_cell = True
            self.cell_parts = []

    def handle_endtag(self, tag: str) -> None:
        t = tag.lower()
        if t in SKIP_TAGS and self.skip_depth > 0:
            self.skip_depth -= 1
            return
        if self.skip_depth > 0:
            return
        if t == "title":
            self.in_title = False
        elif t in HEADING_TAGS and self.current_heading_level is not None:
            heading = " ".join("".join(self.heading_parts).split()).strip()
            level = self.current_heading_level
            self.current_heading_level = None
            if heading:
                self._count_chars(heading)
                self._add_block({"type": "heading", "level": level, "text": heading[:200]})
        elif t in BLOCK_TAGS and self.in_block:
            self.in_block = False
            text = " ".join("".join(self.block_parts).split()).strip()
            if text:
                self._count_chars(text)
                self._add_block({"type": "paragraph", "text": text})
        elif t in {"td", "th"} and self.in_cell:
            self.in_cell = False
            cell = " ".join("".join(self.cell_parts).split()).strip()
            self._count_chars(cell)
            self.current_row.append(cell)
        elif t == "tr" and self.in_row:
            self.in_row = False
            if len(self.current_row) > self.limits.max_columns:
                raise LimitExceeded
            if any(self.current_row):
                if len(self.current_table) >= self.limits.max_rows:
                    raise LimitExceeded
                self.current_table.append(self.current_row)
        elif t == "table" and self.in_table:
            self.in_table = False
            if self.current_table:
                self._add_block({"type": "table", "cells": self.current_table})

    def handle_data(self, data: str) -> None:
        if self.skip_depth > 0:
            return
        if self.in_title:
            self.title_parts.append(data)
        if self.current_heading_level is not None:
            self.heading_parts.append(data)
        elif self.in_cell:
            self.cell_parts.append(data)
        elif self.in_block:
            self.block_parts.append(data)

    def finalize(self) -> tuple[list[dict[str, object]], str]:
        title = " ".join("".join(self.title_parts).split()).strip()
        if not self.blocks and title:
            self._count_chars(title)
            self._add_block({"type": "heading", "level": 1, "text": title[:200]})
        if not self.blocks:
            raise InvalidDocument("empty html document")
        return self.blocks, title


def parse_html(
    source: Path, output_path: Path, limits: Limits, mime: str = "text/html"
) -> None:
    size = source.stat().st_size
    if size <= 0:
        raise InvalidDocument
    if size > limits.max_input_bytes:
        raise LimitExceeded
    raw = source.read_bytes()
    if len(raw) > limits.max_input_bytes:
        raise LimitExceeded
    text = raw.decode("utf-8", errors="strict")
    parser = _DocHTMLParser(limits)
    parser.feed(text)
    parser.close()
    blocks, title = parser.finalize()
    meta: dict[str, object] = {
        "parser_profile": f"tuskrag-parse/{__version__};html={HTML_DRIVER}"
    }
    if title:
        meta["title"] = title[:200]
    document = {
        "schema_version": SCHEMA_VERSION,
        "pages": [
            {
                "no": 1,
                "blocks": blocks,
                "signals": {"char_count": parser.char_count},
            }
        ],
        "meta": meta,
    }
    payload = json.dumps(document, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    if len(payload) > limits.max_output_bytes:
        raise LimitExceeded
    output_path.mkdir(parents=True, exist_ok=True)
    (output_path / "parsed.json").write_bytes(payload)
