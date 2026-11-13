# 停机修复与 T13 生产闭环

2026-11-07 工作树；R05/R16/R20、T13、M1。

## 修复与定向验证

完整进程测试先前在 SIGTERM 阶段暴露两条失败路径：队列认领返回包装的 `context.Canceled`；查询取消关闭连接后，回滚再追加连接关闭错误。

- `cmd/tuskrag` 沿单错误包装链识别正常取消，仅在相应停机上下文中正常退出。包含清理错误的 joined error 仍上报，不用宽泛的 `errors.Is` 吞掉其中的其他失败。
- `jobs.rollback` 在回滚前确认“原错误包含取消且连接已经关闭”。该事务由 PostgreSQL 断连撤销，仍调用 Rollback 释放池连接，但不追加重复的连接关闭错误；其他回滚错误保留。
- 新回归验证取消/非取消两种关闭连接路径、事务写入未提交、池连接已释放。pgx/puddle 的关闭连接销毁是异步的，测试采用一秒有界条件等待，保留零占用断言。

先执行定向门禁：7 项启动层测试和 17 项真实 PG 队列竞态测试全部通过；另有两个子进程入口由父测试调用，在顶层跳过。日志：[启动层](assets/rebuild-production-20261107/shutdown-main.log)、[队列](assets/rebuild-production-20261107/jobs-race.log)。

## 生产链路结果

`TestWorkerProcess` 使用生产 `run()` 启动 worker/all 子进程，保留原有上传、隔离解析、强杀恢复、HTTP 登录、检索、版本替换和清理断言。末尾增加真实 Cookie/CSRF 下的重建流程：

1. 创建新目标空间并准备 HNSW，通过 POST 创建重建；同键重试返回同一任务。
2. 生产 worker 从 reembed 队列认领，按生产批次配置调用 HTTP 替身，GET 轮询到 succeeded。
3. 校验全部 16 个当前块都有目标向量、活动空间切换、配置修订 +1、pending 清空及终态时间。
4. 创建下一目标空间，在模型请求阻塞时通过 POST 取消；GET 返回 cancelled，活动绑定不变，pending 清空，取消目标向量数为 0。
5. SIGTERM 后进程正常退出。

Linux race 全流程通过，耗时 **74.28 秒**，见 [生产日志](assets/rebuild-production-20261107/process-race.log)。与此前 [65 文档/195 组检索一致性](2026-11-07-rebuild-multidocument.md)、[切换事务/迟到提交](2026-11-07-rebuild-switch.md)、[页面](2026-11-07-rebuild-ui.md) 证据共同覆盖 T13 的后台重建、旧空间持续使用、原子切换及操作入口。M1 范围的 T13 流程过关，不据此宣布整个 M1 或生产发布完成。

本轮未改变解析、分块、检索或模型算法，未重跑真实模型语义质量评测；使用 HTTP 替身验证链路。`go vet ./cmd/tuskrag ./internal/jobs`、相关 golangci-lint（0 issues）、Linux 构建及 diff 空白检查通过。

## 同机环境与提速落地

测试二进制、解析器、模型替身和独立 PG 17/迁移 25 均在同一 Linux 主机，数据库不再经过 WSL/SSH 隧道访问。复用已编译 core 二进制，仅因修改异步释放断言重编译 jobs 测试。

环境准备中发现两项差异：发行版旧 Bubblewrap 缺少必需参数；从另一发行版复制的二进制要求更高 glibc。最终在任务目录构建 Ubuntu 0.9.0 上游源码并应用 `0.9.0-1ubuntu0.3` 补丁，具备 FD 绑定功能；测试二进制设为 0755，允许沙箱执行用户运行。未更改系统安装，未移除隔离参数。7 个原生解析子场景通过，见 [隔离日志](assets/rebuild-production-20261107/native-race.log)；版本及二进制摘要见 [环境记录](assets/rebuild-production-20261107/environment.txt)。

环境未就绪时有两次 42 秒左右的完整运行未抵达正文模型调用，其中一次由临时脚本测试名替换失误重复触发；随后改用原生解析定向探测定位 FD 参数和执行权限，环境通过后完整链路成功。74.28 秒是成功运行的实测耗时，不是不同场景间的性能提升百分比。

新增 [同机运行脚本](../../scripts/test-process-linux.sh) 及 [使用说明](../../tests/process/README.md)：分 shutdown/native/process 三个入口，检查编译产物包含目标测试、隔离参数、版本与夹具路径；process 先执行原生隔离探测，再执行完整链路。新脚本 shutdown/native 两个入口实际通过，bash 语法检查通过；完整进程已由相同二进制/参数运行通过，未为检查脚本包装再重复整条链路。

清理结果：专用子数据库、tenant/job/rebuild 均为 0，无遗留测试 cgroup。旧 WSL SSH 隧道及临时密钥副本已清理；同机运行时保留，专用 PG 容器停止为 exited，后续可以直接复用。
