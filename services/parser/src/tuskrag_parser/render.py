"""Render requested PDF pages to PNG images for VLM re-parsing."""

from pathlib import Path

import pypdfium2 as pdfium  # type: ignore[import-untyped]
import pypdfium2.raw as pdfium_raw  # type: ignore[import-untyped]

from tuskrag_parser.limits import (
    EncryptedDocument,
    InvalidDocument,
    InvalidLimits,
    LimitExceeded,
    Limits,
)


def parse_page_spec(spec: str, max_pages: int) -> list[int]:
    pages: list[int] = []
    seen: set[int] = set()
    for part in spec.split(","):
        item = part.strip()
        if not item:
            continue
        if "-" in item:
            pieces = item.split("-", 1)
            try:
                start = int(pieces[0].strip())
                end = int(pieces[1].strip())
            except ValueError as error:
                raise InvalidLimits from error
            if start <= 0 or end < start:
                raise InvalidLimits
            for p in range(start, end + 1):
                if p not in seen:
                    seen.add(p)
                    pages.append(p)
                    if len(pages) > max_pages:
                        raise LimitExceeded
        else:
            try:
                p = int(item)
            except ValueError as error:
                raise InvalidLimits from error
            if p <= 0:
                raise InvalidLimits
            if p not in seen:
                seen.add(p)
                pages.append(p)
                if len(pages) > max_pages:
                    raise LimitExceeded
    if not pages:
        raise InvalidLimits
    return pages


def render_pdf_pages(
    source: Path, out_dir: Path, pages_spec: str, dpi: int, limits: Limits
) -> list[dict[str, object]]:
    if dpi < 72 or dpi > 300:
        raise InvalidLimits
    if source.stat().st_size > limits.max_input_bytes:
        raise LimitExceeded
    pages = parse_page_spec(pages_spec, limits.max_pages)
    out_dir.mkdir(parents=True, exist_ok=True)

    try:
        doc = pdfium.PdfDocument(str(source))
    except pdfium.PdfiumError as error:
        err_code = getattr(error, "err_code", None)
        if err_code == pdfium_raw.FPDF_ERR_PASSWORD:
            raise EncryptedDocument from error
        raise InvalidDocument from error

    rendered: list[dict[str, object]] = []
    try:
        total_pages = len(doc)
        if total_pages > limits.max_pages:
            raise LimitExceeded
        scale = dpi / 72.0
        for page_num in pages:
            if page_num > total_pages:
                raise InvalidDocument(
                    f"page {page_num} out of range (1..{total_pages})"
                )
            page = doc[page_num - 1]
            bitmap = page.render(scale=scale)
            pil_image = bitmap.to_pil()
            out_file = out_dir / f"page-{page_num}.png"
            pil_image.save(out_file, format="PNG")
            rendered.append(
                {
                    "page": page_num,
                    "path": str(out_file),
                    "width": pil_image.width,
                    "height": pil_image.height,
                }
            )
            page.close()
    finally:
        doc.close()
    return rendered
