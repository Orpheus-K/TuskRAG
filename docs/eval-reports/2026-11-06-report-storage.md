# 知识库评测报告存取验收

2026-11-06 工作树；R11/R13/R15、A01/A11、M1。交付完整报告导入/读取服务、REST、迁移 21、sqlc/OpenAPI/TypeScript 生成契约。列表、页面、评测集及批量运行尚未实现。

## 行为

- 只接受通过完整性校验的成功报告。来源固定为 imported，不能冒充服务端执行结果；按规范化 JSON 摘要在租户/知识库内去重，相同内容重试不重复保存和审计。
- owner/admin 可读写，viewer/editor 拒绝；无成员和跨租户隐藏。查询摘要前检查成员权限，返回前在锁内复核，防止通过 403/404 差异探测报告存在性。
- 所有证据重新核对所属知识库、文档、节及页范围，以及当前版本、未删除、继承权限、未禁用、访问标签。任一失效均拒绝整份报告，不以删减后数据伪装完整结果。归档只读。
- 按文档→知识库→成员→证据的锁顺序，与发布/删除协调。应用角色无 RLS 绕过权，报告表强制租户 RLS，应用仅 SELECT/INSERT。保存和不含正文的摘要审计同事务提交。
- HTTP 输入上限、候选数及错误码见 [接口契约](../13-接口契约概要.md)。无答案指标保留 null；延迟分位数必须为数字二元组。Huma 不支持对结构引用使用 nullable 标签，传输层显式声明指标的对象/null 联合，不改 CLI JSON 格式。

## 实际证据

数据库：授权旧测试实例独立容器 tuskrag-reports-pg-20261106；tuskrag-eval:20261029 项目 PG 镜像，回环端口 56446，隔离库 tuskrag_test_reports。WSL Go 1.27.2 构建 Linux race 测试二进制，复制到实例执行；未使用生产库。

| 检查 | 结果 |
|---|---|
| `kb.test -test.run '^TestEvalReport' -test.v -test.count=1` | 4 项及子场景全部通过：并发去重/单次审计、应用角色不可更新、跨租户直接 SQL 零可见记录、owner/admin 和普通成员、无成员/异租户/异知识库、删除/受限/旧版本/禁用/标签变化、归档、伪造位置、审计注入回滚、真实锁等待后的撤权 |
| `db.test -test.v -test.count=1` | 全包通过，含版本 14 升到 21 保留数据、索引约束、租户隔离；独立空库也已迁移到 21 |
| Linux `go test -race -count=1 ./internal/eval ./internal/httpapi` | 通过；HTTP 最终变更后再次通过，覆盖会话/CSRF、摘要格式、未知字段、null 指标、null 延迟拒绝及领域错误映射 |
| 静态与构建 | 相关 eval/kb/db/httpapi/cmd 包 vet、golangci-lint（0 issues）通过；Linux CGO_ENABLED=0 核心构建完成 |
| 契约 | sqlc v1.31.1、Go OpenAPI、`pnpm generate:client`、`pnpm typecheck` 通过 |

收尾查询确认测试库 tenant 与 eval_report 残留记录均为 0；专用容器及匿名卷已移除，未启动或修改旧评测容器。

HTTP 入口使用领域替身验证传输行为；事务和权限由真实 PG 服务测试验证，未声称完整浏览器端到端验收。本次无页面变更，未运行浏览器测试。未改变解析、分块、检索、重排、评分或 Prompt，未重跑模型质量评测；完整报告校验和历史报告对照见[前置证据](2026-11-06-report-validation.md)。导入者提供的指标不能凭完整性检查证明真实相关性或模型质量。
