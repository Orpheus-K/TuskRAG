# 可见评测报告列表

2026-11-06 工作树；R11/R13、A11、M1。新增 GET 报告列表、稳定游标、摘要响应和生成契约；评测页面尚未实现。

列表先核验知识库管理员，再用 SQL 展开报告证据并检查当前文档、版本、块、访问标签和位置。分页在可见性过滤后执行；返回前合并整页证据（含下一页探测项），按文档→知识库→成员→证据锁顺序复核。空页也复核权限，撤权不能变成成功空结果。错误不转换为空列表。

返回摘要、模型标记及汇总指标，不加载或传输完整逐题报告；最多每页 10 项，以可见项 digest 升序分页，新增内容通过从首页刷新获取。不返回隐藏报告计数，游标不引用隐藏项。SQL 仍需展开被检查报告的 JSON 证据；大规模历史报告的查询性能尚未测量。

## 证据

- 独立容器 tuskrag-report-list-pg-20261106，项目镜像 tuskrag-eval:20261029，回环端口 56446，隔离库 tuskrag_test_report_list，迁移至 21。无新迁移。
- WSL Go 1.27.2 Linux race 二进制在测试实例执行 `kb.test -test.run '^TestEvalReport' -test.v -test.count=1`：6 项及子场景全部通过。包括三页无重复/遗漏、空页结构、尾页游标、删除后的可见分页、普通成员/异租户拒绝、参数边界、列表真实锁等待期间撤权；既有报告导入/读取、审计回滚和 RLS 测试仍通过。
- 删除、受限 ACL、旧版本、禁用块、访问标签变化五类失效均验证列表为空且无下一页游标；已有单份读取仍返回 404。
- `go test ./internal/eval ./internal/httpapi`、Linux `go test -race -count=1 ./internal/eval ./internal/httpapi` 通过；HTTP 验证默认分页参数和空页 JSON。
- 相关 eval/kb/httpapi 包 golangci-lint：0 issues。sqlc/OpenAPI/TypeScript 生成及 `pnpm typecheck` 通过。

收尾查询 tenant 和 eval_report 残留记录均为 0；专用容器及匿名卷已移除。

HTTP 用替身验证传输，SQL、分页和权限用真实 PG 验证；没有页面变更，未运行浏览器测试。未改变解析、分块、检索、重排、评分或 Prompt，不重跑模型质量评测。下一小点：评测页面的报告导入、列表、指标与逐题查看；之后实现批量运行。
