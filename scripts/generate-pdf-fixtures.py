"""Generate synthetic PDF parser fixtures with ReportLab, Pillow and pypdf."""

import os
from pathlib import Path

from PIL import Image, ImageDraw
from pypdf import PdfReader, PdfWriter
from reportlab.lib.utils import ImageReader
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.pdfgen import canvas

ROOT = Path(__file__).resolve().parents[1] / "services" / "parser" / "tests" / "fixtures" / "pdf"
PAGE_SIZE = (595, 842)


def create_policy() -> None:
    doc = canvas.Canvas(str(ROOT / "policy.pdf"), pagesize=PAGE_SIZE, invariant=1)
    doc.bookmarkPage("policy")
    doc.addOutlineEntry("差旅管理办法", "policy", level=0)
    doc.setFont("NotoSansSC", 20)
    doc.drawString(60, 780, "差旅管理办法")
    doc.setFont("NotoSansSC", 12)
    doc.drawString(60, 744, "出差前须提出申请。")
    xs = (60, 205, 350)
    ys = (700, 672, 644, 616)
    for x in xs:
        doc.line(x, ys[-1], x, ys[0])
    for y in ys:
        doc.line(xs[0], y, xs[-1], y)
    for y, left, right in (
        (681, "城市", "住宿上限"),
        (653, "北京", "600"),
        (625, "上海", "450"),
    ):
        doc.drawString(68, y, left)
        doc.drawString(213, y, right)
    doc.showPage()
    doc.bookmarkPage("reimbursement")
    doc.addOutlineEntry("报销流程", "reimbursement", level=1)
    doc.setFont("NotoSansSC", 16)
    doc.drawString(60, 780, "报销流程")
    doc.setFont("NotoSansSC", 12)
    doc.drawString(60, 744, "员工应提交票据并完成审核。")
    doc.save()


def create_scanned() -> None:
    doc = canvas.Canvas(str(ROOT / "scanned.pdf"), pagesize=PAGE_SIZE, invariant=1)
    image = Image.new("RGB", (600, 840), "white")
    drawing = ImageDraw.Draw(image)
    drawing.rectangle((20, 20, 580, 820), outline="black", width=3)
    for y in range(100, 700, 70):
        drawing.line((70, y, 530, y), fill="black", width=4)
    doc.drawImage(ImageReader(image), 0, 0, width=PAGE_SIZE[0], height=PAGE_SIZE[1])
    doc.save()


def create_columns() -> None:
    doc = canvas.Canvas(str(ROOT / "columns.pdf"), pagesize=PAGE_SIZE, invariant=1)
    doc.setFont("NotoSansSC", 12)
    for offset in range(4):
        y = 780 - offset * 24
        doc.drawString(60, y, f"左栏第{offset + 1}条内容")
        doc.drawString(335, y, f"右栏第{offset + 1}条内容")
    doc.save()


def create_staggered_columns() -> None:
    doc = canvas.Canvas(str(ROOT / "columns-staggered.pdf"), pagesize=PAGE_SIZE, invariant=1)
    doc.setFont("NotoSansSC", 12)
    for offset in range(4):
        doc.drawString(60, 780 - offset * 24, f"左栏第{offset + 1}条内容")
        doc.drawString(335, 771 - offset * 24, f"右栏第{offset + 1}条内容")
    doc.save()


def create_encrypted() -> None:
    writer = PdfWriter()
    writer.append(PdfReader(ROOT / "policy.pdf"))
    writer.encrypt("fixture-password")
    with (ROOT / "encrypted.pdf").open("wb") as target:
        writer.write(target)


if __name__ == "__main__":
    pdfmetrics.registerFont(TTFont("NotoSansSC", os.environ["TUSKRAG_TEST_NOTO_FONT"]))
    create_policy()
    create_scanned()
    create_columns()
    create_staggered_columns()
    create_encrypted()
