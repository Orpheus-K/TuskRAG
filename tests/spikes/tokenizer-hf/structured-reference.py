"""Count fixed synthetic structured chunks with the pinned reference tokenizer."""

import argparse
import hashlib
import json
from importlib.metadata import version
from pathlib import Path

from tokenizers import Tokenizer

TOKENIZER_SHA256 = "21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08"


def main() -> None:
    argument_parser = argparse.ArgumentParser()
    argument_parser.add_argument("tokenizer", type=Path)
    args = argument_parser.parse_args()
    if version("tokenizers") != "0.22.1":
        raise SystemExit("tokenizers 0.22.1 is required")
    if hashlib.sha256(args.tokenizer.read_bytes()).hexdigest() != TOKENIZER_SHA256:
        raise SystemExit("unexpected tokenizer checksum")
    tokenizer = Tokenizer.from_file(str(args.tokenizer))
    prefix = "管理制度 › 差旅\n"
    parent = "12. 第十二条：报销规定。"
    children = [
        "    - 事前提交申请。",
        "    - 票据需真实完整。",
        "    - 标准依据出差城市。",
        "    - 超标须提前审批。",
    ]
    next_clause = "13. 第十三条：复核。"
    whole = prefix + "\n".join([parent, *children, next_clause])
    chunks = [
        prefix + "\n".join([parent, *children[:2]]),
        prefix + "\n".join([parent, children[2]]),
        prefix + "\n".join([parent, children[3]]),
        prefix + next_clause,
    ]
    plan_list = [
        "\n".join([parent, *children[:2]]),
        "\n".join([parent, children[2]]),
        "\n".join([parent, children[3]]),
        next_clause,
    ]
    code = 'def safe():\n    return "<data>"\n'
    paragraphs = [
        "申请需要事先提交。",
        "住宿标准按出差城市执行。",
        "超出费用须提前审批。",
        "票据应真实完整。",
    ]
    parent_parts = ["\n\n".join(paragraphs[:2]), "\n\n".join(paragraphs[2:])]
    table_rows = [
        ["城市", "上限", "用途"],
        ["北京", "600", "住宿"],
        ["上海", "450", "住宿"],
        ["成都", "350", "住宿"],
        ["深圳", "700", "住宿"],
    ]

    def table_line(row: list[str]) -> str:
        return "| " + " | ".join(row) + " |"

    table_header = table_line(table_rows[0]) + "\n" + table_line(["---"] * 3)
    table_parents = [
        table_header + "\n" + table_line(row) for row in table_rows[1:]
    ]
    table_search = [
        part
        + "\n"
        + "\n".join(
            f"{name}: {value}"
            for name, value in zip(table_rows[0], row, strict=True)
        )
        for part, row in zip(table_parents, table_rows[1:], strict=True)
    ]
    print(
        json.dumps(
            {
                "list_unsplit": len(tokenizer.encode(whole).ids),
                "list_chunks": [len(tokenizer.encode(chunk).ids) for chunk in chunks],
                "code": len(tokenizer.encode(prefix + code).ids),
                "plan_list_parent_unsplit": len(
                    tokenizer.encode("\n".join([parent, *children, next_clause])).ids
                ),
                "plan_list_parents": [
                    len(tokenizer.encode(part).ids) for part in plan_list
                ],
                "plan_list_search_chunks": [
                    len(tokenizer.encode("差旅\n" + part).ids) for part in plan_list
                ],
                "parent_unsplit": len(tokenizer.encode("\n\n".join(paragraphs)).ids),
                "parent_pieces": [
                    len(tokenizer.encode(part).ids) for part in parent_parts
                ],
                "plan_search_unsplit": len(
                    tokenizer.encode("差旅\n" + "\n\n".join(paragraphs)).ids
                ),
                "plan_search_chunks": [
                    len(tokenizer.encode("差旅\n" + "\n\n".join(part)).ids)
                    for part in (paragraphs[:2], paragraphs[1:])
                ],
                "long_paragraph_unsplit": len(
                    tokenizer.encode("".join(paragraphs)).ids
                ),
                "long_paragraph_parents": [
                    len(tokenizer.encode("".join(part)).ids)
                    for part in (paragraphs[:2], paragraphs[2:])
                ],
                "long_paragraph_search_chunks": [
                    len(tokenizer.encode("差旅\n" + "".join(part)).ids)
                    for part in (paragraphs[:2], paragraphs[1:])
                ],
                "plan_table_parent_unsplit": len(
                    tokenizer.encode(
                        table_header
                        + "".join("\n" + table_line(row) for row in table_rows[1:])
                    ).ids
                ),
                "plan_table_parent_pieces": [
                    len(tokenizer.encode(part).ids) for part in table_parents
                ],
                "plan_table_search_chunks": [
                    len(tokenizer.encode(part).ids) for part in table_search
                ],
            },
            separators=(",", ":"),
        )
    )


if __name__ == "__main__":
    main()
