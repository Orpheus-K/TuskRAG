# 重建生命周期清理

2026-11-06 工作树；R16/R05/R13/R20、T13、M1。

RebuildService 新增 Cancel、Fail、Expire。取消在队列事务中复核当前 owner/admin、绑定与审计；重试保留 pending，终态失败及耗尽租约释放匹配目标，源空间及修订不变。队列新增 CheckExpiration，限制清理来自同一 Store 的耗尽任务事务。具体不变量见 [04](../04-数据模型与存储.md)。

验证：

- sqlc generate、go test ./internal/kb ./internal/jobs、go vet、相关 golangci-lint（0 issues）、CGO_ENABLED=0 Linux 核心构建通过。本机无数据库变量，数据库证据以下方独立运行结果为准。
- 隔离 PG 17/项目镜像，完整迁移 25；Linux race 编译后运行 kb.test -test.run '^TestRebuild' -test.v -test.timeout 3m，6 项通过。新增生命周期链覆盖 viewer 取消回滚、运行中取消、旧租约失败拒绝、后续重建预留不受影响、可重试失败保留、永久失败释放、检查点冒充过期清理拒绝、耗尽租约转 dead/lease_expired。另以审计触发器故障验证取消、pending 清理原子回滚。
- jobs.test -test.v -test.timeout 3m：16 项通过，包含进程强杀恢复、并发耗尽恢复、租约过期回滚、迟到完成、通知取消和 worker 分发；2 个子进程入口在父测试进程跳过，由对应进程测试调用。
- 清理前 embedding_rebuild、pending 指针、job、tenant 数量全部为 0；专用 tuskrag-lifecycle-pg-20261106 容器及匿名卷已删除。

未改变模型调用、解析分块或检索评分，未运行质量评测。HTTP/UI、生产 reembed 分发、后台嵌入、完整覆盖核验与原子切换仍待实现；当前领域生命周期通过不代表 T13 完成。
