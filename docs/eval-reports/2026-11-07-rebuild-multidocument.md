# 多文档重建与检索一致性

2026-11-07 工作树；R16/R05/R13、T13、M1。

新增 `TestRebuildMultiDocumentWorker`，每个场景使用独立 PostgreSQL 数据库、迁移 25、真实 BM25/HNSW、真实 jobs.Worker 和生产 RebuildEmbedding handler、HTTP 模型替身及 BGE-M3 计数器。源库包含 65 份文档、66 个块，跨越 ListRebuildDocuments 的 64 份分页边界；另建异租户知识库及命中相同词的证据作为隔离哨兵。

模型调用通过通道阻塞时，确认活动绑定及检索结果仍为旧空间。解除阻塞后检查终态、pending 清理、源向量保留和完整候选列表，包括证据内容、文档/版本/节/块编号、排名、词法分数、向量距离与融合分数。每场景对比 65 个查询，另验证双向跨租户绑定拒绝；成功后旧绑定请求必须报配置变化。

| 场景 | 终态 | 目标向量 | 原向量 | 绑定 | 65 组检索对照 |
|---|---|---:|---:|---|---|
| 成功 | succeeded | 66 | 66 | 目标空间、修订 +1 | 完全一致 |
| 最后一份文档用量错误 | failed | 65 | 66 | 保持源空间与修订 | 完全一致 |
| 首次正文调用期间取消 | cancelled | 0 | 66 | 保持源空间与修订 | 完全一致 |

三场景 pending 均清空，越权证据均为 0。取消后先等待 worker 退出再读取向量数，避免以过早读取掩盖迟到写入。

验证命令与证据：

- Linux `go test -race -c ... ./internal/ingest` 编译测试二进制；隔离 PG 环境执行 `-test.run '^Test(RebuildMultiDocumentWorker|RebuildEmbeddingRetryAndReplay|EmbeddingStageRetriesOnlyMissingVectors)$' -test.v -test.timeout 6m`，三项顶层测试全部通过；多文档三个子场景总耗时 88.39 秒。见 [race 原始日志](assets/rebuild-multidocument-20261107/race.log)。
- `go vet ./internal/ingest` 与该包 golangci-lint 通过，0 issues；最终 diff 空白检查通过。
- 现有 CI 的 `go test -race -p 1 ./...` 会纳入新测试；本轮未触发远端 CI。
- SSH 外层命令的退出码转发曾因 PowerShell 转义错误返回非零；测试日志独立记录最终 PASS，随后用单引号脚本核验末行 PASS 并成功执行清理。该外层错误没有重启测试或改变断言。
- 清理核验 rebuild/pending/vector/job/tenant/专用子数据库计数全部为 0；专用 `tuskrag-rebuild-multi-pg-20261107` 容器与匿名卷已删除。

样例修正：首次使用 66 个相同向量，取消场景在等距 Top-50 截断处出现候选差异。改为每块不同距离的固定向量，源/目标均经相同 halfvec 规范化；保留完整结果 DeepEqual，未改生产检索 SQL，也未放宽比较。共享模型替身仅增加可选固定向量表，原有默认行为由两项重试回归覆盖。多查询测试采用独立两分钟上下文，避免沿用单文档 40 秒限时；本测试不承担延迟门禁。

范围：这是多文档重建一致性和故障隔离证据，固定向量不证明真实模型语义质量。当前文档由测试夹具准备，未验证上传/解析全链路，未运行完整 core API+worker 子进程、浏览器与真实模型联调；T13/M1 整体退出仍须核对这些证据及既有门槛。下一小点为生产入口的创建→worker→查询/取消闭环。
