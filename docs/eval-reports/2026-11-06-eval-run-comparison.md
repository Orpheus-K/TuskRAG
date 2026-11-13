# 服务端评测运行对比接口

2026-11-06 工作树；R11/R13/R20、T08/T09/T12、M1。

交付 RunStore.Compare 与 GET `/api/v1/kbs/{kb}/eval-run-comparison`，以运行 ID 选择基线与当前结果，返回 executed 来源和实际报告摘要。复用既有 CompareReports：题号对齐、当前减基线、超过两百分点回退门禁和逐题分页。协议集中在 [13](../13-接口契约概要.md)。

同一事务加载两份不可变运行与报告，合并完整允许范围和实际证据，统一按顺序加锁后分别复核固定题集修订及结果绑定。当前页之外的证据同样检查；即使结果未生成，也必须检查两侧当前权限后才能返回 result_unavailable。归档可读；历史配置变化不要求重新执行；来源与 imported 报告分离。

## 验证

- 本机 eval/httpapi/kb/CLI 单测通过（本机数据库集成项未配置连接则跳过）。Linux eval/httpapi/CLI `go test -race -count=1` 通过。
- 隔离 PG 17、迁移 24，Linux race 程序执行 `-test.run '^TestEval(Start|Run|Worker)' -test.v -test.timeout 3m`：21 项顶层测试全部通过。
- 新增 3 项真实 PG 测试：worker 实际生成两份不同配置报告，验证 -1/+1 差值方向、回退门禁、题号分页、自比较、超界页、不同模式不可比、归档和跨租户；隐藏基线实际证据但保留题集目标见证，空的输出页仍拒绝汇总泄露；无结果与已完成结果在等待锁期间撤权均返回不可见。18 项既有运行/列表/创建/worker 回归通过。
- HTTP 覆盖必需正运行 ID、页大小、executed/no-store、GET 无 CSRF、不可比/无结果 409、不可见 404、角色 403、存储损坏 500。相关 go vet、golangci-lint、pnpm typecheck 和 git diff --check 通过；OpenAPI/客户端生成已同步。
- 专用容器 tuskrag-eval-compare-pg-20261106 使用回环端口 56446；结束时结果/运行/job/tenant 行数均为 0，容器和匿名卷已删除。

未修改检索、模型调用、评分或比较计算，未重跑模型质量评测；本轮无页面改动，未运行浏览器。控制台选择两次运行并展示比较仍待接入；完整 T12/M1 未完成。
