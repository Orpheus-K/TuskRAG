# 检索评测

关联 R06、R10、R11、T05、T15、V05、V10。`synthetic-zh.json` v1 包含 12 份人工编写的合成文档、54 道有答案题及 10 道无答案题。覆盖条款、改写、表格数值、跨文档；不包含客户数据。

## CI 冒烟

按 `.github/workflows/check.yml` 配置隔离 PostgreSQL 三个测试 URL，执行：

```sh
cd services/core
go test -race -count=1 -run '^TestSyntheticZHEvaluation$' -v ./internal/ingest
```

设置 `TUSKRAG_EVAL_REPORT_DIR` 为已有目录可输出逐题 JSON。测试使用真实 pg_search/pgvector、当前检索服务及 HTTP 模型替身；以字符哈希生成向量、二元字符重合度重排，不根据期望答案生成输出。额外插入受限文档与异租户文档，任何返回的非允许文档都会失败。

此测试直接建立已发布索引夹具，不证明解析、分块或端到端入库。12 个可见块少于 Recall@20 的窗口，因此 Recall@20 不能用于评估真实召回质量。无答案题不计算生成拒答指标；向量返回候选不代表系统已经回答问题。

## 已入库知识库与真实模型

将数据集文档按 `key` 上传到隔离知识库；记录实际文档 ID。配置文件包含 `actor`（`TenantID`、`UserID`）、`knowledge_base_id`、`documents`（key→ID）、`allowed_document_ids`（该测试主体允许看到的全部文档）、`model_label`（固定模型及版本）、`model_kind`（`real` 或 `stub`），以及可选 `reranker`（`provider_id`、`model`、`top_n`）。模型地址及凭据使用已有数据库提供方配置，配置文件和报告不包含密钥。

```sh
tuskrag eval run dataset.json config.json rrf report-rrf.json
tuskrag eval run dataset.json config.json rerank report-rerank.json
tuskrag eval compare baseline.json current.json
```

连接经 `TUSKRAG_DATABASE_URL` 的应用角色；加密凭据使用 `TUSKRAG_MASTER_KEY_FILE`。报告路径必须不存在，防止覆盖基线。查询失败会返回非零退出码并保存失败状态的部分报告；越权证据不会写入逐题结果。

`lexical`、`vector` 模式从同一次混合查询的完整 Top50 候选并集中投影排名，可用于 E1 质量对照，耗时仍是混合管线耗时，不能冒充独立驱动性能。`rerank` 强制关闭降级，可分别配置 Top-N 20/30/50。E3 需要不同分块配置的独立知识库；E4 需要兼容驱动，当前命令不模拟这两项实验。

报告含数据集规范化 JSON 的 SHA-256、运行配置 SHA-256、模型标签、逐题证据 ID、主指标及阶段 p50/p95。比较拒绝跨数据集、模式或真实/替身类型；任一主指标下降超过 0.02 或越权数非零均失败。`passed` 表示运行和权限检查成功，不代表产品准确率门槛或 M1 验收通过。

比较入口校验双方报告：数据集标识非空、模式及模型类型有效、状态为 `passed`、越权数为零；总题数和有答案题数为正，分类计数一致，四项指标均为 [0, 1] 内有限值。两份报告的总题数与有答案题数须一致。CI 汇总基线允许省略逐题结果；此检查不验证题目身份或重新计算指标，也不证明报告来源可信。关联 R11、T12。

离线运行显式固定 Top-K=30 及当前模式的重排开关，不继承知识库默认重排，避免默认配置污染实验。`TestSyntheticZHEvaluation` 另用相同参数保存知识库配置并运行继承路径，断言修订号、逐题证据顺序及指标与显式路径一致；有报告目录时同时保存 `*-saved.json`。

## SiliconFlow 真实消融实验

`TestRealModelEvaluation` 是显式启用的评测入口，普通测试和 CI 跳过。要求三个本地隔离 PostgreSQL 测试 URL，自动新建并清理一次性数据库。只读取仓库合成集，不接受客户语料。提供方固定为 `https://api.siliconflow.cn`，使用 `BAAI/bge-m3`（1024 维）和 `BAAI/bge-reranker-v2-m3`；不使用 Pro 型号。

设置 `TUSKRAG_REAL_EVAL=siliconflow-synthetic-only`、`TUSKRAG_REAL_EVAL_KEY_FILE`（仓库外凭据文件）及 `TUSKRAG_EVAL_REPORT_DIR`（已有空目录），执行 `go test -race -p 1 -count=1 -timeout 30m -run '^TestRealModelEvaluation$' -v ./internal/ingest`。每次调用都使用真实客户端和出域检查；凭据在测试库中加密，报告不包含凭据。先核验 13 组 tokenizer 用量，再核验每个入库块的用量。报告以独占创建方式写入，失败后换新的输出目录，避免覆盖证据。

