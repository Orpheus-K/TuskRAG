# Markdown 规整与节树对照（2026-10-28）

- 数据：`services/core/internal/ingest/testdata/assemble-golden.json` 及对应的 3 份 Markdown、1 份 TXT 合成样例；黄金文件 SHA-256 为 `5224ae39e4b3755ba1257f570c501b0d048ce50ccec5151bea388fd42cbd06f8`。样例哈希见 [Markdown/TXT 解析报告](2026-10-28-native-text-structure.md)，不含客户数据。
- 基线：此前没有 Markdown 规整及节树实现，4 份样例均不能得到可用节树。
- 对照：`go test -count=1 -run TestAssembleGoldenRoundTrip ./internal/ingest` 中 4/4 样例的父节、标题路径、顺序和块数符合黄金结果；生成的 Markdown 再解析后与原始块及表格单元格一致。真实 PostgreSQL 与 Linux 沙箱中的 CSV、DOCX、XLSX 合成样例也分别通过解析、规整和块内容往返对照。跨页节范围另有测试。
- 边界：覆盖不可信文字的 Markdown 转义、表格内换行与竖线、列表层级和段落边界、代码围栏、畸形数据、限额与取消。Linux 无网络命名空间内 20 秒段落往返模糊测试执行 36,705 次输入，无失败。当前拒绝不规则表格、outline、图片和七级以上标题；代码原文保留在结构化块中，围栏显示可补末尾换行。
- 未运行：真实知识库、分块、索引、检索对照和权限证据。规整器尚未接入 worker 或持久化；不能判定 R04、T01 或 E5 通过。
