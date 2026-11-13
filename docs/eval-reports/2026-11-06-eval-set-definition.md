# 知识库评测集定义校验

2026-11-06 工作树，R11/T12、M1。完成评测集管理的输入边界：`internal/eval/set.go` 定义管理员编写的题目、期望文档/节/页、显式答案属性、参考答案及标签；`tuskrag eval validate-set` 可只读检查 JSON 文件。字段与限制单点维护在[评测说明](../../tests/eval/README.md#知识库评测集定义校验)。

现有 synthetic Dataset 携带语料且只绑定文档 key，不能表达产品要求的节/页。知识库定义直接引用文档 ID，不引入原文副本或合成语料兼容转换；未来存储及运行须消费此定义。既有离线入口和评分未改动。

## 验证

- `go test ./internal/eval ./cmd/tuskrag` 通过。
- WSL Go 1.27.2 下 `go test -race -count=1 ./internal/eval ./cmd/tuskrag` 通过。
- `go vet ./internal/eval ./cmd/tuskrag`、相关两包 golangci-lint 通过（0 issues）。默认 lint 锁检查曾拒绝运行；核实无存活 Windows lint 进程后，使用 `--allow-parallel-runners` 完成。
- `go run ./cmd/tuskrag eval validate-set ../../tests/eval/set-example.json` 成功退出。
- `git diff --check` 通过。

测试覆盖中文及参考答案/位置往返、空集、重复题号/标签/位置、答案属性与期望项矛盾、负数位置、问题字节上限、非法 UTF-8/NUL、总量限制、未知字段、截断/尾随 JSON、缺失/null 答案属性、失败不返回部分内容，以及 CLI 不改写输入文件。

本小点没有数据库写入、HTTP 或页面；未运行 PG、浏览器或模型质量评测。校验成功不证明目标存在或有权限。下一步实现知识库授权下的题集存取与修订，真实 PG 验证位置归属、RLS、撤权、归档及并发更新；再接编辑页面和批量运行。完整 T12/M1 仍未完成。
