# 重建客户端调用边界

2026-11-07 工作树；R16/R05、T13、M1。

api-client 新增 createRebuild/getRebuild/cancelRebuild，直接使用生成的 Rebuild/RebuildRequest 类型。写请求附带 CSRF，创建附带调用方保留的幂等键；GET 传递 AbortSignal，全部沿用 checked/ApiError。创建正文只发送目标和修订，响应校验任务/目标绑定、正整数、六态、尝试数、nullable 字段与终态时间。

核对 queries/jobs.sql 后确认 ClaimJob/CompleteJob 不清除 last_error，因此成功重试仍可带历史 failure_code；客户端明确接受该状态，不能将非空失败码等同当前任务失败。页面后续须以 status 决定终态和操作。

验证：pnpm test:web apps/web/src/RebuildClient.test.tsx 22 项通过；覆盖稳定键重试、CSRF/请求地址、无效输入零网络调用、响应绑定不符、字段缺失/状态不自洽、六态与取消确认、成功重试保留历史失败码。pnpm typecheck、相关 Biome 检查及 git diff --check 通过。

本轮未改页面、数据库或模型路径，未运行浏览器视觉测试、PG 或质量评测。控制台创建表单、状态轮询、取消及撤权隐藏仍待接入；本报告不表示页面完成。
