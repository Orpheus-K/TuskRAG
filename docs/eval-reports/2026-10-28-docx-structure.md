# DOCX 结构对照（2026-10-28）

- 数据：仓库内合成集 `services/parser/tests/fixtures/docx/golden.json`，版本 `docx-structure-v1`；3 份 DOCX 覆盖中文标题、继承标题样式、段落、超链接文字、表格顺序、横向及纵向合并单元格、换行与制表符。另有合成加密样例；均不含客户数据。
- 样例 SHA-256：`policy.docx` `af4b9d42eec68887003ce0755e8cc11fe13e5a35a35bebc8278d8283ccab521d`；`merged.docx` `868266207df88c3f6eb85dd90faa8d356f433d3895d7ffbb985cdb977beefad4`；`styles.docx` `fe5036bcfc9c3cbeb198837346ba9bc1923f6d3983bf3fdaeece01ff1724623b`。
- 基线：工作区没有 Git 提交；按此前检查过的 0.2.0 CLI 入口在临时目录复原，使用同一 MIME 分别运行 3 个样例，均返回 `unsupported_format`（退出码 10），结构匹配 0/3。
- 对照：Python 3.13、解析工具 0.3.0、python-docx 1.2.0、默认限额；`uv run --locked pytest -q` 的黄金样例中 3/3 按预期块类型、顺序、标题层级和表格单元格精确匹配，输出通过共享 JSON Schema。该合成集不能代表真实 DOCX 的整体质量。
- 安全回归：覆盖 ZIP 压缩比与解压大小、条目与 XML 节点限额、路径穿越、DTD/外部实体、ZIP 加密标记、实际加密 OOXML、畸形容器与不支持内容；不支持的列表、图片、页眉、修订、隐藏文字与嵌套表格显式拒绝。Linux bubblewrap 中 Go 调用真实解析器验证 DOCX、加密退出码及入库检查点。
- 未运行：真实知识库、检索基线、Recall@20、Hit@5、MRR、越权证据及 E5。当前入库、索引和检索链尚未完成，不能判定 T01、V07 或产品验收通过。
