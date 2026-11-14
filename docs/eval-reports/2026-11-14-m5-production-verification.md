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
  - `tuskrag ops reindex <tenant-id> --kb <kb-id>`（[ops_m5.go](../../services/core/internal/operations/ops_m5.go)）：读取知识库 `embedding_space_id`，调用 `indexing.Provision` 核验/补齐共享 BM25 与 HNSW 索引并执行 `ANALYZE chunk` / `ANALYZE chunk_vector`。
- **文档更新**：同步更新 [部署手册](../../deploy/README.md)、[09 部署运维与可观测](../09-部署运维与可观测.md) 与 [12 开发计划与里程碑](../12-开发计划与里程碑.md)。

## 2. 实测验证结果

1. **云端隔离 PG17（`test-host.invalid`，`tuskrag-postgres:verify`，35 项迁移全绿）**：
   - `operations.linux.test`：`PASS`
     - `TestM5OperationsDiagnoseAndRotateMasterKeyPostgres`（`0.36s`）：验证 `Diagnose`、`RotateMasterKey`（对 `model_provider` 与 `oidc_provider` 凭据单事务重加密且旧密钥失效）及 `ReindexKB`（`tuskrag ops reindex` 索引校验与 `ANALYZE`）。
     - `TestDiagnoseAndRotateMasterKeyValidation`：`PASS`。
   - `T16` / `V12` 冷备与空库恢复演练：`PASS`（`T16_V12_RESTORE_OK`）：
     - 在隔离 PG17 执行 `tuskrag.linux bootstrap` → `ops diagnose 1` → `ops verify-storage 1` → `pg_dump -U postgres -Fc -d tuskrag` 与存储目录 `tar` 备份 → 新建空库 `tuskrag_restored` 并 `pg_restore` + 存储解包 → `ops verify-storage 1` 与 `ops diagnose 1` 均返回成功。
   - `jobs.linux.test`（`V01` / `V02`）：`PASS`（全部 17 项真实 PG 用例通过，2 个子进程辅助入口 `TestClaimChild` / `TestWorkerProcessChild` 按设计跳过）：
     - 覆盖 `TestConcurrentClaimsAndStaleCompletion`（`0.06s`）、`TestExpiryDuringEffectsRollsBack`（`0.04s`）、`TestCheckpointAndFailureEffectsAreFenced`（`0.05s`）、`TestKilledClaimantCanBeRecovered`（`0.06s`）、`TestWorkerRecoversExhaustedLease`（`0.05s`）、`TestWorkerRenewsAndStopsWhenLeaseIsReclaimed`（`0.66s`）、`TestWorkerProcessCrashAndRecovery`（`0.10s`）。
2. **本地 Go 单元与 HTTP 端点测试**（Windows）：
   - `go vet ./...`：通过。
   - `go test -p 1 -count=1 ./internal/operations ./internal/jobs ./internal/models ./internal/secrets ./cmd/tuskrag`：全部 `ok`。
   - `httpapi.test.exe -test.run=TestMetricsEndpointLoopbackAndTokenAuth`：`PASS`（验证未带 Token 远端 403、回环 200 及 `TUSKRAG_METRICS_TOKEN` Bearer 校验）。
3. **前端、Lint 与 SBOM 脚本检查**：
   - `pnpm typecheck`、`pnpm test:web`（18 文件 122 项）与 `pnpm build:web` 全部通过。
   - Linux 端 `npx @biomejs/biome@2.5.15 check .`（`pnpm lint` 等价检查）：71 个文件全部通过，0 错误 0 警告。
   - WSL 运行 `bash deploy/linux/sbom.sh`：成功输出 `go.sum`、`uv.lock`、`pnpm-lock.yaml` 快照及 `SHA256SUMS` 校验清单（环境未安装 `syft`，按预期跳过 CycloneDX 生成并提示）。

## 3. 未运行项与环境边界说明

- **100 万块规模压测（`V08`）与 50/100 并发会话压测（`V09`）**：本轮未在独立生产规格主机上执行 `k6`/`locust` 长时压测，因此 [01 定位与范围](../01-定位与范围.md) 与 [09 部署运维与可观测](../09-部署运维与可观测.md) 中的目标容量与延迟数字仍保留为“目标值”，不写成实测结论。
- **跨版本滚动升级（`V13`）与发行版 Docker 镜像推送 / `syft` CycloneDX 扫描**：已完成 `T16`/`V12` 冷备恢复演练与 `sbom.sh` 锁文件快照/SHA-256 校验；跨版本滚动升级演练与完整发行镜像 `syft` 扫描需在目标发行镜像构建与发布环境独立执行。