- E1/E2：默认整篇单块，运行 lexical、vector、rrf 及重排 Top-N 20/30/50，每组 64 题。
- E3：分别设置 `TUSKRAG_REAL_EVAL_CHUNKS=structured`、`structured-no-heading`、`fixed`、`fixed-heading`。结构化方案使用实际原生 Markdown 解析器、规整器和 BuildPlan（父块上限 512、子块目标 32–64、硬上限 128、重叠上限 16 tokens）；固定方案按同一计数器切 64-token 窗口，重叠上限 16 tokens。无标题方案仅移除检索和嵌入输入的标题路径。合成夹具允许进程内调用解析函数，不证明生产沙箱或 T01。
- E4：设置 `TUSKRAG_REAL_EVAL_LEXICAL=tsvector`，按同一 gse v0.80.3 简体词典生成文档与查询词元，通过实际 GIN/ts_rank_cd SQL 替换词法召回；复用向量、RRF、权限复核和重排。此构造器仅用于显式消融实验，未提供不依赖 pg_search 的完整生产部署路径，不作为 V03 全部通过的证据。
- V08：单独设置 `TUSKRAG_CAPACITY_EVAL=200000` 与输出目录，执行 `go test -race -p 1 -count=1 -timeout 45m -run '^TestCapacity200K$' -v ./internal/ingest`。200000 块、1024 维、256 个确定性正弦向量循环分配；记录插入/建索引耗时、100 次 SQL 召回 p50/p95 和存储大小。不得用于模型准确率证明。需预留独立测试磁盘，并单独采集 PostgreSQL 内存。
## 知识库评测集定义校验

`tuskrag eval validate-set set.json` 只读校验管理员编写的知识库题集，格式见 [set-example.json](set-example.json)。这与带合成语料的 `dataset.json` 用途不同：题集直接引用已入库文档 ID，不携带原文；`eval run` 保留原有 dataset 格式；已保存题集使用下述 `eval run-set`。

题集包含 `name` 和非空 `cases`；每题包含唯一 `id`、`question`、显式布尔 `answerable`、`expected` 和 `tags`，可选 `reference_answer`。期望项包含正整数 `document_id`，可选 `section_id` 和 `page`；省略或 0 表示不限定，负数拒绝。有答案题必须有期望项，无答案题必须没有期望项；重复位置、题号或标签拒绝。允许同文档不同位置作为不同期望项。

上限：输入及序列化内容 8 MiB、10000 题、每题 100 个期望项和 32 个标签；名称 200 bytes、题号/单标签 128 bytes、问题 4096 bytes（与检索入口一致）、参考答案 32768 bytes。文字须为有效 UTF-8、不含 NUL，必填文字不允许全空白；题号及标签不允许首尾空白。未知字段、截断或尾随 JSON、缺失/null 的 answerable 均拒绝，不返回部分题集。

校验命令不访问数据库或模型，不证明文档存在、节页归属或操作者有权限；示例中的 ID 只是格式占位。知识库授权存取及修订已通过独立 REST 接口实现，见 [13](../../docs/13-接口契约概要.md)；题集列表、编辑页面及离线固定修订运行已接入，后台领域执行已接入，HTTP 创建与状态/结果查询已接入，控制台已接入固定修订运行、状态轮询与结果展示。参考答案仅保存为评测定义，不代表已实现引用或拒答评分。

## 已保存题集的固定修订运行

`tuskrag eval run-set binding.json config.json rrf report.json` 从配置知识库读取已保存题集并离线运行。binding 为 `{"set_id":9,"revision":2}`；两个值必须为正，不能用 revision=0 自动追随最新。配置沿用 `actor/knowledge_base_id/allowed_document_ids/reranker/model_label/model_kind`，不接受非空 `documents` 映射；期望项直接使用题集中的文档/节/页。allowed_document_ids 是本次评测主体允许返回的完整文档范围，并非只包含期望命中文档。

运行开始和完成时均通过题集存储复核指定修订的当前权限及目标；网络检索不持有存储事务。每次 Search 仍执行既有权限检查和返回证据范围检查。两入口共用查询、融合投影、重排、评分与汇总；重排强制关闭降级，取消/查询失败/越权/结束复核失败均停止成功发布。题集运行出错时清空逐题结果、汇总与延迟，不输出可误用的部分证据；开始前无法绑定有效定义则不创建输出文件。执行中失败仍可保存 status=failed 的诊断报告并非零退出。

