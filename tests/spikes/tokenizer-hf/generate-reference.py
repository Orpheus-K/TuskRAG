"""Generate synthetic BGE-M3 reference IDs with the pinned HF tokenizer."""

import argparse
import hashlib
import itertools
import json
import random
from importlib.metadata import version
from pathlib import Path

from tokenizers import Tokenizer

TOKENIZER_SHA256 = "21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08"
GRAPHEME_SHA256 = "ee2b9354d270ac061b29f09662cafea06341d77e704b8cc6bd72aaeeda363cb5"
RANDOM_SHA256 = "45b76e67ca05a8dd0bb1c711a141c8f3f0ecbaf981d298f77f9f73b98895e226"
BASE_SHA256 = "be9e5c23ecaf0af0e064b76b63440e90de801822fe38c105b0da661419ac6720"


def verified(path: Path, digest: str) -> bytes:
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != digest:
        raise SystemExit(f"unexpected checksum: {path.name}")
    return data


def encoded(tokenizer: Tokenizer, values: list[str]) -> list[dict]:
    return [
        {"id": str(index), "text": text, "reference_ids": tokenizer.encode(text).ids}
        for index, text in enumerate(values)
    ]


def short_inputs() -> list[str]:
    parts = [
        "差旅",
        "北京",
        "😀",
        "👩\u200d💻",
        "\u0301",
        "Å",
        "Ａ",
        "①",
        "½",
        "a",
        "B",
        "5",
        "_",
        "-",
        ".",
        "/",
        "<",
        ">",
        " ",
        "  ",
        "\t",
        "\n",
        "\r",
        "\r\n",
        "\u00a0",
        "\u1680",
        "\u2000",
        "\u200b",
        "\u200d",
        "\u2028",
        "\u2029",
        "\u202f",
        "\u2060",
        "\u3000",
        "\ufeff",
        "<s>",
        "</s>",
        "<pad>",
        "<unk>",
        "<mask>",
    ]
    values = (
        [""] + parts + ["".join(pair) for pair in itertools.product(parts, repeat=2)]
    )
    rng = random.Random(20261029)
    values.extend("".join(rng.choices(parts, k=3)) for _ in range(4000))
    return values


def wide_inputs() -> list[str]:
    parts = [
        "差旅报销",
        "员工制度",
        "北京",
        "供应商合同",
        "第三条",
        "2026-10-28",
        "¥1,200.50",
        "a",
        " Z ",
        "\t",
        "\r\n",
        "\u00a0",
        "\u200b",
        "\u200d",
        "\u202f",
        "\ufeff",
        "<s>",
        "</s>",
        "<pad>",
        "<unk>",
        "<mask>",
        "<mask",
        "s>",
        "👩\u200d💻",
        "👨‍👩‍👧‍👦",
        "🇨🇳",
        "🏳️‍🌈",
        "👍🏽",
        "\u0301",
        "\u0300",
        "\u0308",
        "Å",
        "Ａ",
        "①",
        "½",
        "™",
        "क्",
        "ष",
        "हिंदी",
        "ภาษาไทย",
        "العربية",
        "עברית",
        "각",
        "한글",
        "한국어",
        "にほんご",
        "\u2028",
        "\u2029",
        "\u2060",
    ]
    rng = random.Random(20261030)

    def scalar() -> str:
        while True:
            value = rng.randrange(0x110000)
            if not 0xD800 <= value <= 0xDFFF:
                return chr(value)

    values = []
    for index in range(30000):
        mode = index % 3
        if mode == 0:
            text = "".join(rng.choice(parts) for _ in range(rng.randrange(0, 40)))
        elif mode == 1:
            text = "".join(scalar() for _ in range(rng.randrange(0, 40)))
        else:
            text = "".join(
                rng.choice(parts) if rng.randrange(3) else scalar()
                for _ in range(rng.randrange(0, 40))
            )
        values.append(text)
    return values


def grapheme_inputs(data: str) -> list[str]:
    values = []
    for line in data.splitlines():
        content = line.split("#", 1)[0].strip()
        if not content:
            continue
        text = "".join(
            chr(int(part, 16)) for part in content.split() if part not in {"÷", "×"}
        )
        values.extend([text, "Å" + text, "a" + text, text + "\u0301", "<mask>" + text])
    return values


def special_inputs() -> list[str]:
    brackets = [("<", ">"), ("＜", "＞"), ("﹤", "﹥"), ("<", "＞"), ("＜", ">")]
    names = ["s", "/s", "pad", "unk", "mask", "ｓ", "ｐａｄ", "ｕｎｋ", "ｍａｓｋ"]
    values = [left + name + right for left, right in brackets for name in names]
    values += ["x" + value + "y" for value in values]
    values += ["Ａ" * 1000, "a" * 65536, "报销" * 10000, "<mask>" * 10000]
    return values


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("tokenizer", type=Path)
    parser.add_argument("grapheme_tests", type=Path)
    parser.add_argument("random_reference", type=Path)
    parser.add_argument("base_reference", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    if version("tokenizers") != "0.22.1":
        raise SystemExit("tokenizers 0.22.1 is required")
    verified(args.tokenizer, TOKENIZER_SHA256)
    graphemes = verified(args.grapheme_tests, GRAPHEME_SHA256).decode("utf-8")
    random_cases = json.loads(verified(args.random_reference, RANDOM_SHA256))
    base_cases = json.loads(verified(args.base_reference, BASE_SHA256))
    tokenizer = Tokenizer.from_file(str(args.tokenizer))
    cases = encoded(tokenizer, short_inputs())
    cases.extend(encoded(tokenizer, wide_inputs()))
    cases.extend(encoded(tokenizer, grapheme_inputs(graphemes)))
    for code in range(0x10000):
        if 0xD800 <= code <= 0xDFFF:
            continue
        for prefix in ("", "A"):
            text = prefix + chr(code) + "<mask>"
            cases.append(
                {
                    "id": f"{code:04x}-{prefix}",
                    "text": text,
                    "reference_ids": tokenizer.encode(text).ids,
                }
            )
    cases.extend(encoded(tokenizer, special_inputs()))
    cases.extend(random_cases)
    cases.extend(base_cases)
    output = json.dumps(cases, ensure_ascii=False, separators=(",", ":")).encode(
        "utf-8"
    )
    args.output.write_bytes(output)
    print(f"samples={len(cases)} sha256={hashlib.sha256(output).hexdigest()}")


if __name__ == "__main__":
    main()
