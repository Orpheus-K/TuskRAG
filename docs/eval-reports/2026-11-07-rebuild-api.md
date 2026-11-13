# 重建 HTTP 接口

2026-11-07 工作树；R16/R05/R13、T13、M1。

新增创建 POST /kbs/{kb}/rebuilds、查询 GET /kbs/{kb}/rebuilds/{job}、取消 POST /kbs/{kb}/rebuilds/{job}/cancel，版本前缀 /api/v1。复用 RebuildService 的权限、幂等、事务及取消逻辑，主体只取当前会话。运行时依赖已注入。Go 类型约束生成 OpenAPI 和 TypeScript schema；详见 [13](../13-接口契约概要.md)。

验证：

- go test ./internal/httpapi ./internal/kb ./cmd/tuskrag、相关 go vet/golangci-lint（0 issues）、CGO_ENABLED=0 Linux 构建通过。
- WSL Linux go test -race ./internal/httpapi 通过。
- 新接口测试覆盖会话/CSRF、幂等键必填、必填/范围/未知字段、主体和路径传递、创建冲突、归档、权限、出域、目标不支持、依赖缺失、错误脱敏、查询 nullable 字段及取消 CSRF。
- go run ./cmd/tuskrag openapi、pnpm generate:client、pnpm typecheck、git diff --check 通过。客户端 schema 包含请求、响应、六态枚举和 nullable；本轮未新增前端交互。
- 首轮本机 HTTP 测试因默认 Windows Temp 目录拒绝 multipart 临时文件创建失败；按现有测试环境将 TEMP/TMP 指向项目工具临时目录后整包通过，未改上传逻辑。

HTTP 测试使用领域接口替身验证传输和鉴权边界，不证明数据库事务。领域持久化、撤权、取消和 worker 的真实 PG 证据沿用前轮报告；本轮无 SQL/事务逻辑变化，未重复数据库测试或模型质量评测。控制台和完整多文档 T13 质量验收仍待完成。
