# Unicode 16 grapheme segmentation

`splitfunc.go` and `trie.go` are from [clipperhouse/uax29 v2.4.0](https://github.com/clipperhouse/uax29/tree/v2.4.0/graphemes), under the adjacent MIT license. The generated trie uses Unicode 16 data. `iterator.go` is the local bounded adapter.

The pinned reference tokenizer uses Unicode 16, while the application's dependency graph selects a newer `uax29/v2` release with Unicode 17. Keeping this small versioned subset prevents a dependency upgrade from silently changing token counts. Validate replacements against the fixed tokenizer reference and Unicode 16 grapheme cases before updating it.
