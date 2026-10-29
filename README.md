# TuskRAG

面向企业 Agent 的轻量知识检索底座：**单个 PostgreSQL**（pgvector + BM25）承载全部状态与检索；**Go 核心单二进制**提供 API、SSE、MCP 与内嵌控制台，不加载模型；Python 仅作为一次性文档解析工具。向控制台与 Agent（REST / MCP）提供带权限、带引用、可评测的检索与问答。

当前状态：**M0 工程骨架进行中**。已有本地身份、知识库、文档上传、受限入库及单知识库混合召回；重排、问答及完整产品验收尚未完成。设计方案见 [docs](docs/README.md)；当前进度与验证证据见 [.agents/NOTES.md](.agents/NOTES.md)。

| 目标 | 手段 |
|---|---|
| 轻 | 2 个常驻服务；无 JVM/Redis/MinIO/MQ；解析在一次性子进程；模型外置 |
| 快 | 一条 SQL 完成预过滤 + BM25 + 向量 + RRF；只重排 Top-N；默认单轮检索 |
| 准 | 结构保真解析 + 页级质量门 + 按需 VLM；标题路径父子分块；交叉编码重排；引用校验与拒答；评测门禁 |

开发环境使用 Go 1.27.2、Python 3.13、uv、Node 22.18 和 pnpm 10.15。运行 `pnpm install` 和 `uv sync --locked --group dev`（在 `services/parser` 下）安装依赖；`pnpm build` 构建并嵌入控制台，生成 `dist/tuskrag`（Windows 为 `dist/tuskrag.exe`）；`pnpm check` 运行静态检查与基础测试。数据库与跨语言集成测试需要额外配置，见 [当前进度](.agents/NOTES.md)。PostgreSQL 初始化见 [deploy/postgres](deploy/postgres/README.md)。
