# 评测检索修订一致性

2026-11-06 工作树；R01/R11/R20、T12、M1。

核对后台接入时发现：固定题集修订不固定逐题 Search 读取的检索配置，运行期间保存配置会使一份报告混用修订。本轮先修复该前置缺口；eval_run 创建、快照与 worker 接入仍未交付。

`runQuestions` 记录首个成功结果的检索修订，后续不同修订返回 `ErrRetrievalRevisionChanged`；修订 0 是合法未配置状态。每题结果与 trace 必须同修订且非负，否则显式失败。原 Dataset 和固定题集共用此检查；RunSet 失败沿用清空逐题证据、汇总和耗时的行为。

该检查不冻结文档版本、嵌入空间或模型端权重，也不取代未来任务创建时的完整配置快照。未改变召回、融合、重排及评分算法；没有数据库或 HTTP 契约变更。

## 验证

- 修复前新增回归测试复现四项错误：0→1、5→6、trace 不一致、负修订均被错误接受并执行完全部四题。
- 修复后六项场景通过：上述错误分别在第二/第一题停止；正常 0、正常 5 完成四题并通过 ValidateReport。失败题集没有部分结果。
- `go test ./internal/eval ./cmd/tuskrag`、Linux `go test -race -count=1 ./internal/eval ./cmd/tuskrag`、相关 vet 与 golangci-lint 通过（0 issues）。

- 真实 PostgreSQL 17/pgvector/pg_search、确定性模型替身：Linux race `TestSyntheticZHEvaluation` 通过（45.76s）。synthetic-zh 64 题 × 四模式 × 三路径，共 768 次查询；原入口、保存配置继承、固定题集的逐题证据与指标相等，越权数均为 0。
- [12 份报告](2026-11-06-revision-run/)与[上一轮](2026-11-06-set-run/)逐份比较 summary 完全一致，Recall@20、Hit@5、MRR@10、nDCG@10 差值均为 0；12 份均通过现有 CLI validate。正常评测无需放宽断言。
- 专用容器 `tuskrag-revision-run-pg-20261106` 回环端口 56446，测试自建并删除随机 publication 库；结束查询剩余数为 0，容器及匿名卷已删除。未使用客户语料或真实模型。
- 无前端或接口改动，未运行浏览器；配置变化故障由领域替身测试验证，真实 PG 本轮验证稳定配置三路径回归，未新增数据库竞态测试。
