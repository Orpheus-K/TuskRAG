# 重建 worker 分发与终态清理

2026-11-07 工作树；R16/R05/R13、T13、M1。

生产 worker 的认领集合新增 reembed，分发至 RebuildEmbedding.Prepare/Expire。Prepare 执行模型批次并返回完成效果；执行和完成阶段的已知错误映射为 ProcessingFailure，失败回调由 kb.FailureEffects 在相同租约/计划状态的失败事务中验证并处理 pending。可重试保持 pending，永久失败及耗尽释放；未知错误仍显式返回，不伪装成功。新增 rebuild_incomplete 可重试失败码，处理当前覆盖变化，仍受三次尝试上限约束。规则维护在 [04](../04-数据模型与存储.md)，启动方式见 [09](../09-部署运维与可观测.md)。

验证：

- go test、go vet 与相关 golangci-lint（0 issues）覆盖 internal/ingest、internal/kb、internal/jobs、cmd/tuskrag；CGO_ENABLED=0 Linux 核心构建通过。本机数据库集成项无环境变量时跳过。
- 独立 PG 17/项目镜像迁移 25、真实 BM25/HNSW、HTTP 模型替身、BGE-M3 计数器，Linux race TestRebuildWorkerLifecycle 五个子场景全部通过：成功认领并原子切换；索引缺失三次后 dead/index_unavailable；用量错误 failed/tokenizer_mismatch；撤权 failed/permission_revoked；耗尽租约 dead/lease_expired。全部终态 pending 为空，仅成功切换活动空间。撤权和耗尽路径模型请求数为 0。
- kb -test.run '^TestRebuild' 8 项回归通过；jobs 全包 16 项通过，2 个子进程入口在父进程跳过，由对应进程测试执行。
- 本轮运行真实 jobs.Worker 与生产使用的同一重建 handler；未额外启动依赖解析沙箱的完整 core 子进程。生产 bootstrap/分发完成编译、静态及构建验证。
- 清理检查：重建记录、pending、chunk_vector、job、tenant 均为 0，publication 专用子数据库为 0。

未改变模型批次、分块或检索算法，复用上轮切换样例对照。本轮未重跑完整质量评测；HTTP/UI、真实多文档切换评测和完整 T13/M1 验收仍待完成。

专用 tuskrag-rebuild-worker-pg-20261107 容器及匿名卷已删除。
