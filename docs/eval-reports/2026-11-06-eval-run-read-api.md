# 评测运行状态与结果查询

2026-11-06 工作树；R11/R13/R20、T08/T09/T12、M1。

## 交付

已接入两个知识库范围 GET：`/api/v1/kbs/{kb}/eval-runs/{run}` 和 `/result`。身份取自当前会话，领域层要求 owner/admin。状态不返回完整 RunConfig 或题目，明确区分六类 job 状态、实际尝试次数、最后失败码、完成时间与结果是否可用。服务器执行结果来源固定 executed，不改变 imported 报告接口。

状态查询在同一事务复核题集目标与允许范围；已成功运行还复核完整返回证据。任务状态与结果存在性必须自洽。结果缺失新增领域错误 ErrRunResultUnavailable，对应 409 result_unavailable；返回该状态前也必须验证当前权限和目标，不能泄露不可见运行的“结果尚未生成”信息。归档允许读取；撤权或证据失效整份拒绝。契约细节集中在 [13](../13-接口契约概要.md)。

创建 HTTP 接口和控制台运行仍未交付；后续创建必须由服务端构造主体、模型标识与允许范围，不能直接暴露内部 RunConfig 入队方法。

## 验证

- 本机 eval/httpapi/kb/CLI 单测通过；HTTP 覆盖会话、正整数路径、依赖缺失、200/403/404/409/500、错误内容隐藏、no-store、nullable 字段、GET 不需 CSRF 及 executed 来源。
- 首次测试中的 internal_error 断言与仓库实际 internal 错误码不符，已按现有错误协议修正；Windows 上传回归最初因默认 TEMP 不可写失败，显式使用已有 Go 临时目录后通过，未修改上传实现。
- Linux eval/httpapi/CLI race 通过；相关 vet、golangci-lint 通过。sqlc、OpenAPI 及 TypeScript 客户端生成完成，pnpm typecheck 通过；已核对失败码与完成时间的 nullable 契约。

- 真实 PG 17、Linux race `^TestEval(Run|Worker)` 共 11 项顶层测试通过。新增 3 项覆盖 queued→running→succeeded、failed/dead/cancelled、attempts/nullable/结果可用性、归档读取、完成报告返回块失效（保留另一个目标见证）、状态及无结果查询等待期间撤权；既有 8 项运行/worker 回归通过。
- 专用容器 `tuskrag-run-read-pg-20261106`，回环端口 56446，沿用迁移 24；结束时结果/运行/job/tenant 行数均为 0，容器与匿名卷已删除。`git diff --check` 通过。

无召回、分块、评分或模型调用改动，未重跑模型评测；无页面改动，未运行浏览器。未把领域/传输分层测试描述为真实浏览器验收。
