# CSV 结构对照（2026-10-28）

- 数据：仓库合成集 `services/parser/tests/fixtures/csv/golden.json`，版本 `csv-structure-v1`，6 个 CSV，覆盖中文、引号、单元格内换行、空值、不规则列、UTF-8 BOM 与 CRLF；不含客户数据。
- 配置：Python 3.13，`text/csv`，默认解析限额；旧工具 0.1.0 与新工具 0.2.0，输出契约 `1.0.0`。工作区未初始化 Git，无法记录提交号。
- 基线：工具 0.1.0 对 6 个样例均返回 `unsupported_format`（退出码 10），无结果；表格精确匹配 0/6。
- 对照：`uv run --locked pytest -q` 的黄金样例中，6/6 以原始行列和文本精确匹配，均通过共享 JSON Schema；不规则列数被标记为可疑。该结果仅覆盖这些样例，不代表真实文档解析质量。
- 安全回归：测试另覆盖空文件、ZIP/PDF/XML 伪装、非法 UTF-8、NUL、未闭合引号和各项 CSV 限额，失败时无解析结果。Linux Go↔Python 测试在 bubblewrap 中验证 CSV 解析与契约。
- 未运行：检索基线、Recall@20、Hit@5、MRR、越权证据及 E5。当前尚无入库、检索和 VLM 路径，不能据此判定 T01、V07 或产品验收通过。
