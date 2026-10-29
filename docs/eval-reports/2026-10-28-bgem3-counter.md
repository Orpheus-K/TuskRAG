# BGE-M3 token 计数对照（2026-10-28）

关联 R04、A05/A08、S5。此次验证独立计数器，不代表文档已经可检索。

固定 [BAAI/bge-m3 tokenizer.json](https://huggingface.co/BAAI/bge-m3/blob/5617a9f61b028005a4858fdac845db406aefb181/tokenizer.json)，原始 SHA-256 `21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08`；参考为 Hugging Face `tokenizers` 0.22.1，包含首尾特殊 token。输入全部为合成数据。

| 对照集 | 有效输入 | token ID 完全一致 |
|---|---:|---:|
| 短 Unicode / 标记组合 | 5,641 | 5,641 |
| 随机多语种及 Unicode 标量 | 30,000 | 30,000 |
| Unicode 16 字素边界变体 | 5,465 | 5,465 |
| 基本多文种平面字符与 `<mask>` | 126,974 | 126,974 |
| 全角标记和长输入 | 94 | 94 |
| 原 5,000 条随机样例与 24 条小样例 | 5,024 | 5,024 |
| 合计 | 173,198 | 173,198 |

复合参考文件共 173,200 条，SHA-256 `02167fb603e5c5e2fd92f03081aaa60abfa97fe8a94b9bc8accfde728a9390e9`；两条含 NUL 输入按约束拒绝。固定黄金集另覆盖 224 条，含空格标记与全角特殊标记。旧 `goSentencePiece` 直接读取 JSON 的小样例结果为 7/24；单纯适配其规范化后，全角特殊标记仍有差异。参考 fast tokenizer 的字符映射使用 Unicode 16 字素边界和短前缀匹配，不能简单替换为 SentencePiece 的最长前缀规则。

独立分块器接入该计数器的合成样例：段落原文加标题为 45 token，硬上限 40，拆成 4 块，最大 25；表格原始检索输入为 100 token，上限 65，拆成 2 块，最大 64，保留 4/4 数据行。两类输出的计数分别为 `25, 24, 25, 24` 和 `64, 64`，与参考 tokenizer 相同。该对照不测召回质量。

模型客户端提供独立的服务端用量核对：逐条发送 13 个固定合成样例，比较本地计数和响应的 `usage.prompt_tokens`；最后一个样例为 794 token，接近默认 800 token 硬上限。替身服务按参考 tokenizer 的计数返回用量，13/13 一致；用量缺失、非法、不同、出域被拒或服务失败均显式报错，且请求大小在发送前检查。该验证只覆盖样例；对不能处理 794 token 的服务，降低上限与校准的流程尚未实现。实际服务尚未验证。

资源为一次本机 Windows amd64 测量：11th Gen Intel i5-1135G7；新实例加载约 0.73 s，GC 后堆增量约 18.1 MB，累计分配约 211 MB；基准样例平均约 75 μs/次、72,952 B/次。测量不是部署容量或 p95 结论。计数器限制输入 64 KiB、规范化输出 1 MiB、并发 1—16，并对无效 UTF-8、NUL、超限及取消显式失败。

复现：先以 `tokenizers==0.22.1` 运行 [参考集生成器](../../tests/spikes/tokenizer-hf/generate-reference.py)，输入固定词表、[Unicode 16 GraphemeBreakTest](https://www.unicode.org/Public/16.0.0/ucd/auxiliary/GraphemeBreakTest.txt)、[原随机样例生成器](../../tests/spikes/tokenizer-model/generate-reference.py)产物及 [小样例](../../tests/spikes/tokenizer/reference.json)；然后在 `services/core` 设置 `TUSKRAG_TOKENIZER_REFERENCE` 为生成文件的绝对路径，运行 `go test -count=1 -run TestBGEM3ExternalReferences -v ./internal/tokenizer`。Unicode 文件 SHA-256 为 `ee2b9354d270ac061b29f09662cafea06341d77e704b8cc6bd72aaeeda363cb5`。

尚未验证实际嵌入服务是否使用同一词表、特殊标记和用量统计；不得将该组件视为通用模型计数器。worker、索引、检索评测与 S5 的服务端一致性门禁均未完成。
