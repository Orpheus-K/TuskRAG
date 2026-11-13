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

## 3. E7 跨文档消融未运行说明

- **未运行项**：真实大模型下跨文档多跳题集「`standard` 模式 vs `agentic` 模式」的准确率、步数分布与端到端延迟对比。
- **原因**：本轮验证使用本地确定性替身驱动多步工具调用与上限测试，未接入真实多跳题集与外部实时推理网关执行对比消融。待目标推理模型配额就绪后补跑基准并决定自动升级阈值。
