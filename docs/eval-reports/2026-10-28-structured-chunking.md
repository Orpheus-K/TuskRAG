# 列表与代码结构对照（2026-10-28）

关联 R04、A08。数据均为合成样例，不包含客户文档。

| 对照 | 基线 | 当前结果 |
|---|---|---|
| 嵌套有序列表往返 | 旧渲染省略必要空行，起始编号 100 的子列表在再次解析时被并入正文，结构保留 0/1 | 保留起始编号 100 和 2/2 子项，结构保留 1/1 |
| 四款制度列表，按 Unicode 码点计数 | 不拆分为 97，超过本次硬上限 45 | 5 块，最大 40，4/4 子项按序保留，每块重复所属条号 |
| 同一制度列表，按固定 BGE-M3 词表计数 | 不拆分为 55 token，超过本次硬上限 32 | 4 块，计数 `32, 25, 26, 17`，4/4 子项按序保留 |
| 代码块 | 无独立分块器 | 完整保留，检索输入 21 token；超限时显式失败 |

解析修复使 Markdown 的 `parser_profile` 从 `native-markdown-v2` 变为 `native-markdown-v3`。解析、规整、嵌套列表分块和代码分块已串联测试；真实隔离 PostgreSQL 中验证了上传、解析检查点、重放与列表分块。旧版解析检查点不因版本号变化自动重解析。

边界测试覆盖顶层与嵌套有序编号、条号重复、标题路径、页码、坐标复制、字面量转义、代码围栏、超限、取消、计数错误和不可支持结构的显式失败。Linux 无网络命名空间内最终版本的 10 秒模糊测试执行 7,473 次输入；修改前的 30 秒测试执行 47,110 次。受影响 Go 包、真实 PostgreSQL 测试及竞争检测通过。

复现：在 `services/core` 运行 `go test -count=1 -run 'TestSplitList|TestStructured|TestListGroup|TestParsedMarkdownStructuredChunks' -v ./internal/ingest`、`go test -count=1 -run TestNativeMarkdownListPreservesNestedOrderedStart -v ./internal/parser` 和 `go test -count=1 -run TestBGEM3CounterWithStructuredChunks -v ./tests/integration`。使用 `tokenizers==0.22.1`、固定词表运行 [参考计数脚本](../../tests/spikes/tokenizer-hf/structured-reference.py)可独立得到 BGE-M3 数值。对应测试文件 SHA-256：列表测试 `b3217a4217cdd34f7506c6a702c2dcb0020d270863d2cb536aa0d9c119adc5aa`；解析测试 `ff1b1f4efd7caf5ed5054de61bb4f7faba01a08fd588bbd294b798e7459a15a4`；tokenizer 集成测试 `dabdd503d8f91a99fd46852991f157d27bdfb544c517ddcb48b05c9f9d2baddd`。

当前只在超长顶层条款恰好包含一个直接子列表时按款拆分；不可再拆的单项、代码超限和其他超长结构显式失败。尚未接入完整父子分块、worker、索引和检索；Recall、MRR、越权证据数和 E3 均未运行，不能判定 R04 完成。
