# 接续项目迭代

## 当前快照（2026-10-30）

- M0 进行中，退出条件见[开发计划](../docs/12-开发计划与里程碑.md#二m0-工程骨架与-spike)，完整产品与验收尚未完成。
- 已实现本地身份、知识库和成员管理、模型提供方及凭据、文档上传/版本更新/删除/列表、单文件本地存储、租户强制 RLS、审计和幂等。Go 单二进制内嵌 React 控制台；PostgreSQL 已有 19 批向前迁移。
- Linux `worker`/`all` 已连接解析、质量门、规整、分块、分词、嵌入、索引核验、原子发布和旧版本清理；按配置轮询最多 64 个租户，仅认领 `ingest`/`purge`，每实例最多 4 条池连接及 1 条监听连接。质量不达标或表头未确认时显式失败；GFM 表头由语法确定，其他表格可由上传者确认首行为表头。参数和边界见[部署](../docs/09-部署运维与可观测.md#八运维命令)、[入库对照](../docs/eval-reports/2026-10-28-ingest-worker.md)与[表头对照](../docs/eval-reports/2026-10-28-table-header-confirmation.md)。
- 解析覆盖 Go 原生 Markdown/TXT 与 Python 工具 CSV、受限 DOCX/XLSX/数字 PDF；所有格式都在一次性受限子进程执行。Linux 使用 bubblewrap、`prlimit` 和委派的 cgroup v2；启动核验工具版本。固定样例对照与隔离测试见[解析隔离报告](../docs/eval-reports/2026-10-28-native-parser-isolation.md)和 [PDF 报告](../docs/eval-reports/2026-10-28-pdf-structure.md)。Windows 完整沙箱、生产容器安全配置、通用 PDF、VLM、逐表预览和页级报告未实现。
- 结构分块支持父节、段落、列表、代码、表格，固定 BGE-M3 计数器对照参考逐 ID 结果 173,198/173,198；嵌入阶段校验本地与服务端用量并执行当前出域策略。真实 BGE-M3 分词探测 13 项通过；相关合成对照见[计数](../docs/eval-reports/2026-10-28-bgem3-counter.md)、[计划](../docs/eval-reports/2026-10-28-ingest-plan.md)、[嵌入](../docs/eval-reports/2026-10-28-embedding-stage.md)。
- 发布使用租约及数据库事务围栏核验内容、向量与索引，原子切换当前版本并入队清理；迁移 13/14 防止清理中的版本成为当前版本。隔离数据库测试覆盖版本切换、迟到结果、并发清理、租约丢失与回滚，见[发布](../docs/eval-reports/2026-10-28-version-publication.md)和[清理](../docs/eval-reports/2026-10-28-version-purge.md)。
- 隔离 PostgreSQL 与本地模型替身测试覆盖 Markdown 入库、模型 503 重试、索引失效恢复、质量门、CSV 沙箱、未知表头、租户隔离以及强杀真实 worker 后手动推进租约并重领；块和向量无重复。Linux `worker`/`all` 正常启动与 SIGTERM 退出经进程测试；不等于真实模型或完整 T01/T14 验收。
- 一次性迁移角色命令创建共享 BM25 和每嵌入空间 HNSW 索引。pg_search 0.22.6 通用计划在租户变化后曾复用旧租户条件；应用连接及检索事务强制自定义计划，单连接交替查询 12 次越权数为 0。连接池代理未验证，见[索引对照](../docs/eval-reports/2026-10-28-index-provision.md)。
- 单知识库检索已接入真实 PostgreSQL、查询嵌入客户端、RRF、权限预过滤与出库复核、审计、REST 和控制台；仅返回继承知识库权限的文档，明确标注未经重排。合成对照、并发撤权与模型错误测试见[召回对照](../docs/eval-reports/2026-10-28-hybrid-retrieval.md)；真实 SiliconFlow 模型已运行 synthetic-zh 64 题基线，仍不代表客户语料质量。
- 当前版本的目录与章节已接入 REST 和控制台；章节浏览前重新核验成员、版本及块，禁用或不匹配时拒绝读取。仅数字 PDF 显示物理页码，其他格式不把逻辑页伪装为物理页；对照见[章节读取](../docs/eval-reports/2026-10-29-section-reading.md)。
- 同格式重新上传已接入 REST 与控制台，真实进程测试验证处理中旧版可检索、成功后切换与失败版本不影响当前版本；迁移、幂等和权限对照见[版本更新报告](../docs/eval-reports/2026-10-28-version-upload.md)。
- 软删除在事务内撤下证据、记录审计并安排清理；清理可取消仍在运行的入库并删除原件和派生数据。真实进程及并发测试见[删除报告](../docs/eval-reports/2026-10-28-document-deletion.md)；清理状态界面和清理任务手动重试未实现。
- 可恢复的入库死信已支持任务查询和手动重试，原任务保留，版本重新排队与审计原子提交；详见[任务重试报告](../docs/eval-reports/2026-10-29-job-retry.md)。其他任务类型的查询和重试尚未实现。
- 已接入外置重排、阈值、显式降级、权限复核后的调试 trace 和控制台阶段表格；模型配置按请求传入，知识库持久化配置尚未实现。`tuskrag eval import/run/compare` 支持 JSONL 严格导入、批量检索、报告和两百分点回归门禁；`synthetic-zh` 64 题已在真实 PostgreSQL + 确定性替身上运行，四种模式越权数均为 0，RRF MRR@10=0.7919、替身重排后=0.9249。Go 相关竞态测试、12 项前端测试和两个视口的浏览器测试通过，证据及限制见[重排与评测报告](../docs/eval-reports/2026-10-29-m1.md)。这不等于真实模型或完整 M1 验收。
- 跨知识库检索、问答、MCP、通用 ACL、OIDC、备份恢复、T13 嵌入空间重建切换尚未实现。真实模型 E1/E2、E4 及 E3 三组已完成，E3 fixed-heading 六组已在旧云端实例完成；本地 32 份与云端 6 份通过报告（每份 64 题、越权数 0）已保存。V08 已在云端完成 20 万/100 万块，各 100 次查询、越权数 0。最终 SQL p95=84.978/271.923 ms，数据库 1.147/5.874 GiB，PG 采样峰值 1179.65/1220.61 MiB；20 万块达到 100 ms 预算，百万块仍未达到。使用 BM25 v2 授权版本预过滤及事务内关闭查询并行以复用 Jieba 缓存，见[容量报告](../docs/eval-reports/2026-10-30-sql-capacity.md)。远端 CI 的 Linux AppArmor/bubblewrap 隔离和生产浏览器懒加载故障注入已修复，最新代码 5c9b17f 的 CI 全绿，包含 Go 竞态、真实 PG、Linux 解析隔离与浏览器验收，见 [本轮证据](../docs/eval-reports/2026-10-30-sql-capacity.md)。
- 解析与分块合成评测及局限见 [CSV](../docs/eval-reports/2026-10-28-csv-structure.md)、[DOCX](../docs/eval-reports/2026-10-28-docx-structure.md)、[XLSX](../docs/eval-reports/2026-10-28-xlsx-structure.md)、[PDF](../docs/eval-reports/2026-10-28-pdf-structure.md)、[Markdown/TXT](../docs/eval-reports/2026-10-28-native-text-structure.md)、[规整](../docs/eval-reports/2026-10-28-markdown-assembly.md)、[表格](../docs/eval-reports/2026-10-28-table-chunking.md)、[段落](../docs/eval-reports/2026-10-28-text-chunking.md)、[父节](../docs/eval-reports/2026-10-28-parent-sections.md)和[结构分块](../docs/eval-reports/2026-10-28-structured-chunking.md)。

## 关键判断与环境

- 首发为客户内网私有化；项目自身许可已按所有者授权选择 MIT；pg_search AGPL 法务意见及完整传递依赖盘点尚未取得，见 [A03](../docs/11-架构决策记录.md#a03) 和[许可清单](../deploy/licenses/README.md)。
- 当前目录已初始化 Git，私有远端为 `Orpheus-K/TuskRAG`；本机部分可执行工具不能访问项目目录，使用 `E:/agent-project/.tools/tuskrag-work/frontend` 构建并同步源文件。SSH 别名 `legacy-test-host` 已验证连接 `legacy-host.invalid`。用户授权在该旧实例测试；本轮独立目录为 `<deployment-path>`。
- 本轮本机 PostgreSQL 容器、Docker 临时数据和两块合计 10 GiB 的临时盘已清理；保留评测报告。后续容量测试只在云端独立容器执行。10 月 30 日最终容量测试已完成，随机测试库数量为 0、无容量进程、磁盘约 15 GiB 可用。归档后 SSH 曾短暂中断；恢复后已停止专用 PG 容器并确认 `exited`，报告保留，其他应用未改动。

## 下一步

1. SiliconFlow 的 `BAAI/bge-m3`（1024 维）及 `BAAI/bge-reranker-v2-m3` 已实际调用成功，见[接口探测](../docs/eval-reports/2026-10-29-siliconflow-probe.md)；已完成 E1/E2、E4 及 E3 三组真实基线。云端 E3 fixed-heading 与 V08 20 万块报告已保存；20 万/100 万块最终容量测试已完成且回写 04；继续补齐 T13 后台重建切换、知识库检索配置及评测页面，另行验证真实多文档和并发负载；真实模型重排延迟和兼容驱动词法召回仍须按门槛判断。完整 M1 退出条件仍按开发计划验收，不提前启动后续里程碑。
2. 实现逐表预览、清理任务可见和重试及更多 PDF 格式；核验真实模型的分词、用量和出域行为，补充可重复数据集与准确率评测。
3. 补齐 Windows 解析隔离及发布许可；已有依赖元数据与许可文本校验值见许可清单，原生组件传递依赖与 pg_search 法务意见仍待完成。保留 CI 隔离门禁；按真实结果推进 M0/M1。

