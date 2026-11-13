# 重建批次持久化

2026-11-06 工作树；R16/R05/R13、T13、M1。

新增 ingest.RebuildVectors.Read/Write：按当前 ready 文档读取有界页，返回私有内容快照；写入复核任务、文档当前版本、目标配置、当前权限、块正文和标签。复用入库 halfvec 维度、归一化与可表示性校验。目标向量以 (tenant,chunk,space) upsert，与旧空间并存，不修改活动绑定。详见 [04](../04-数据模型与存储.md)。

kb.TargetTx 复用同一目标授权检查，要求 jobs.CheckCheckpoint 证明同一 Store/租约的检查点事务；调用方先锁文档，再锁知识库/成员，避免与发布/删除锁序反转。

验证：

- sqlc generate、go test / go vet ./internal/kb ./internal/jobs ./internal/ingest、相关 golangci-lint（0 issues）、CGO_ENABLED=0 Linux 核心构建通过。本机未设置数据库连接；以下为实际数据库结果。
- 独立 PG 17/项目镜像、迁移 25，Linux race：ingest.test -test.run '^TestRebuildVectors' 1 项通过；kb.test -test.run '^TestRebuild' 8 项通过（含目标范围及 HTTP 模型替身子场景）；jobs.test 全包 16 项通过，2 个子进程入口在父进程跳过、由对应进程测试调用。
- 新批次测试覆盖当前文档分页、两次同批 upsert 仍仅一条目标向量、旧空间条数保持、正文变化拒绝整批写入、错误维度拒绝、取消后的迟到写入拒绝，活动空间及源修订不变。
- 首轮行为断言通过但测试夹具清理因 document_current_version_fk 失败；修正夹具先解除当前版本引用，在全新隔离库重跑通过。未调整生产约束或放宽断言。
- 最终重建记录、pending、chunk_vector、job、tenant 数量均为 0。

本轮仅交付批次领域存储，未启用生产重建模型批次编排、完整覆盖核验或原子切换，也未提供 HTTP/UI。未改活动检索路径或分块策略，未运行模型质量评测；后续完整重建需补模型批次用量/分词校验、失败处理及切换前后质量对照。T13 尚未完成。

专用 tuskrag-rebuild-vectors-pg-20261106 容器及匿名卷已删除。
