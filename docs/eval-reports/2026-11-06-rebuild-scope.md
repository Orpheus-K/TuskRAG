# 重建目标调用范围

2026-11-06 工作树；R16/R05/R13、T13、M1。

RebuildService.Target 返回目标 EmbeddingBinding 与绑定租约的专用 resolver。每次解析在 WithLease 短事务内锁任务、知识库与创建者成员记录，复核当前权限、活动状态、源空间/修订、pending 目标及目标模型参数快照，读取当前出域策略。仅范围校验；模型请求仍由 models.OpenEmbeddingClient 执行并使用既有运行时快照及出域审计。现有活动空间 resolver 不放宽。

验证：

- sqlc generate、go test ./internal/kb、go vet ./internal/kb、相关 golangci-lint（0 issues）、CGO_ENABLED=0 Linux 核心构建通过。本机数据库集成项未配置连接，以下为真实数据库证据。
- 独立 PG 17/项目镜像迁移至 25，Linux race 程序运行 -test.run '^TestRebuild|^TestResolveEmbeddingScope|^TestEmbedding' -test.v -test.timeout 3m：9 个顶层测试通过。包含 7 项重建测试、既有数据库出域客户端及活动范围回归。
- 新增 TargetScope 的 7 个子场景：模型变化、源修订变化、pending 失配、归档、角色降权、取消均拒绝并不返回范围；租户策略更新返回当前策略。各场景均先验证正常目标、错误 binding 拒绝及活动 resolver 拒绝 pending 空间。
- 清理前 embedding_rebuild、pending 指针、job、tenant 全为 0。

未改动已运行的解析、检索或模型评分，未运行质量评测。专用 resolver 尚未连接生产重建 worker；本轮未将其接入模型替身执行重嵌入。后续需接模型批次调用、当前块读取与向量持久化、完整覆盖检查及原子切换，再开放 API/UI。T13 尚未完成。

专用 tuskrag-scope-pg-20261106 容器及匿名卷已删除。

## 模型客户端调用边界补验

同日新增 TestRebuildClientRechecksBeforeSendingContent。独立 PG 17（迁移 25）与本地 HTTP 模型替身、Linux race：cancel/revoke/model/egress 四个子场景全部通过。每个场景首次正文请求校验目标 replacement 模型、请求路径与合成输入；状态变化后再次调用，同一个模型客户端必须返回对应错误且替身计数保持 1。出域策略不再满足租户上限时拒绝请求并产生一条 model.call deny 审计。调用方伪造 allow 策略无法绕过数据库策略。

本机 go test ./internal/kb、go vet、相关 golangci-lint 通过。此次只补调用边界验收，未新增生产批次执行路径，未运行检索质量评测。清理前重建记录、pending、任务、租户数量均为 0。

后续持久化必须走独立重建路径：既有 ingest.VectorStage.Write 仅接受未发布的 staged plan，不适用于当前版本块；重建应复用向量编码验证，并遵守发布/删除路径的文档→知识库锁顺序，避免拿知识库锁后再等待文档锁。

补验专用 tuskrag-rebuild-client-pg-20261106 容器及匿名卷已删除。
