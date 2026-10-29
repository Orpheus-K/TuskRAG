# 段落分块结构对照（2026-10-28）

- 数据：`services/core/internal/ingest/text_chunks_test.go` 中 `TestSplitTextGolden` 的 2 段合成差旅文本，5 个句子、跨 2 个逻辑页；测试文件 SHA-256 `05760dcbcbdf817a4c838971838f5fedb9dd9a57f2d0f3df51aa69d9649b2b61`，不含客户数据。测试计数器按 Unicode 码点计数，不代表嵌入模型的 token 数。
- 基线：不拆分的完整检索输入为 64 个测试单位，超过本次设置的 45 单位硬上限。
- 对照：`go test -count=1 -run TestSplitTextGolden -v ./internal/ingest` 生成 4 块，最大 34 个测试单位；5/5 原句顺序保留，另有 3 处单句重叠；标题路径、跨页范围和原始文字可还原，计数针对实际返回的检索输入。
- 边界：覆盖中文句末标点、换行、小数点、引号、跨页重叠、重叠不再适配时的退让、无断句点的超长文本、计数器错误和取消。Linux 无网络命名空间内 20 秒模糊测试执行 37,426 次输入，无失败；真实 PostgreSQL 中的 DOCX、Markdown、TXT 合成样例经解析与规整后使用测试计数器完成段落拆分，未调用模型。
- 未运行：实际模型的 token 一致性、分块检索对照、Recall@20、Hit@5、MRR、越权证据及 E3。生产计数器、长父节切分、列表/代码块分块、索引和 worker 尚未接入，不能判定 R04 或 T01 通过。
