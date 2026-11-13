# 问答 MVP 开发预览验收

关联 R07/R08/R11/R13/R15/R17/R20、A02/A05/A16、T06—T09/T12/T16。基于 5c9b17f 加当前工作树；不提交凭据、不推送 GitHub。M1 证据与支持范围见[收束记录](2026-11-07-m1-closure.md)。

## 已交付范围

控制台选库、提问、流式回答、点击引用查看当前原文；证据不足拒答、失效编号移除、权限与出域复核、超时和取消；本人会话保存、刷新恢复、多轮追问和待审反馈。手机会话历史限制高度，避免长列表挤走回答。引用块若包含上一父节片段的重叠文本，预览补读同标题路径的紧邻上节；上节撤权或版本变化时隐藏整个预览。

Linux 以非 root 的 systemd `all` 服务运行，解析使用委派的 cgroup v2 与 Bubblewrap；PostgreSQL 17 独立进程。提供静态 core、内嵌页面、解析运行时、27 批迁移及[部署/冷备份/恢复脚本](../../deploy/linux/README.md)。`ops verify-storage` 只读分页校验每个保留版本的规范路径、字节数及 SHA-256，错误非零退出。

## 真实项目语料

使用四份当时的项目文档，固定[语料快照](../../tests/eval/project-documents-20261107.json)及[题集](../../tests/eval/project-documents-qa.json)。原 Markdown 均含当前解析器不支持的语言标记代码围栏，真实上传显式返回 `unsupported_format`，保留[失败记录](assets/mvp-20261107/project-markdown-failure.json)。同字节改用 `.txt` 上传后，4 份文档经实际受限解析、BGE-M3 嵌入和索引发布为 31 个块；未改动解析器来绕过质量门。

真实 SiliconFlow `BAAI/bge-m3`（1024 维）、`BAAI/bge-reranker-v2-m3` 与用户既有 CLI Proxy；包含 10 道可回答题、2 道证据不足题及 1 道追问。事实片段、期望文档、有效引用、预览原文、会话文本与反馈共同校验，失败结果完整保留。

| Prompt / 网关模型 | 通过 | 结论 |
|---|---:|---|
| grounded-v2 / gemini-3.5-flash-lite | 10/13 | 否定答案缺引用、拒答带引用及跨父节预览不完整 |
| grounded-v3 / gemini-3.5-flash-lite | 12/13 | 预览与拒答编号修正；仍把有明确依据的 Python 解析进程问题拒答 |
| grounded-v3 / gemini-3.8-flash-high | 13/13 | 事实、来源、拒答、追问和反馈断言全部通过，越权证据 0 |

原始报告：[v2 lite](assets/mvp-20261107/project-qa-grounded-v2-lite.json)、[v3 lite](assets/mvp-20261107/project-qa-grounded-v3-lite.json)、[v3 high](assets/mvp-20261107/project-qa-grounded-v3-high.json)。另有固定生成样本 [v3 lite 8/8](assets/mvp-20261107/real-prompt-v3-lite.json) 对照此前 v2 的 8/8。预览验收选择 high；轻量模型的失败不从报告中剔除。

所有项目语料题均显式报告 `context_budget`。high 的 12 道首轮首字为 3.513—16.555 秒，不能宣称达到首字延迟目标。该小样本验证项目文档上的闭环，不是客户语料质量，也不把引用编号映射正确等同于逐句语义支持。网关模型权重未冻结；此前 high 的合成追问改写曾超时，仍保留在[会话记录](2026-11-07-conversations.md)。

## 页面与 Linux 全链路

- 类型检查、生产构建及前端 112 项测试通过；预览新增三项覆盖上文、撤权和版本改变。生产入口 JS 约 504 kB，Vite 仍有体积提示。
- 两个模拟浏览器用例通过；最终真实 Linux 服务的 1280px/375px 四项流程在 51.1 秒通过，覆盖登录、选库、真实问答、引用上文、刷新、反馈、两轮追问及下述 PDF 两页原文，无水平溢出。[桌面](assets/mvp-20261107/live-answer-1280.png)、[手机](assets/mvp-20261107/live-answer-375.png)。
- 同机 Linux 生产 `run()` 全链路在最终 core 上通过：隔离原生解析 4.52 秒，worker/all 75.18 秒；包括上传、版本、清理、T13、引用原文、取消、两轮会话、改写、重载及反馈。此项模型为可控 HTTP 替身。[原生日志](assets/mvp-20261107/native-race.log)、[全链路日志](assets/mvp-20261107/process-race.log)。
- `VerifyStorage` 真实 PG race 1.26 秒通过：101 版本跨页、租户隔离、空/未知租户、路径错误、同尺寸内容损坏、缺失文件、取消以及不改状态。[日志](assets/mvp-20261107/storage-race.log)。相关 Linux race/vet、Go 定向 lint（0 issues）通过。

按本轮约定运行定向检查及功能闭环后的同机全链路，未重跑与本次改动无关的全仓 CI。PDF 解析算法未改动，新增夹具通过真实入库与页面验证，生成脚本 Ruff 通过。仓库 903 个已跟踪及未忽略文件的实际凭据匹配数为 0；密钥、测试账号密码和主密钥仍在仓库外。

