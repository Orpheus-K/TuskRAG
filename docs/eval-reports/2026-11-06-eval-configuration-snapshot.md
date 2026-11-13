# 固定题集运行配置绑定

2026-11-06 工作树；R01/R11/R16/R20、T12/T13 配置边界、M1。后台任务接入的前置执行能力，本轮不交付任务创建或 worker。

## 实现

`retrieval.Service.Snapshot` 在现有租户、成员与知识库锁下读取嵌入绑定和检索配置修订。`Request.ExpectedConfiguration` 在同一检索入口读取当前配置后核对全部绑定字段；失配返回 `ErrConfigurationChanged`，不进入分词或模型调用。正常请求不携带该约束时保留现有配置继承行为。

`RunConfig.configuration` 可选，逐题传递并参与配置 SHA-256。`eval run-set` 在未提供该字段时自动捕获；显式旧快照不被更新为当前值。固定题集开始/结束权限检查与失败清空行为保留。合成集原 `eval run` 省略字段时序列化摘要不变。

快照包含空间 ID、入库配置修订、维度、距离、归一化与检索修订。保存的检索参数由不可变修订标识，显式重排参数仍在 RunConfig 中；密钥不进入快照。快照不冻结文档版本、提供方后台模型权重或提供方可变连接配置。已开始的单次请求仍使用其读取的配置；后续请求若配置更新则失败，不持有跨模型调用的事务。

## 验证

- 本机 `go test ./internal/eval ./internal/retrieval ./cmd/tuskrag ./internal/ingest` 通过（数据库集成场景另行运行）。
- Linux `go test -race -count=1 ./internal/eval ./internal/retrieval ./cmd/tuskrag` 通过；相关 vet、golangci-lint 通过（0 issues）。
- 领域测试证明配置快照传到每题请求，修订变化改变报告配置摘要；原有失败清空及修订混用检查保留。

- 真实 PG、Linux race `TestHybridRetrievalFromPublishedVersion` 通过（26.04s）：未配置快照、正常快照、旧检索修订及五个嵌入绑定字段失配；六类失配均在分词计数器调用前拒绝。配置在单次查询中更新时该查询仍使用旧修订，下一次带旧快照的请求拒绝；不带快照的新请求读取新配置。原权限、检索及模型异常回归一并通过。
- 同环境 `TestSyntheticZHEvaluation` 通过（46.27s）：64 题 × 四模式 × 三路径共 768 次查询，固定题集使用新快照约束；三路径逐题证据、指标一致，越权 0。
- [12 份报告](2026-11-06-snapshot-run/)与[上轮](2026-11-06-revision-run/)逐份 summary 相等，四项检索指标差值均为 0，全部通过现有 CLI validate。
- 独立容器 `tuskrag-snapshot-run-pg-20261106` 回环端口 56446；两次测试自行创建、迁移并删除随机库。结束时随机 publication 库数为 0，专用容器及匿名卷已删除。模型仅使用替身。

未修改数据库结构、HTTP 契约或页面，未运行浏览器。CLI 已编译和单测；本轮成功模型链路由真实服务与 RunSet 集成测试验证，不宣称已用 CLI 子进程完成整轮模型评测。
