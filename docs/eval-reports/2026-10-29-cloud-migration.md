# 本轮评测迁移到旧云端实例

2026-10-29，按用户指示将后续测试迁移至 `legacy-host.invalid`（SSH 别名 `legacy-test-host`）。已验证 SSH 登录与 sudo；实例为 2 核、约 3.3 GiB 内存，初次检查根盘可用约 18 GiB。

## 本地收尾

- 已终止 `m1-capacity-run.sh` 等待任务。20 万块容量数据尚未开始写入。
- 已终止尚在运行的 E3 最后一组，删除本轮两个 PostgreSQL 容器及其匿名卷；停止本轮专用 Docker daemon，清理其数据目录。
- 已卸载并删除本轮 `capacity-20261029.ext4`（4 GiB），删除仓库外本轮模型凭据文件。
- 又核验并删除未挂载的 `tuskrag-docker-test.ext4`（6 GiB）：其中仅有本项目 `tuskrag-kb-image` / `tuskrag-postgres:kb-test` 临时容器及匿名卷；挂载与进程检查均为空。两块临时盘共 10 GiB，清理后 E 盘可用 10.50 GiB。
- `2026-10-29-real/` 下所有评测结果保留：32 组报告均为 `passed`、各 64 题。E1/E2、E4、E3 的 structured、structured-no-heading、fixed 各六组已完成；fixed-heading 的 lexical、vector 两组完成，其余四组中断后未输出成功报告。
- 中断前的 `structured/` 目录仅有 tokenizer 探测，首次分块夹具因空标题路径违反非空约束失败；修正后成功结果位于 `e3-v2/structured/`。不得将首次失败目录当成完成的实验。

## 云端布局与范围

专用目录 `<deployment-path>`；源码归档、测试可执行文件、报告和模型临时凭据分开保存。凭据不进入源码归档、Git 或报告。测试数据库只监听回环地址，使用 `tuskrag_test_` 数据库名前缀，并由测试自动新建和清理独立数据库。

后续在云端重新运行 E3 fixed-heading 全六组，以保持这一组内部环境一致，然后运行 V08 200000 块并采样 PostgreSQL 容器内存。云端与本地硬件和网络不同，性能数据分开解释，不将跨机器耗时直接当作消融收益。已有旧应用容器未作为本轮测试数据清理。

迁移记录不代表云端评测或 M0/M1 验收已完成；运行结果以随后生成的报告为准。

测试二进制 SHA-256 为 `327d221fea941a81d21e7b028f20f94e64211660f66e8a552e4ad2f0f13540c0`，上传前后匹配，云端已验证可执行。`start.sh` 已在云端后台启动，等待固定摘要的数据库镜像构建成功后建立 1536 MiB 内存上限、仅监听 `127.0.0.1:56444` 的独立容器，再顺序运行 E3 与 V08。运行日志为专用目录下 `run.log`；成功写入 `completed`，失败写入 `failed`。
