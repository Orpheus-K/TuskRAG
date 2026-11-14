# E7 Agentic 问答模式、MCP 接入与 V14 一致性验证报告（2026-11-14）

## 1. 变更基线与范围

- **代码基线**：`03bccbc`（M4-2：MCP Streamable HTTP、邻居块回溯、`V14`/`T11`）与 `ece2d92`（M4-3：Agentic QA `mode=agentic`、`step` SSE 事件、可选 `section_summary`）。
- **关联需求与验证**：`R09`、`R22`、`T11`、`T19`、`V14`、`E7`。

## 2. 已执行验证与实测结果

1. **V14 / T11 MCP 与 REST 权限一致性（真实 PG17）**：
   - 云端 `test-host.invalid` 隔离 PG17 容器运行 `TestMCPRESTConsistencyPostgres`：`PASS`（`1.75s`）。
   - 验证同一受限主体分别经 `POST /api/v1/search`（REST）与 `POST /mcp`（MCP `search_kb`）查询时返回一致的授权证据集合；验证文档级 `restricted` 权限与撤权后越权证据数为 `0`；验证 API Key `kb_ids` / `actions` 范围与 `read_section` / `get_chunk_neighbors` 一致。
2. **Agentic 问答与节摘要测试**：
   - `internal/answer/agentic_test.go`：验证 `mode=agentic` 工具调用闭环（`search_kb`、`read_section`、`get_chunk_neighbors`）、`step` SSE 事件顺序、步数/时间/Token 硬上限截断（`truncated`）以及未闭合或越权证据拒绝。
   - `internal/ingest` 与 `internal/kb`：验证迁移 `00035_kb_section_summary.sql`（`SchemaVersion = 35`）与可选 `section_summary` 生成、存储及 `SectionReferenceBody.Summary` 返回。

## 3. E7 跨文档多跳消融实测（`gemini-3.8-flash-high`）

- **测试入口**：`services/core/internal/ingest/e7_agentic_ablation_test.go`（`TestRealAgenticVsStandardAblationPostgres`，由 `TUSKRAG_TEST_CHAT_CONFIG` 门控，运行于云端 `test-host.invalid` 隔离 PG17 `tuskrag_test_e7`，35 项迁移全绿）。
- **测试条件**：4 篇跨文档引用中文企业知识库文档 + 4 道跨 2–3 跳问题（共 13 项待核对事实），同一检索配置下对比 `mode=standard` 与 `mode=agentic`（`MaxSteps=6`、`MaxDuration=45s`、`MaxEvidenceTokens=20000`）。
- **实测对比**：
  - **`standard` 模式**：`fact_recall=1.0000 (13/13)`，`valid_cit_rate=1.0000 (4/4)`，`avg_steps=0.00`，`truncated=0`，端到端延迟 `avg=15006.0ms`、`p50=22223ms`、`p95=23539ms`。
  - **`agentic` 模式**：`fact_recall=1.0000 (13/13)`，`valid_cit_rate=1.0000 (4/4)`，`avg_steps=6.00`，`truncated=4`（4 题均走满 `MaxSteps=6` 后基于已收集证据完成带引用作答），端到端延迟 `avg=48653.8ms`、`p50=46508ms`、`p95=58263ms`（`TestRealAgenticVsStandardAblationPostgres` `PASS`，`256.06s`）。
- **结论与边界**：在 4 篇小语料下 `standard` 单次 Top-K 即可覆盖全部跨文档证据且延迟显著更低（约快 3.2 倍）；`agentic` 链路与 `MaxSteps` 截断/带引用兜底在真实模型下工作正常，适合作为大语料单次召回不足时的显式按需模式，默认仍保持 `standard`。
