# E5 解析增强、VLM 兜底与 V07 安全验证报告（2026-11-14）

## 1. 变更基线与范围

- **代码基线**：`4aa6a27`（M4-4：PPTX / HTML 解析、`tuskrag-parse render`、VLM 客户端与低分/扫描页重解析、SSRF 防护 URL 导入）+ `c16dded`。
- **关联需求与验证**：`R03`、`R22`、`T02`、`T03`、`V07`、`E5`。

## 2. 已执行验证与实测结果

1. **Go 单元与安全边界测试**（Windows 本地，`go test -p 1 -count=1 ./internal/models ./internal/ingest ./internal/kb ./cmd/tuskrag` 及独立编译 `httpapi.test.exe`）：
   - `internal/models/vlm_test.go`：校验 OpenAI 兼容多模态 VLM `chat/completions` 请求格式（`image_url` data URI）、用量统计与出域策略拦截（`deny` 拒绝外部调用）。
   - `internal/ingest/vlm_reparse_test.go`：校验质量门触发低分页/扫描页重解析回填、页级报告 `VLMPages` / `VLMTokens` / `VLMUsed` 累计与显式降级/失败行为。
   - `internal/kb/url_import_test.go`：校验 URL 导入 SSRF 防护（拦截回环/私网 IP、非 `http/https` 协议、重定向至内网地址及超限响应体）。
2. **Python 解析器与 V07 恶意样本测试**：
   - Windows 非 PDF 套件（`pytest tests/test_csv.py tests/test_docx.py tests/test_xlsx.py`）：`86 passed in 68.80s`。
   - 云端 Ubuntu `test-host.invalid` 隔离 `.venv`（`pytest -q tests/test_pptx_html_render.py tests/test_cli.py tests/test_pdf.py`）：`24 passed in 6.24s`，覆盖 PPTX 讲义/备注提取、HTML 正文/表格提取、`tuskrag-parse render` 页面栅格化、以及 V07 恶意样本（OOXML zip 炸弹/超限、XXE 防护、畸形 PDF 与伪装扩展名拒收）。

## 3. E5 真实 VLM 消融实测（`gemini-3.8-flash-high`）

- **测试入口**：`services/core/internal/ingest/e5_vlm_ablation_test.go`（`TestRealVLMReparseAblation`，由 `TUSKRAG_TEST_VLM_CONFIG` 与 `TUSKRAG_TEST_SCANNED_PNG_DIR` 门控）。
- **测试条件**：2 页纯图像扫描页 PNG（含标题、键值对及 3 行 Markdown 表格，共 6 项关键事实：`TuskRAG`、`VLM-2026`、`99.95%`、`150`、`PostgreSQL 17`、`AES-256-GCM`），经 OpenAI 兼容网关调用 `gemini-3.8-flash-high` 执行 `ReparseLowScorePages`。
- **对比结果**：
  - **基线（原生纯文本 PDF 提取）**：`chars=0`，`blocks=0`，关键事实命中 `0/6`（0.00%）。
  - **原生 + VLM 低分页重解析**：触发重解析页 `[1 2]`，提取 `322` 字符 / `7` 块（含 Markdown 表格），关键事实命中 `6/6`（100.00%），VLM 总消耗 `2619` tokens，总耗时 `13.30s`（`13303ms`），`TestRealVLMReparseAblation` `PASS`。
- **边界说明**：本轮实测为 2 页合成扫描 PNG 样本，尚未在大规模真实复杂版面 PDF 数据集上做千页级成本/精度统计。
