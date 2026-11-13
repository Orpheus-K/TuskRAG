# M5 生产化交付与验证报告（2026-11-14）

## 1. 交付范围与制品

- **容器与编排**：
  - [Dockerfile](../../deploy/Dockerfile)：多阶段构建（`pnpm build` → `CGO_ENABLED=0` 静态 Go 二进制 → 非 root 运行时 + `bubblewrap` + Python 解析器）。
  - [docker-compose.lite.yml](../../deploy/compose/docker-compose.lite.yml)（单机 `postgres` + `tuskrag` `role=all`）、[docker-compose.standard.yml](../../deploy/compose/docker-compose.standard.yml)（`postgres` + `tuskrag-migrate` + `tuskrag-api` + `tuskrag-worker`）、[docker-compose.model-node.yml](../../deploy/compose/docker-compose.model-node.yml) 与 [docker-compose.legacy-office.yml](../../deploy/compose/docker-compose.legacy-office.yml)。
  - [prometheus-alerts.yml](../../deploy/compose/prometheus-alerts.yml)、[.env.example](../../deploy/compose/.env.example) 与 [sbom.sh](../../deploy/linux/sbom.sh)。
- **可观测与运维命令**：
  - `GET /metrics`（[metrics.go](../../services/core/internal/httpapi/metrics.go)）：Prometheus 文本格式导出（`tuskrag_up`、`tuskrag_ready`、`tuskrag_http_requests_in_flight`、`tuskrag_http_requests_total`、`tuskrag_http_request_duration_seconds_sum`、`tuskrag_go_goroutines`、`tuskrag_go_mem_alloc_bytes`、`tuskrag_go_mem_sys_bytes`），默认仅允许回环地址或 `TUSKRAG_METRICS_TOKEN` Bearer 鉴权。
  - `tuskrag ops diagnose <tenant-id>`（[ops_m5.go](../../services/core/internal/operations/ops_m5.go)）：导出脱敏诊断快照（Go/Schema 版本、PG 扩展、词汇驱动与任务计数，不含正文与密钥）。
  - `tuskrag ops rotate-master-key <tenant-id>`（[ops_m5.go](../../services/core/internal/operations/ops_m5.go)）：单事务内将 `model_provider` 与 `oidc_provider` 密文从 `TUSKRAG_OLD_MASTER_KEY_FILE` 重加密至 `TUSKRAG_MASTER_KEY_FILE`。
- **文档更新**：同步更新 [部署手册](../../deploy/README.md)、[09 部署运维与可观测](../09-部署运维与可观测.md) 与 [12 开发计划与里程碑](../12-开发计划与里程碑.md)。

## 2. 实测验证结果

1. **云端隔离 PG17（`test-host.invalid`，`tuskrag-postgres:verify`，35 项迁移全绿）**：
   - `operations.linux.test`：`PASS`
     - `TestM5OperationsDiagnoseAndRotateMasterKeyPostgres`（`0.27s`）：验证 `Diagnose` 与 `RotateMasterKey` 对 `model_provider` 和 `oidc_provider` 凭据单事务重加密成功，且旧密钥失效。
     - `TestDiagnoseAndRotateMasterKeyValidation`：`PASS`。
   - `jobs.linux.test`（`V01` / `V02`）：`PASS`（全部 17 项真实 PG 用例通过，2 个子进程辅助入口 `TestClaimChild` / `TestWorkerProcessChild` 按设计跳过）：
     - 覆盖 `TestConcurrentClaimsAndStaleCompletion`（`0.06s`）、`TestExpiryDuringEffectsRollsBack`（`0.04s`）、`TestCheckpointAndFailureEffectsAreFenced`（`0.05s`）、`TestKilledClaimantCanBeRecovered`（`0.06s`）、`TestWorkerRecoversExhaustedLease`（`0.05s`）、`TestWorkerRenewsAndStopsWhenLeaseIsReclaimed`（`0.66s`）、`TestWorkerProcessCrashAndRecovery`（`0.10s`）。
2. **本地 Go 单元与 HTTP 端点测试**（Windows）：
   - `go vet ./...`：通过。
   - `go test -p 1 -count=1 ./internal/operations ./internal/jobs ./internal/models ./internal/secrets ./cmd/tuskrag`：全部 `ok`。
   - `httpapi.test.exe -test.run=TestMetricsEndpointLoopbackAndTokenAuth`：`PASS`（验证未带 Token 远端 403、回环 200 及 `TUSKRAG_METRICS_TOKEN` Bearer 校验）。
3. **前端与契约检查**：
   - `pnpm typecheck`、`pnpm test:web`（18 文件 122 项）与 `pnpm build:web`（`496ms`）全部通过。

## 3. 未运行项与环境边界说明

- **100 万块规模压测（`V08`）与 50/100 并发会话压测（`V09`）**：本轮未在独立生产规格主机上执行 `k6`/`locust` 长时压测，因此 [01 定位与范围](../01-定位与范围.md) 与 [09 部署运维与可观测](../09-部署运维与可观测.md) 中的目标容量与延迟数字仍保留为“目标值”，不写成实测结论。
- **滚动升级（`V13`）与完整生产冷备恢复（`T16`/`V12`）**：本轮聚焦新增 `ops diagnose`/`ops rotate-master-key`、`V01`/`V02` 与编排交付；跨版本滚动升级演练与完整冷备恢复演练需在目标发行镜像构建后独立执行。
- **Docker 镜像构建与 `syft` CycloneDX 生成**：已提供 `deploy/Dockerfile` 与 `deploy/linux/sbom.sh`，本轮未在目标发行环境执行全量镜像推送与 `syft` 扫描。
