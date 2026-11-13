# 重建覆盖核验与原子切换

2026-11-07 工作树；R16/R05/R13、T13、M1。

RebuildEmbedding.Completion 返回供 jobs.Complete 使用的数据库效果。kb.CompleteEffects 检查匹配的完成事务，锁知识库与创建者成员并复核源/目标快照，再由 ingest 核验索引及全部当前证据覆盖；CAS 切换目标、递增 config_revision、清空 pending、完成审计，与任务 succeeded 一起提交。索引仍由迁移角色 Provision 创建，应用不执行 DDL。规则见 [04](../04-数据模型与存储.md)。

覆盖核验拒绝缺向量、非 ready/受限当前文档、无块当前文档、错误标签、非当前/禁用块、错误维度或归一化，以及当前文档集合外的当前块/目标向量。查询只投影标识和状态。完成持有知识库锁后不再取得文档锁，发布/删除在改变当前证据前取得同一知识库锁；等待锁后用当前快照重查覆盖。

验证：

- sqlc generate、go test/go vet ./internal/kb ./internal/ingest、相关 golangci-lint（0 issues）、CGO_ENABLED=0 Linux 核心构建通过。本机无数据库变量时集成项跳过，真实证据如下。
- 隔离 PG 17/项目镜像，完整迁移 25；真实 BM25/HNSW 索引、HTTP 模型替身、BGE-M3 计数器，Linux race。
- 新增 TestRebuildCompletionAtomicSwitch 通过：拒绝检查点冒充完成、缺索引、缺向量；确认 pg_blocking_pids 中完成事务实际等待知识库锁后，提交新增当前块，完成醒来重新核验并拒绝切换；审计触发器故障使绑定/pending/修订/job 全部回滚；成功后新绑定、修订 +1、pending NULL、job succeeded/finished_at 同时成立；迟到完成拒绝。
- 切换前后同一合成查询真实混合检索，两个候选的内容、标识、排序、RRF/词法分数和距离完整相同；新读绑定使用目标与递增修订，旧绑定检索报 ErrBindingChanged。仅为切换一致性样例，不代替完整质量评测。
- 首轮对照将基线取在故障注入前，临时块插入/删除改变了 BM25 统计，原始词法分数不同。将基线采集移至注入清理后、成功切换前，保持两次语料一致，保留完整结果严格相等断言，重跑通过。生产逻辑未因此修改。
- 同轮其余 5 项重建批次/模型 race 回归通过；修正测试后单独重跑完成测试通过，无需重复未变的模型回归。
- 清理检查：重建、目标向量、任务和租户均为 0，publication 专用子数据库为 0。

生产 worker 分发、失败回调接入、HTTP/UI 及完整多文档评测仍待完成；本轮未宣布 T13/M1 完成。

专用 tuskrag-rebuild-switch-pg-20261107 容器及匿名卷已删除。
