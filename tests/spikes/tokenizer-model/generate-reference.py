import hashlib
import json
import random
import sys
from importlib.metadata import version
from pathlib import Path

from tokenizers import Tokenizer

if version("tokenizers") != "0.22.1":
    raise SystemExit("unexpected reference tokenizer version")

tokenizer_path = Path(sys.argv[1])
if hashlib.sha256(tokenizer_path.read_bytes()).hexdigest() != (
    "21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08"
):
    raise SystemExit("unexpected tokenizer checksum")
tokenizer = Tokenizer.from_file(str(tokenizer_path))
rng = random.Random(20261028)
fragments = [
    "差旅", "住宿", "审批", "北京", "报销", "😀", "👩\u200d💻", "\u0301", "Å", "Ａ", "①", "½",
    "a", "B", "5", "_", "-", ".", "/", "<", ">", " ", "  ", "\t", "\n", "\r", "\r\n",
    "\u00a0", "\u1680", "\u2000", "\u200b", "\u200d", "\u2028", "\u2029", "\u202f", "\u2060",
    "\u3000", "\ufeff", "<s>", "</s>", "<pad>", "<unk>", "<mask>", "x<mask>y",
]
cases = []
for index in range(5000):
    text = "".join(rng.choice(fragments) for _ in range(rng.randrange(0, 100)))
    cases.append({"id": str(index), "text": text, "reference_ids": tokenizer.encode(text, add_special_tokens=True).ids})
output = json.dumps(cases, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
Path(sys.argv[2]).write_bytes(output)
print(f"samples={len(cases)} seed=20261028 sha256={hashlib.sha256(output).hexdigest()}")
