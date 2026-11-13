# 完整评测报告读取边界

2026-11-06 工作树，R11/T12、M1 评测报告页面的前置小点。当前仍只有 CLI 报告，未实现报告存储、REST 或页面。

`internal/eval.ValidateReport` 校验完整成功报告的元数据、摘要格式、逐题数量/唯一编号、证据位置、指标范围及分类计数，并由逐题值核对汇总均值与延迟 p50/p95。拒绝 NaN、无穷、负耗时、缺失总耗时、伪造汇总以及标记成功的降级重排。完整无答案集可以通过完整性检查，但不进入要求有答案题的比较门禁。

CLI 新增 `tuskrag eval validate <report.json>`；读取沿用严格 JSON 解码，拒绝未知字段、截断和尾随数据。比较命令对带逐题数据的报告调用相同校验；已有仅汇总 CI 基线继续使用已定义的汇总口径，不能通过完整报告校验。

## 验证

- `go test ./internal/eval`：通过，含实际 Run 生成报告的往返验证及损坏报告反例。
- `go test ./cmd/tuskrag -run '^TestValidateReportCommand$'`：通过，含成功、截断、尾随、未知字段与空对象。
- WSL Linux `go test -race -count=1 ./internal/eval` 和 `go test -race -count=1 ./cmd/tuskrag -run '^TestValidateReportCommand$'`：通过。
- `go vet ./internal/eval ./cmd/tuskrag`、相关包 golangci-lint v2.15.0：通过，0 issues；Go 可执行文件构建成功。
- 使用构建后的 CLI 对 [配置消费目录](2026-11-06-config-consumption/) 中 8 份完整报告逐个运行 validate：全部成功；四种已提交 CI 基线自比较成功。

未重跑模型质量评测：未改变解析、分块、检索、重排、评分与 Prompt；本次检查现有报告的自洽性。报告无期望文档绑定及可验证来源，无法据此重算相关性或证明当前权限；不把完整性校验当成授权或质量达标证明。后续必须实现知识库管理员授权的报告存取，再接入页面；批量运行仍为独立小点。
