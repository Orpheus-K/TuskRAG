# 真实模型基线（本地已完成部分）

2026-10-29。使用 SiliconFlow BAAI/bge-m3（1024 维）与 BAAI/bge-reranker-v2-m3，模型提供方未公开固定权重版本。数据集 synthetic-zh-v1-20261029，共 64 题，其中 54 题可回答、10 题无答案。

以下从已保存 JSON 报告提取；passed 表示评测运行完成，不代表所有性能及里程碑门槛通过。所有报告越权证据数均为 0。

| 实验 | 模式 | Recall@20 | Hit@5 | MRR@10 | 总耗时 p95（ms） |
|---|---|---:|---:|---:|---:|
| E1/E2 | [lexical-30](2026-10-29-real/./lexical-30.json) | 0.9815 | 0.9815 | 0.9599 | 579.4 |
| E1/E2 | [vector-30](2026-10-29-real/./vector-30.json) | 1.0000 | 1.0000 | 0.9543 | 879.9 |
| E1/E2 | [rrf-30](2026-10-29-real/./rrf-30.json) | 1.0000 | 0.9815 | 0.9527 | 673.0 |
| E1/E2 | [rerank-20](2026-10-29-real/./rerank-20.json) | 1.0000 | 1.0000 | 0.9907 | 1118.2 |
| E1/E2 | [rerank-30](2026-10-29-real/./rerank-30.json) | 1.0000 | 1.0000 | 0.9907 | 1073.2 |
| E1/E2 | [rerank-50](2026-10-29-real/./rerank-50.json) | 1.0000 | 1.0000 | 0.9907 | 988.5 |
| E4 tsvector | [lexical-30](2026-10-29-real/e4-tsvector/lexical-30.json) | 0.9630 | 0.9630 | 0.8935 | 467.3 |
| E4 tsvector | [vector-30](2026-10-29-real/e4-tsvector/vector-30.json) | 1.0000 | 1.0000 | 0.9543 | 604.1 |
| E4 tsvector | [rrf-30](2026-10-29-real/e4-tsvector/rrf-30.json) | 1.0000 | 0.9815 | 0.9619 | 566.7 |
| E4 tsvector | [rerank-20](2026-10-29-real/e4-tsvector/rerank-20.json) | 1.0000 | 1.0000 | 0.9907 | 1178.7 |
| E4 tsvector | [rerank-30](2026-10-29-real/e4-tsvector/rerank-30.json) | 1.0000 | 1.0000 | 0.9907 | 1059.2 |
| E4 tsvector | [rerank-50](2026-10-29-real/e4-tsvector/rerank-50.json) | 1.0000 | 1.0000 | 0.9907 | 1110.0 |
| E3 structured | [lexical-30](2026-10-29-real/e3-v2/structured/lexical-30.json) | 0.9815 | 0.9815 | 0.9414 | 506.7 |
| E3 structured | [vector-30](2026-10-29-real/e3-v2/structured/vector-30.json) | 1.0000 | 1.0000 | 0.9636 | 593.4 |
| E3 structured | [rrf-30](2026-10-29-real/e3-v2/structured/rrf-30.json) | 1.0000 | 0.9815 | 0.9630 | 597.2 |
| E3 structured | [rerank-20](2026-10-29-real/e3-v2/structured/rerank-20.json) | 1.0000 | 1.0000 | 0.9907 | 1259.1 |
| E3 structured | [rerank-30](2026-10-29-real/e3-v2/structured/rerank-30.json) | 1.0000 | 1.0000 | 0.9907 | 1211.4 |
| E3 structured | [rerank-50](2026-10-29-real/e3-v2/structured/rerank-50.json) | 1.0000 | 1.0000 | 0.9907 | 1209.2 |
| E3 structured-no-heading | [lexical-30](2026-10-29-real/e3-v2/structured-no-heading/lexical-30.json) | 0.9815 | 0.9444 | 0.9090 | 575.9 |
| E3 structured-no-heading | [vector-30](2026-10-29-real/e3-v2/structured-no-heading/vector-30.json) | 1.0000 | 1.0000 | 0.9759 | 487.4 |
| E3 structured-no-heading | [rrf-30](2026-10-29-real/e3-v2/structured-no-heading/rrf-30.json) | 1.0000 | 0.9630 | 0.9568 | 469.3 |
| E3 structured-no-heading | [rerank-20](2026-10-29-real/e3-v2/structured-no-heading/rerank-20.json) | 1.0000 | 1.0000 | 0.9907 | 1201.0 |
| E3 structured-no-heading | [rerank-30](2026-10-29-real/e3-v2/structured-no-heading/rerank-30.json) | 1.0000 | 1.0000 | 0.9907 | 1243.5 |
| E3 structured-no-heading | [rerank-50](2026-10-29-real/e3-v2/structured-no-heading/rerank-50.json) | 1.0000 | 1.0000 | 0.9907 | 1194.0 |
| E3 fixed | [lexical-30](2026-10-29-real/e3-v2/fixed/lexical-30.json) | 0.9815 | 0.9444 | 0.9012 | 457.5 |
| E3 fixed | [vector-30](2026-10-29-real/e3-v2/fixed/vector-30.json) | 1.0000 | 0.9815 | 0.9753 | 493.9 |
| E3 fixed | [rrf-30](2026-10-29-real/e3-v2/fixed/rrf-30.json) | 1.0000 | 0.9630 | 0.9383 | 558.0 |
| E3 fixed | [rerank-20](2026-10-29-real/e3-v2/fixed/rerank-20.json) | 1.0000 | 1.0000 | 1.0000 | 1167.2 |
| E3 fixed | [rerank-30](2026-10-29-real/e3-v2/fixed/rerank-30.json) | 1.0000 | 1.0000 | 1.0000 | 1261.9 |
| E3 fixed | [rerank-50](2026-10-29-real/e3-v2/fixed/rerank-50.json) | 1.0000 | 1.0000 | 1.0000 | 1330.1 |

E1/E2：整文档夹具只有 12 个可见块，因此 Top-N=20/30/50 实际候选数相同，不能据此选出最佳 Top-N。重排改善该集合的排序，但总耗时 p95 超过 800 ms，性能目标未通过。无答案题不计入表中检索指标；没有运行生成问答，不能据此验收拒答或引用。

E3：结构化分块共 29 块（含受限块），固定分块共 26 块；使用生产解析/规整/分块逻辑处理可信合成文本，再直接写入评测夹具，不等于真实入库全链路验收。fixed-heading 本地只完成两组，已迁至云端重新跑全六组。跨机器耗时不直接比较。

E4：仅替换词法召回为 gse 中文分词加 tsvector，其他环节相同。词法 MRR 从 0.9599 降至 0.8935，下降约 6.64 个百分点，超过两百分点回归门槛；重排后的该小集合指标相同。此实现仅为显式评测入口，索引核验仍依赖 pg_search；不能宣称完整兼容驱动部署或 V03 契约通过。

本地环境已按用户指示清理，报告保留。E3 最后一组与 V08 后续证据见[云端迁移记录](2026-10-29-cloud-migration.md)。T01/T04/T05/T13 和 M0/M1 仍须按各自退出条件验收。
