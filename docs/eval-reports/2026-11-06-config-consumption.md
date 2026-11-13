# 检索配置消费与控制台调用

关联 M1、R01/R06/R10/R13/R20、A09、T05/T08/T09/T15 的相关子场景。

## 变化

- 检索在成员授权下读取知识库检索配置快照，模型调用期间不持有事务；返回和 trace 记录 `retrieval_revision`。
- 省略参数继承知识库设置，显式参数覆盖；关闭重排与要求重排的失败语义见[接口契约](../13-接口契约概要.md)。在途请求固定快照，下一请求读取新配置；权限与出域仍实时复核。
- 控制台默认继承，支持“本次关闭 / 本次自定义”，显示读取的修订号。客户端不再写死 Top-K=8 和关闭重排。
- 离线评测显式指定 Top-K 和重排开关，避免管理员修改默认值后悄悄改变评测模式。配置编辑表单尚未交付；目前通过配置 API 保存。

## 真实 PG 与评测对照

2026-11-06 工作树；Go 1.27.2，Linux `-race` 构建的测试二进制；授权旧实例上独立容器 `tuskrag-consume-pg-20261106`，项目镜像 `tuskrag-eval:20261029`（pgvector 0.8.1 / pg_search 0.22.6），回环端口 56446、独立测试库。模型为现有字符哈希向量与二元字符重合度重排替身，无外部模型调用。

`TestHybridRetrievalFromPublishedVersion` 通过（26.52 s）：验证配置 Top-K、重排、单次关闭与覆盖、阈值、配置更新期间不混用修订、下一请求采用新修订、缺失必需模型明确失败；保留原有跨租户、撤权、出域、模型失败、降级与出库复核反例。

`TestSyntheticZHEvaluation` 通过（35.70 s）。四种模式各运行 64 题显式参数和 64 题已保存参数，共 512 次查询。逐题融合/最终证据顺序完全一致，四项指标完全一致，所有报告越权数为 0；各显式报告另通过与仓库历史基线的 `eval compare` 门禁。

| 模式 | Recall@20 | Hit@5 | MRR@10 | nDCG@10 | 继承配置后的指标差 |
|---|---:|---:|---:|---:|---:|
| lexical | 0.981481 | 0.981481 | 0.959877 | 0.963114 | 0 |
| vector | 1 | 0.796296 | 0.613580 | 0.689538 | 0 |
| rrf | 1 | 0.981481 | 0.791872 | 0.835696 | 0 |
| rerank | 1 | 0.981481 | 0.924897 | 0.940768 | 0 |

八份逐题报告位于 [原始报告目录](2026-11-06-config-consumption/)。`*-saved.json` 为配置继承路径，测试逐次断言读取的修订为 1/2/3/4；报告中的运行配置摘要相同用于同参数对照。数据集只有 12 个可见块，有答案题 54 道、无答案题 10 道；不能由此证明真实召回质量、生成拒答质量或生产延迟达标。

复现（隔离 PostgreSQL 三个测试 URL 按 CI 设置）：

```sh
cd services/core
go test -race -count=1 -run '^(TestHybridRetrievalFromPublishedVersion|TestSyntheticZHEvaluation)$' -v ./internal/ingest
go test -race -count=1 ./internal/retrieval ./internal/httpapi ./internal/eval
# 对每个模式执行：
go run ./cmd/tuskrag eval compare ../../tests/eval/baselines/rrf.json ../../docs/eval-reports/2026-11-06-config-consumption/synthetic-zh-rrf.json
```

本轮在 WSL 用 `go test -race -c` 构建 ingest 测试二进制，复制到测试实例，保持仓库相对目录及数据集路径后执行上述两个测试。其余三个包在 WSL 执行竞态测试，均通过。

## 前端及工程检查

- `pnpm typecheck`、`pnpm test:web`（12 项）、`pnpm build`、相关包 golangci-lint（0 issues）通过；OpenAPI/客户端类型同步。
- Vite 生产预览下运行 `playwright test tests/e2e/retrieval-debug.spec.ts`，1280 / 375 px 两项通过。验证继承、关闭、自定义的实际请求体、修订显示、窄屏无页面溢出、撤权后清除结果；截图已检查：[桌面](2026-11-06-config-consumption/desktop.png)、[手机](2026-11-06-config-consumption/mobile.png)。浏览器使用 API 替身，真实数据库行为由上述集成测试覆盖。
- 未运行真实外部模型、全库回归或完整 M1 验收；本轮证据仅覆盖配置消费及受影响的检索链路。

测试结束时临时 publication 测试库残留数为 0。专用 PG 容器及本地预览进程在收尾时关闭。
