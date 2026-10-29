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