成功报告 `dataset_version=set:<id>:<revision>`、dataset_sha256 为已保存定义摘要，包含标签/参考答案等定义变化；报告本身不写问题或参考答案。报告保存到新文件，不自动入库，也不等于后台任务已实现。文档内容变化、模型端版本及运行配置仍应由评测操作者固定；固定题集修订不冻结文档版本或模型权重。真实服务端结果落库前还须在落库事务复核全部返回证据，不能把一次检索时可见当作永久授权。

两入口均要求所有题目的检索配置修订与首题一致（包含未配置的修订 0），且每题结果和 trace 修订一致；检测到变化立即失败。

`eval run-set` 还会在运行前自动读取嵌入绑定（空间、入库配置修订、维度、距离、归一化）和检索配置修订，加入本次内存运行配置的 `configuration` 字段并纳入 `config_sha256`；配置文件显式提供该字段时沿用指定值，不自动替换失效快照。每题在分词和模型调用前核对快照，失配直接失败。已开始的单次查询仍使用它读取的不可变配置，下一题会拒绝新修订；不持有跨模型调用的数据库事务。原 `eval run` 支持显式传入同一字段，省略时保留原行为。

快照标识数据库内的配置修订，不保存密钥，不固定提供方后台模型权重或文档版本。后台 RunStore 已持久化同一运行配置，worker 使用有效租约执行并复核结果后原子落库，见[后台执行验收](../../docs/eval-reports/2026-11-06-eval-worker.md)；CLI 输出仍仅写本地报告。显式重排模型及参数仍取自本次 RunConfig，并一并参与配置摘要。

对照见[固定题集运行](../../docs/eval-reports/2026-11-06-set-run.md)。

## JSONL 题目导入格式

`tuskrag eval import manifest.json questions.jsonl dataset.json` 将文档清单和逐行题目转换为 `eval run` 使用的数据集，拒绝覆盖已有输出。清单包含 `version`、`documents`，不包含题目；文档记录仍使用 `key/title/content`。

每行是一个题目对象，例如：

```json
{"id":"q1","question":"如何申请？","expected":["policy"],"answerable":true,"tags":["语义改写"]}
{"id":"q2","question":"没有记载的问题？","expected":[],"answerable":false,"tags":["无答案"]}
```

`expected` 必须引用清单中的文档 key。重复题号、重复或未知期望文档、未知字段、空行、截断记录、单行超过 1 MiB 或总输入超过 16 MiB 均显式失败；不保存部分导入结果。导入后的 `dataset.json` 继续使用现有 `eval run/compare` 命令。

## 完整报告校验

`tuskrag eval validate report.json` 只读校验完整成功报告，成功退出 0，损坏、不完整或失败报告非零退出。检查身份与 SHA-256 格式、逐题数量及唯一编号、证据位置、有限指标与耗时、汇总均值和逐题延迟的 p50/p95；无答案题的指标保持 null，完整无答案数据集可查看但不可用作检索回归比较。重排模式含降级结果不能标为通过。

`eval compare` 对带逐题数据的报告执行同一检查；已提交的仅汇总 CI 基线仍按汇总口径比较，不满足完整报告校验。报告不包含期望文档绑定，因此校验不证明逐题相关性评分正确、数据来源可信或当前用户有证据访问权限；后续报告页面与存储接口必须独立落实知识库授权。该命令不修改评分、检索或历史报告。

两份比较输入均含完整逐题数据时，`eval compare` 还要求题号集合和每题有/无答案属性一致；允许行顺序变化，不能仅凭相同总题数放行。指标差值和两百分点门禁与已保存报告的对比接口共用实现，接口契约见 [13](../../docs/13-接口契约概要.md)。

## 项目真实文档问答 MVP

`project-documents-qa.json` 包含 10 道可回答题、2 道证据不足题及 1 道多轮追问。语料固定在 `project-documents-20261107.json`：四份当时的项目文档原始 UTF-8 字节及 SHA-256。此文件只作评测夹具，不替代 `docs/` 和 `deploy/` 的现行规范。由于原文含当前 Markdown 解析器不支持的语言标记代码围栏，验收使用同字节的 `.txt` 上传；不能据此声称原 Markdown 已通过。

