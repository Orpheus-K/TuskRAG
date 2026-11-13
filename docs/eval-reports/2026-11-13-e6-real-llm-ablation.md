# E6 真实大模型生成评测与上下文组装消融报告（2026-11-13）

## 1. 测试环境与范围

- **代码基线**：`4214399` + 本地修复 `services/core/internal/db/db.go`（`SchemaVersion` 从 `27` 升至 `28`，匹配迁移 `00028_answer_eval_report.sql`）+ 新增集成测试 `TestRealLLMContextAssemblyAndE6GenerationEvalPostgres`（`services/core/internal/ingest/answer_r07_eval_integration_test.go`）。
- **API 端点**：`https://cliproxy.example.invalid`（OpenAI 兼容 `/v1/chat/completions`，`models.NewChatClient` 自动拼接路径）。
- **数据库**：云端 `test-host.invalid` 隔离 `tuskrag-postgres:verify` PG17（`127.0.0.1:<db-port>`），28 项迁移全绿。

## 2. `gemini-4-hydrogen-low` 实测结果

- **本地回答与追问改写评测**（`go test -v -count=1 -run "TestRealAnswerPromptEvaluation|TestRealFollowupRewriteEvaluation" ./internal/answer`）：
  - `TestRealAnswerPromptEvaluation`：`PASS`（68.15s），`prompt=grounded-v3 cases=8 passed=8 answered=4 refused=4`
  - `TestRealFollowupRewriteEvaluation`：`PASS`（17.12s），`rewrite=rewrite-v1 cases=4 standalone context before=1 after=4`
- **E6 消融集成测试阻塞**：
  - 随后在云端运行 `TestRealLLMContextAssemblyAndE6GenerationEvalPostgres` 时，`gemini-4-hydrogen-low` 上游返回 `HTTP/2 503`（`retry-after: 22208`，`auth_unavailable: no auth available (providers=antigravity, model=gemini-4-hydrogen-low; last upstream error: 404 Requested entity was not found.)`），因此转用同网关可用模型 `gemini-3-flash` 完成 E6 消融。

## 3. `gemini-3-flash` E6 消融与延迟实测

测试命令：`ingest.test -test.v -test.count=1 -test.timeout=15m -test.run "^TestRealLLMContextAssemblyAndE6GenerationEvalPostgres$"`（总耗时 `243.85s`，`PASS`）。

| 组别 | 门禁状态 | ValidCitationRate | SourceIntegrityRate | EvidenceHitRate | DeterministicPassRate | RefusalAccuracy | FalseRefusalRate | UnsupportedClaimRate | CitationSupportRate | TTFT p50 / p95 | Total p50 / p95 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `off-6k` | `passed` | `1.0000` | `1.0000` | `1.0000` | `1.0000` | `1.0000` | `0.0000` | `0.0000` | `1.0000` | `0.0ms` / `0.0ms` | `3629.6ms` / `5153.7ms` |
| `on-4k` | `passed` | `1.0000` | `1.0000` | `1.0000` | `1.0000` | `1.0000` | `0.0000` | `0.0000` | `1.0000` | `0.0ms` / `0.0ms` | `3212.9ms` / `10389.6ms` |
| `on-6k` | `passed` | `1.0000` | `1.0000` | `1.0000` | `0.7500` | `1.0000` | `0.0000` | `0.2500` | `1.0000` | `0.0ms` / `0.0ms` | `3386.7ms` / `5939.8ms` |
| `on-10k` | `passed` | `1.0000` | `1.0000` | `1.0000` | `1.0000` | `1.0000` | `0.0000` | `0.0000` | `1.0000` | `0.0ms` / `0.0ms` | `2574.5ms` / `4496.4ms` |

注：离线评测路径记录的 `ttft_p50` 与 `ttft_p95` 均为 `0.0ms`；真实端到端响应延迟见 `Total p50 / p95`。

## 4. 本地与云端清理核验

- 本地：`go vet ./...` 与 `go test -p 1 -count=1 ./internal/db ./internal/eval ./internal/httpapi ./internal/kb ./cmd/tuskrag` 全部通过。
- 云端：测试完成后输出 `<db-port>_IS_CLEAN` 与 `CGROUP_IS_CLEAN`，临时凭据文件已由 `trap cleanup EXIT` 删除。
