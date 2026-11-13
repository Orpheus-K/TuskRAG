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

## 3. E5 真实 VLM 消融未运行说明

- **未运行项**：真实扫描件/复杂版面数据集上的「原生解析 vs 原生 + VLM 低分页」端到端召回与 Token 成本对照消融。
- **原因**：本轮验证环境未配置真实线上 VLM 模型凭据与扫描版面基准集，仅使用本地 HTTP 替身验证 VLM 协议、质量门触发逻辑、页面渲染与出域策略。不以替身测试替代真实 VLM 准确率或延迟结论。
