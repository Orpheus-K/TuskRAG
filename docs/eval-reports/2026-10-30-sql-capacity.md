# SQL 召回与云端容量复测（2026-10-30）

关联 R06、T04、T05、V08、M0/M1。容量数据仅在旧云端实例的独立 PostgreSQL 容器生成；本地只编译测试二进制和运行静态检查。

## 实现与正确性

提交 `14a07bc` 使用 BM25 v2：将知识库成员权限、继承 ACL、当前版本、版本状态和文档过滤先转换为授权版本集合，使用 `paradedb.term_set` 在索引内过滤。`kb_access` 是从 `access_tags` 自动生成的布尔列，仅表达当前单知识库主体；不能替代未来通用 ACL。RLS、自定义计划、向量预过滤及出库复核保留。

原查询的词法分支扫描大量块后排序；中间版本 `605c25e` 对每个授权文档执行 LATERAL Top-K，20 万块 p95 从 833.479 ms 降到 602.002 ms，但百万块仍出现数十秒查询，且有多文档重复扫描问题，因此未作为最终方案。该百万块中间版本采样被主动中止，不是完整容量验收。

曾尝试直接用相关 EXISTS 过滤 BM25 Top-K，但 pg_search 0.22.6 在该计划中先截取候选，导致被拒绝的高排名行遮住可见行。回归测试发现后弃用。现有反例覆盖超过 50 条受限文档、禁用块及访问标签不匹配的块；并测试跨租户连接复用和并发撤权。

云端执行 `TestLexicalAdvancesPastRejectedCandidates`、`TestHybridRetrievalFromPublishedVersion`、`TestSyntheticZHEvaluation` 通过。64 题、四种模式的 Recall@20、Hit@5、MRR@10、nDCG@10 与上一版完全一致，越权证据均为 0。替身模型不证明真实模型质量；真实 E1—E4 记录见 [10 月 29 日基线](2026-10-29-real-baseline.md)。原始对照保存在 [本轮目录](2026-10-30-cloud/)。新增访问标签反例纳入远端 CI。

## 容量结果

| 方案 | 块数 | p50 / p95（ms） | PG 数据库（B） | PG 采样峰值（MiB） |
|---|---:|---:|---:|---:|
| 10 月 29 日原查询 | 200,000 | 768.567 / 833.479 | 见原始报告 | 1111.04 |
| LATERAL 中间版本 | 200,000 | 558.502 / 602.002 | 1,207,916,211 | 1170.43 |
| BM25 v2，并行查询 | 200,000 | 296.779 / 354.776 | 1,228,879,539 | 882.90 |
| BM25 v2，并行查询 | 1,000,000 | 442.393 / 501.012 | 6,226,065,075 | 1160.19 |
| **最终：BM25 v2 + 词典缓存复用** | **200,000** | **51.926 / 84.978** | **1,231,886,003** | **1179.65** |
| **最终：BM25 v2 + 词典缓存复用** | **1,000,000** | **224.084 / 271.923** | **6,307,542,707** | **1220.61** |

最终两轮均完成 100 次查询、越权数 0；20 万块 p95 较原查询下降约 90%，达到该合成负载下的 100 ms SQL 预算。百万块 p95 较 v2 并行版下降约 46%，仍超过 100 ms。百万块数据库约 5.874 GiB；该主机低于设计中的 standard 4C16G 档，不能由此认定标准档达标或调整目标。

最终 20 万 / 100 万块入库耗时 18.976 / 152.705 秒，建索引耗时 66.315 / 440.711 秒。HNSW 大小分别为 265,404,416 / 1,568,309,248 B，BM25 主关系大小分别为 9,584,640 / 34,185,216 B；完整关系统计在对应 `v08-200k.json` / `v08-1000k.json`。两轮使用独立数据库、串行执行；没有同时运行诊断查询。

最终测试日志为 PASS，脚本完成标志已生成。已确认随机测试数据库数量为 0、容量测试进程已退出、磁盘恢复到约 15 GiB 可用，并恢复本轮 auto_explain 诊断设置。随后 SSH 曾短暂在握手阶段被远端关闭；连接恢复后已停止并核验专用 PG 容器状态为 `exited`。评测报告已下载保留，未修改其他应用容器。

## 测量边界

