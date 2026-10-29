# XLSX 结构对照（2026-10-28）

- 数据：仓库内合成集 `services/parser/tests/fixtures/xlsx/golden.json`，3 份工作簿覆盖多工作表、空工作表、行列位置、横向和纵向合并单元格、中文、日期、时间、布尔值及普通数值；不含客户数据。
- SHA-256：`travel.xlsx` `b87e1ddfa9a07ecce28bd702df00c221b3f1436eb27dae939de3a65919f92653`；`types.xlsx` `b44021108710ff683214e4d5bfd0522136b25bd5835b8bc51467a3e4639e51a6`；`text.xlsx` `73dc5d44d6e78af70c9b02392cb36aeae68936a5eb1c1fd8c427365e1829f66d`；`golden.json` `a3178a3b66a8577ffca6af6bd836b9fe89a84e975d1828f428857941bc9dc476`。
- 基线：Python 3.13 调用原 0.3.0 CLI，3 个 XLSX 均返回 `unsupported_format`（退出码 10），结构匹配 0/3。
- 对照：Python 3.13、解析工具 0.4.0、python-calamine 0.9.0 和 openpyxl 3.1.5，默认限额；`uv run --locked pytest -q tests/test_xlsx.py` 中 3/3 黄金样例与预期块及单元格精确匹配，输出通过共享 JSON Schema。合成集不能代表真实 XLSX 的整体质量。
- 边界：公式、隐藏内容、批注、图形、错误单元格和自定义数字格式显式拒绝；覆盖解压、XML、大小与坐标限额、外部实体、加密、重叠合并区域和读取器不一致。Go 解析阶段通过 Linux bubblewrap 与真实 PostgreSQL 验证 XLSX 及合并单元格。
- 未运行：真实知识库、检索基线、Recall@20、Hit@5、MRR、越权证据及 E5。当前入库、索引和检索链尚未完成，不能判定 T01、V07 或产品验收通过。