core SHA-256：`9fbd0db0c8cbbb2c6ef2d9fc8bcd3863e7b23c6aae9f5b8f08bd5bd368cc31c3`；完整测试产物见[摘要](assets/mvp-20261107/binaries.sha256)。

## 跨页数字 PDF 引用

新增[两页合成住宿表](../../services/parser/tests/fixtures/pdf/policy-cross-page.pdf)，由现有 ReportLab 夹具生成器生成，两页 PNG 已逐页检查；字体沿用现有 OFL 许可。文件 SHA-256 `fa8c7f3ec7aa6059e789c7a6100cbfafddfb47aba48960c73276a15b2a255db7`，20,840 字节，第一页含北京 600 元，第二页续表含杭州 380 元，各页显式保留表头。

在恢复后的真实 Linux 服务通过 HTTP 上传并显式确认表头，经过既有 Python 隔离解析、BGE-M3、混合检索、真实重排及 CLI Proxy high 回答；一次提问引用物理页 `[1,1]` 和 `[2,2]`，600/380 断言正确，无非法引用、无降级，当前章节包含全部引用原文。[API 报告](assets/mvp-20261107/cross-page-pdf.json)。

桌面与手机均实际点击两处页码引用，核对北京/600 与杭州/380 表格单元格，刷新后引用仍可见。[手机第一页](assets/mvp-20261107/cross-page-375-1.png)、[手机第二页](assets/mvp-20261107/cross-page-375-2.png)、[桌面第一页](assets/mvp-20261107/cross-page-1280-1.png)、[桌面第二页](assets/mvp-20261107/cross-page-1280-2.png)。此证据覆盖 T06 的明确表头跨页样例；解析器没有合并跨页表格，也不据此承诺任意续表、扫描 PDF 或页图预览。

## 备份恢复

在专用测试环境停止服务，备份到新目录，并在同集群的全新 `template0` 空库及新文件目录恢复；保留源库，不将此写成“删除源库后恢复”的完整 T16 演练。

- 30 张表（含迁移表）的行数与排序后 JSON 内容摘要完全一致，包含 8 个版本、31 个块/向量、权限、凭据密文、471 条审计、50 个会话、57 条消息及 7 条反馈。[备份前](assets/mvp-20261107/backup-before.tables)、[恢复后](assets/mvp-20261107/backup-after.tables)。
- 8 份原件、69,816 字节，恢复后逐份校验成功；归档 SHA-256 全部通过。[备份](assets/mvp-20261107/backup.log)、[恢复](assets/mvp-20261107/restore.log)。
- 活动服务下备份及覆盖非空库恢复均被拒绝，未创建目标文件目录。[活动服务门禁](assets/mvp-20261107/backup-active-guard.log)、[非空库门禁](assets/mvp-20261107/restore-nonempty-guard.log)。演练修复了 `PGDATABASE` 不展开 URL 的问题；应用连接经 psql 环境变量与 `\connect` 核对，不把密码放入命令行参数。
- 直接重新调用在线模型时，仅 1/12 查询的分数/排序完全相同；完整差异和非零退出保留。恢复后的会话和反馈与备份前摘要一致。[在线对比](assets/mvp-20261107/restore-live-model-comparison.json)、[失败日志](assets/mvp-20261107/restore-live-model-comparison.log)。备份前各轮同题也已有重排分数波动，不能把在线模型变化当成存储变化。
- 对同一批[真实查询向量](assets/mvp-20261107/restore-query-vectors.json)，使用生产 BM25/HNSW/RRF 驱动和权限复核：12 题各 30 条候选的 ID、排序、全部分数、正文、节页及其他字段完全一致，越权 0；race 1.39 秒通过。[报告](assets/mvp-20261107/restore-fixed-recall.json)、[日志](assets/mvp-20261107/restore-fixed-recall.log)。重排未参与这一确定性对照。

恢复后的服务已启动并就绪。主密钥及服务配置独立保管，不包含在原件备份或发布包中。备份恢复只证明该单机本地存储流程，不代表 PITR、多实例或 S3 已实现。

## 可交付制品与剩余边界

Linux amd64 包 96,113,222 字节，SHA-256 `f7c5bbd3e9aa38cf91c41231f67b442758b4226bedab98431ce67a21399e72f3`；解包清单核验、普通用户启动解析器 0.5.0 与 core OpenAPI 均通过。制品保存在仓库外，未上传公共托管。OpenAPI SHA-256 `dab393516693d6eecc6f78ecc38efaabb2d47ae47d420e8f4844c6cdb0c19983`。包中不带 PG 镜像、密钥或模型权重。

本记录不宣布完整 M0/M1/M2/M5 验收通过：R07 的完整父节扩展、相邻合并、相似去重及模型 token 预算仍未完成；通用 PDF/OCR、Windows 隔离、OIDC、完整文档 ACL、MCP、反馈审核界面、生产资源与许可证发布门禁仍按既有计划处理。