百万块词法分支诊断发现并行计划的固定开销明显：同一连接第二次查询，并行 2 worker 执行 680.820 ms，关闭并行后第二次执行 182.233 ms；两者都为 TopKScan、50 次 heap fetch。[原始计划](2026-10-30-cloud/lexical-diagnostic.txt)采集时后台仍在建 HNSW，不能充当完整召回容量结果。该现象与 [上游 Jieba 词典重复加载问题](https://github.com/paradedb/paradedb/issues/4840)一致。只在原生召回事务内设置 `max_parallel_workers_per_gather=0`，复用池连接的分词器缓存，不改服务器全局设置或兼容驱动。

- 主机为 2 CPU、3399 MiB RAM；测试 PG 容器限额 1536 MiB，Go 测试进程 `GOMEMLIMIT=512MiB`。PG 17.9、pg_search 0.22.6、pgvector 0.8.1。
- `TestCapacity200K` 通过环境变量选择 200000 或 1000000 块；1024 维 halfvec、HNSW m=16。5 次预热后测量 100 次，包含召回、出库复核及审计，不包含嵌入和重排网络调用。
- 一个合成文档，256 种归一化向量反复使用；不等价于百万种独立向量、多文档、多租户或并发负载。不能从该结果推出真实客户负载 SLA。
- Docker 内存每 10 秒采样，峰值只是采样峰值，不是精确峰值。关系大小不能将表总大小与其索引大小再次相加。
- SQL 阶段预算为 100 ms；完整检索预算为 800 ms。二者不可混用，容量测试不证明完整检索达标。

## 升级

迁移 19 会重写块表并移除旧 BM25 v1，需维护窗口和重新运行 `provision-indexes`；操作顺序见 [运维命令](../09-部署运维与可观测.md#八运维命令)。缺少 v2 索引时明确失败，不自动降级或返回空结果。

## 复现

使用测试实例自身的三个环回 PostgreSQL URL：`TUSKRAG_TEST_ADMIN_URL`、`TUSKRAG_TEST_APP_URL`、`TUSKRAG_TEST_MIGRATOR_URL`。只允许隔离的 `tuskrag_test_` 数据库，测试会创建随机数据库并在结束时删除。运行目录为源码的 `services/core/internal/ingest`，测试二进制由 `go test -c ./internal/ingest` 构建；本轮容量测试不调用外部模型，不需要 API Key。

```sh
GOMEMLIMIT=512MiB TUSKRAG_EVAL_REPORT_DIR=/absolute/new/report-directory \
  ./ingest.test -test.run '^(TestLexicalAdvancesPastRejectedCandidates|TestHybridRetrievalFromPublishedVersion|TestSyntheticZHEvaluation)$' -test.v -test.timeout 15m
GOMEMLIMIT=512MiB TUSKRAG_CAPACITY_EVAL=1000000 \
  TUSKRAG_EVAL_REPORT_DIR=/absolute/new/million-directory \
  ./ingest.test -test.run '^TestCapacity200K$' -test.v -test.timeout 45m
```

20 万块将容量变量改为 `200000`。先完成正确性回归，再串行运行两种规模；每次使用新的报告目录，避免混入旧结果。环境、提交和二进制 SHA-256 见 [environment.json](2026-10-30-cloud/environment.json)。

## 里程碑边界

代码提交 `5c9b17f011cc66c959182a9e5f743ad83e5bab8d` 的 [GitHub Actions 41034213223](https://github.com/Orpheus-K/TuskRAG/actions/runs/41034213223) 全绿：静态检查、生成契约/sqlc 一致性、真实 PG 集成、Go 竞态、64 题评测、Linux 解析沙箱和生产构建浏览器测试全部通过。步骤结果存于 [ci.json](2026-10-30-cloud/ci.json)。随后仅归档证据与更新文档的提交不重新运行产品测试。

本轮补充 JSONL 评测集导入、CI 隔离修复、数据库运行依赖清单和 V08 容量证据。M0/M1 尚不能整体关闭：T13 后台重建/原子切换、知识库持久化检索参数、评测页面/期望答案编辑、Windows 完整解析隔离仍未实现；pg_search 法务意见和全部二进制传递依赖许可盘点未完成。真实模型全链路延迟及兼容词法回归仍按 10 月 29 日报告保留偏差，不能用本轮无模型容量结果覆盖。
