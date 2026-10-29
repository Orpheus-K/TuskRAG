# 数字 PDF 结构对照（2026-10-28）

- 数据：5 份仓库内合成文件，不含客户数据。`policy.pdf` 有两页、一级和二级书签、3 行 2 列表格；`scanned.pdf` 为整页图片；`columns.pdf` 和 `columns-staggered.pdf` 分别为同基线及错位双栏；`encrypted.pdf` 为加密文件。测试字体及许可见 `services/parser/tests/fixtures/pdf/NotoSansSC-OFL.txt`。
- SHA-256：`policy.pdf` `a7ebac70d843c233c5279737eb3cd5945320ae6305030a18962a9a327a6af3dd`；`scanned.pdf` `1ba6b3c589ffbc69408334855a5266e944e574ba2af6fee0cf7e49335807b1ec`；`columns.pdf` `74e7d8623e5a4c46cbd20c54f8037e290e6e5866dccb39ac0cefd5fc30ad2417`；`columns-staggered.pdf` `0384d92374ecb65117e00639a319531fad097e7f3380451b714da5d8b0a26b34`；`encrypted.pdf` `c925699ad808dae39889cb8d21e6a9c608aa3b7cd0629085bee666d64b1a809b`。
- 基线：保留的解析工具 0.3.0 对数字和扫描 PDF 均返回 `unsupported_format`，可用结构 0/1。实现过程中的初版 0.5.0 错误接受错位双栏并交错输出两栏文字；加入版面检查后该样例返回 `unsupported_format`，不输出解析结果。
- 对照：Python 3.13、解析工具 0.5.0、pdfplumber 0.11.10、pypdfium2 5.14.0；数字 PDF 的 2 页、5 个预期块、书签层级、6 个表格单元格和页内坐标精确匹配，并通过共享 JSON Schema。两类双栏显式拒绝，加密返回 `encrypted`，截断和超限输入分别失败；扫描页报告 0 字符与 100% 图像覆盖。`uv run --locked pytest -q tests/test_pdf.py tests/test_cli.py` 为 20 项通过。
- 入库与检索：隔离 PostgreSQL 和本地模型替身下，Linux 实际 `worker`/`all` 进程通过受限子进程处理数字 PDF；未确认表头返回 `table_header_required`，扫描 PDF 返回 `quality_review`，确认表头的数字 PDF 生成表格块和向量。实际 HTTP 上传后查询返回所上传版本的表格内容，物理页码为 `[1,1]`。该测试未使用真实模型或客户文档。
- 边界：标题识别、字符校验和双栏检测均为受限算法；测试不能证明所有 PDF 的阅读顺序或表格识别正确。多栏启发式无法识别所有版面；跨页表格合并、OCR、VLM、页图渲染及页级报告尚未实现。
- 未运行：代表性真实 PDF 数据集、真实模型、Recall@20、Hit@5、MRR 及 E5。当前不能判定完整 T01/T02/T03、V07 或产品验收通过。
