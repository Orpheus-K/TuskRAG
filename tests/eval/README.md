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

## SiliconFlow 真实消融实验

`TestRealModelEvaluation` 是显式启用的评测入口，普通测试和 CI 跳过。要求三个本地隔离 PostgreSQL 测试 URL，自动新建并清理一次性数据库。只读取仓库合成集，不接受客户语料。提供方固定为 `https://api.siliconflow.cn`，使用 `BAAI/bge-m3`（1024 维）和 `BAAI/bge-reranker-v2-m3`；不使用 Pro 型号。

设置 `TUSKRAG_REAL_EVAL=siliconflow-synthetic-only`、`TUSKRAG_REAL_EVAL_KEY_FILE`（仓库外凭据文件）及 `TUSKRAG_EVAL_REPORT_DIR`（已有空目录），执行 `go test -race -p 1 -count=1 -timeout 30m -run '^TestRealModelEvaluation$' -v ./internal/ingest`。每次调用都使用真实客户端和出域检查；凭据在测试库中加密，报告不包含凭据。先核验 13 组 tokenizer 用量，再核验每个入库块的用量。报告以独占创建方式写入，失败后换新的输出目录，避免覆盖证据。

- E1/E2：默认整篇单块，运行 lexical、vector、rrf 及重排 Top-N 20/30/50，每组 64 题。
- E3：分别设置 `TUSKRAG_REAL_EVAL_CHUNKS=structured`、`structured-no-heading`、`fixed`、`fixed-heading`。结构化方案使用实际原生 Markdown 解析器、规整器和 BuildPlan（父块上限 512、子块目标 32–64、硬上限 128、重叠上限 16 tokens）；固定方案按同一计数器切 64-token 窗口，重叠上限 16 tokens。无标题方案仅移除检索和嵌入输入的标题路径。合成夹具允许进程内调用解析函数，不证明生产沙箱或 T01。
- E4：设置 `TUSKRAG_REAL_EVAL_LEXICAL=tsvector`，按同一 gse v0.80.3 简体词典生成文档与查询词元，通过实际 GIN/ts_rank_cd SQL 替换词法召回；复用向量、RRF、权限复核和重排。此构造器仅用于显式消融实验，未提供不依赖 pg_search 的完整生产部署路径，不作为 V03 全部通过的证据。
- V08：单独设置 `TUSKRAG_CAPACITY_EVAL=200000` 与输出目录，执行 `go test -race -p 1 -count=1 -timeout 45m -run '^TestCapacity200K$' -v ./internal/ingest`。200000 块、1024 维、256 个确定性正弦向量循环分配；记录插入/建索引耗时、100 次 SQL 召回 p50/p95 和存储大小。不得用于模型准确率证明。需预留独立测试磁盘，并单独采集 PostgreSQL 内存。