`node tests/eval/run-project-qa.mjs <setup|model|ingest|evaluate|compare|vectors> /outside/private/config.json` 使用真实 HTTP、受限解析、嵌入、重排和生成服务。仅接受回环地址及显式 `opt_in: "project-documents-external-models"`；外部服务只收到这四份项目资料及题目。配置中的 `endpoint`、`tenant_id`、`password_file`、`embedding_key_file`、`chat_config_file`、`state_file`、`report_file`、`restore_report_file` 都由操作者在仓库外提供。chat 配置为 `base_url/model/api_key`；其他凭据文件只含密钥或测试密码。账号固定为专用 `mvp-admin`。

顺序为 setup → 以迁移角色配置新空间索引 → ingest → evaluate；model 仅用于显式增加对照模型并切换评测状态，报告应另用新文件。evaluate 校验事实片段、期望来源、引用映射、当前节及重叠上文、保存文本与流式文本一致、拒答、多轮和反馈；失败非零退出并保留报告。不会把映射正确等同于逐句语义支持，也不将 13 题结果外推为企业语料质量。模型端权重未冻结。

恢复后运行 compare：严格比较 12 道查询的证据 ID、顺序、分数和内容摘要，以及保存的会话和反馈，保留全部差异后非零退出。本次在线提供方重复调用有分数和邻近排名波动，不能据此单独判定恢复损坏或成功。

确定性恢复检查另用 vectors：一次调用 BGE-M3 获取 12 道题的向量，写入配置指定、原先不存在的 `recall_fixture_file`。设置 `TUSKRAG_RESTORE_EVAL=project-documents`、`TUSKRAG_RESTORE_SOURCE_URL`、`TUSKRAG_RESTORE_TARGET_URL`（不同的同机回环 `tuskrag_test_` 库及应用角色）、`TUSKRAG_RESTORE_FIXTURE` 和新 `TUSKRAG_RESTORE_REPORT`，在 `services/core` 运行 `go test -race -count=1 -run '^TestRestoredProjectRecall$' -v ./internal/retrieval`。两库使用同一查询向量，经生产 BM25/HNSW/RRF 驱动及权限复核后比较所有候选字段；不调用重排服务。此检查与全部业务表摘要、原件校验及会话读取共同证明恢复一致性，不代替模型质量评测。

浏览器流程用 `TUSKRAG_LIVE_QA=project-documents`、仓库外 `TUSKRAG_LIVE_QA_PASSWORD_FILE`、可选 `TUSKRAG_LIVE_QA_URL` 与 `TUSKRAG_LIVE_QA_MODEL_ID` 启用 `tests/e2e/live-answer.spec.ts`；默认 CI 跳过真实调用。

补充的 `pdf` 模式仅接受 SHA-256 固定的 `services/parser/tests/fixtures/pdf/policy-cross-page.pdf`；配置另外提供 `pdf_file`、全新的 `pdf_state_file` 和 `pdf_report_file`。它创建独立知识库，继承已验证的检索配置，显式确认首行为表头后上传，等待发布，再检查两个城市金额与物理页 1/2 的引用。`tests/e2e/live-pdf.spec.ts` 用相同显式环境验证实际页面、两处原文表格及刷新恢复。该样例每页重复表头，不证明自动跨页合表。

## 生成评测与 R07/E6 消融

`tuskrag eval` 新增三条生成评测命令（关联 R07、R11、T12、E6）：

```sh
tuskrag eval answer-run dataset.json config.json report.json
tuskrag eval answer-validate report.json
tuskrag eval answer-compare baseline.json current.json
```

`answer-run` 基于 `TUSKRAG_DATABASE_URL`、`TUSKRAG_MASTER_KEY_FILE`、知识库与已登记 `chat_model` 运行完整问答流水线，报告仅保留题目编号、终态、证据标识与块数、合法性/完整性/确定性断言/拒答/误拒/无引用统计、越权数（必须为 0）与 `first_delta`/`total` p50/p95 延迟，不写入正文或模型输出全文。配置可显式设置 `parent_backtrack`（默认 `true`）与 `context_budget_tokens`（默认 `6000`）；未配置 `judge` 时 `citation_support_rate` 显式为 `null`，启用 `judge` 时通过受出域策略控制的 `judge-v1` 评审器计算逐句引用支持率。`answer-compare` 校验双方可比性与两百分点回归门禁（正向指标下降 `> 0.02` 或负向指标上升 `> 0.02` 均返回非零退出）。

长文档合成评测集见 [r07-e6-zh.json](r07-e6-zh.json)（6 份合成文档、8 道有答案题及 2 道无答案题），用于验证小节父节回溯补齐例外条款、跨块拆表合并、>0.95 冗余去重及 `4k/6k/10k` 预算消融。
