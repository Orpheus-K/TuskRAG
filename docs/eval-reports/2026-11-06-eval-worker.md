# 评测 worker 与租约结果提交

2026-11-06 工作树；R11/R13/R20、T08/T09/T12/T14、M1。

## 实现

worker 分发增加 eval_run，复用已有认领、续租、取消通知、重试与过期回收。handler 在有效租约短事务中读取与 job/tenant 精确绑定的不可变运行记录，按创建者检查当前权限及配置；随后在事务外调用 RunSet，单轮限制 30 分钟。执行错误按既有失败机制分类，撤权使用 permission_revoked，失败不保存部分报告。

迁移 24 新建强制 RLS 的不可变 eval_run_result，每个运行最多一份成功报告。Prepare 返回的写入闭包只能由同一 queue、同一租约的 Complete 事务执行，普通 WithLease 检查点不能写入。完成前重查创建者、知识库状态、完整允许文档范围、固定题集目标、配置绑定及全部返回证据；结果、完成审计与 job=succeeded 原子提交。取消或旧世代的迟到结果被租约检查拒绝。

领域 GetResult 按当前读取者复核目标和实际证据，验证报告摘要，来源固定 executed；外部导入依然只写 eval_report。HTTP 创建/状态/结果入口及控制台运行待下一步接入。本轮不新增服务或模型常驻进程。

## 验证

- 本机 eval/kb/ingest/jobs/CLI 单测与相关 vet、golangci-lint 通过；Linux eval/jobs/CLI race 通过，核心 Linux 静态构建通过。
- 领域失败分类覆盖权限、配置变化、超时、出域拒绝、错误模型响应、429、400、内部错误；取消和失去租约保持原错误。

- 真实 PostgreSQL 17、Linux race `^TestEval(Worker|Run)` 共 8 项顶层测试通过：4 项运行存储回归、4 项 worker/结果测试（含 5 种迟到/失效场景及正常/取消运行）。验证 WithLease 不能伪装 Complete、同租约重复完成拒绝、取消/重领后旧结果无写入、成员/配置/返回块失效拒绝并记录终态失败、审计失败回滚结果及 succeeded、结果不可修改且跨租户不可见。返回块失效场景特意保留另一个题集目标见证，证明实际返回证据被独立复核。
- 真实 jobs.Worker 循环成功完成评测；取消测试在停止 worker 前等待上游搜索收到取消，证明取消通知生效，未产生结果。
- 真实 PG db 全包 race 通过，包含 1→24 新库迁移、14→24 升级、RLS 和就绪检查。生产 worker 分发变更由 CLI 单测/构建验证；本轮未启动含解析沙箱的完整生产 worker 进程，实际循环使用同一 jobs.Worker 与 RunHandler。
- `TestSyntheticZHEvaluation` race 通过（57.89s）：64 题 × lexical/vector/rrf/rerank × 原入口/配置继承/固定题集/队列执行，共 1024 次查询。四路径每题融合与重排证据、汇总指标完全相同，越权 0；队列路径通过真实 RunStore、claim、handler、Complete 和 GetResult。
- [16 份完整报告](2026-11-06-worker-run/)全部通过 CLI validate，与[上轮](2026-11-06-snapshot-run/)逐份 summary 比较相同，四项指标差值均为 0（job 报告对应上轮 set 报告）。模型仅使用确定性替身。
- 独立容器 `tuskrag-eval-worker-pg-20261106`，回环端口 56446；结束时 eval_run_result/eval_run/job/tenant 行数及随机 publication 库数均为 0，专用容器及匿名卷已删除。`git diff --check` 通过。

未修改 HTTP 契约或页面，未运行浏览器。模型端权重与提供方连接配置仍不由快照冻结；合成替身评测不代表真实模型或大规模召回质量。T12 的生成引用/拒答评分与完整 M1 仍未完成。
