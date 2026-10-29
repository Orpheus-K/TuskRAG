# BGE-M3 tokenizer asset

`bge-m3-tokenizer.json.gz` is a deterministic gzip of the tokenizer vocabulary from [BAAI/bge-m3 revision 5617a9f61b028005a4858fdac845db406aefb181](https://huggingface.co/BAAI/bge-m3/tree/5617a9f61b028005a4858fdac845db406aefb181). The model card declares MIT. The uncompressed file SHA-256 is `21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08`; it contains no model weights. The asset is checked before loading.

This profile reproduces the pinned Hugging Face fast tokenizer locally. A configured embedding service must still be checked for matching token accounting before the counter is used in ingestion.
