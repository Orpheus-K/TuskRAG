# Markdown / TXT 结构对照（2026-10-28）

- 数据：`services/core/internal/parser/testdata/native-text/` 内 3 份 Markdown 和 1 份 TXT 合成样例；覆盖中文章/节、标题、段落、换行、链接文字、GFM 表格、嵌套列表、无语言标注的代码块、转义和字符引用，不含客户数据。
- SHA-256：`policy.md` `71c52ee098e1dd1d94a0f760f9978a21d80600ed90878f4e1a159544c47d2449`；`encoding.md` `7b958887d25c1ccda9d2ef9e51bfb54698df06aeefd32b2c5d5a590b120bf08c`；`script.md` `7e536a22b26279b1c605a4a400194209fb58b1df9bfad3e7c4e6708ac0b5a07e`；`policy.txt` `4cc2d92c41dd36605e37495ac644bae0e7548b9f3cc7621961a06dd0d7ad857f`；`golden.json` `f52c7837b17f1b54f359080a156b466bcc740058ab611f2f3fe72a63ef53a165`。
- 基线：此前 Go 解析入口将这两种 MIME 交给 Python 0.4.0 工具；分别调用工具处理 4 个样例，均返回 `unsupported_format`（退出码 10），结构匹配 0/4。
- 对照：Go 原生解析 `native-markdown-v2` / `native-txt-v1`、goldmark 1.8.6、默认限额；`go test -count=1 -run TestNativeTextGolden ./internal/parser` 的 4/4 样例按预期块类型、顺序、标题层级和单元格精确匹配，输出通过共享 JSON Schema。合成集不能代表真实文件的整体质量。
- 边界：图片、HTML、脚注、引用块、带语言标注的代码和会丢失单元格的表格显式拒绝；覆盖编码、输入/输出和结构限额、取消、嵌套列表及 CR 换行。列表中文字的字面标记和多段落边界有回归测试。Linux 无网络命名空间内 20 秒模糊测试执行 15,310 次输入，无崩溃；真实 PostgreSQL 验证上传领域服务、解析检查点、重放和失败状态，尚非完整入库。
- 未运行：真实知识库、检索基线、Recall@20、Hit@5、MRR、越权证据及 E5。当前入库、索引和检索链尚未完成，不能判定 T01、V07 或产品验收通过。
