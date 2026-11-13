# 重建模型批次编排

2026-11-06 工作树；R16/R05/R13、T13、M1。

RebuildEmbedding.Run 遍历当前文档和块，使用目标配置打开单个 models 客户端，保留同一次运行的提供方/凭据运行时快照。先执行既有 13 个分词探测，再按目标计数器核验每批用量和 token 上限，调用 RebuildVectors 写入。批次 resolver 在发送正文前检查原始内容、文档当前版本、权限及租约，写入前再次复核；网络阶段不持有事务。源模型的存储 token_count 不冒充目标模型计数。详细规则见 [04](../04-数据模型与存储.md)。

Run 只填充目标空间，任务仍 running、旧空间仍 active；并发文档发布的最终覆盖判定留给完成事务。失败重跑会重新遍历并覆盖已写目标行，按主键保持幂等；未宣称仅补缺失向量。

验证：

- sqlc generate、go test ./internal/ingest、go vet、相关 golangci-lint（0 issues）、CGO_ENABLED=0 Linux 核心构建通过。本机数据库集成项因无连接变量跳过。
- 独立 PG 17/项目镜像迁移 25，HTTP 模型替身及真实 BGE-M3 本地计数器，Linux race 运行 -test.run '^TestRebuild' -test.v -test.timeout 3m，5 项全部通过。
- 新增四项：503 后可恢复错误保留、重跑及再次执行不增目标行/不切换/不完成任务；用量差异拒绝写入；模型请求在途时取消事务能够完成、迟到结果不写入；本地计数后文档删除时阻止正文请求。既有批次持久化回归也通过。
- 最终 embedding_rebuild、pending、chunk_vector、job、tenant 数量全部为 0。

未连接生产 worker 或 HTTP/UI，未实现索引/完整覆盖核验及原子切换。未修改活动检索、解析或分块路径；模型替身验证调用与存储正确性，不代表真实目标模型质量。完整 T13 仍需切换前后检索质量对照。

专用 tuskrag-rebuild-model-pg-20261106 容器及匿名卷已删除。
