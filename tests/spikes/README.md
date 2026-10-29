# M0 Spike 证据

## S1：中文分词与过滤计划（初探）

- 环境：独立 PostgreSQL 17.9，`pg_search` 0.22.6，`vector` 0.8.1；使用 `s1_pg_search.sql` 的 6 条合成记录，执行于名称以 `tuskrag_test_` 开头的测试库，事务结束后回滚。
- Jieba 对“差旅住宿标准第三条”输出 `差旅、住宿、标准、第三、三条、第三条`。该样例不代表中文分词质量评测。
- 带租户、知识库、当前版本和访问标签过滤的 BM25 查询仅返回预期的可见记录；样例中越权记录数为 0。向量查询也只返回两条可见记录。这不替代完整权限测试或召回评测。
- 在强制关闭顺序扫描的计划中，BM25 自定义扫描将租户、知识库和当前版本表达为索引 term；`access_tags && ...` 表现为扫描内 `heap_filter`，需要访问堆数据，并非访问标签的索引原生下推。向量 HNSW 计划也在索引扫描后应用租户和标签过滤。
- 单条 SQL 可同时出现 BM25 与 HNSW 两路计划；样本过小，不足以证明大规模计划选择、过滤召回率或延迟。S1 尚未完成。
- 后续隔离测试发现，`pg_search` 0.22.6 在通用预编译计划跨租户复用时可返回其他租户的记录；同一连接上应用强制自定义计划后，12 次交替查询越权数为 0。独立对照、运行方式及限制见 [索引对照](../../docs/eval-reports/2026-10-28-index-provision.md)。

## S2：过滤后的 HNSW 迭代扫描（单次合成实验）

- 环境：独立 PostgreSQL 17.9、pgvector 0.8.1；容器限制 1 CPU、768 MiB；`s2_filtered_hnsw.sql` 设置 256 MiB 维护内存、串行构建、HNSW `m=16`、`ef_construction=64`、`ef_search=40` 和 `max_scan_tuples=20000`。
- 使用 `-v rows=100000` 生成 100,000 个 1024 维 halfvec。向量仅有前 4 个维度变化，其余维度固定；查询为单个合成向量，过滤包含租户、知识库、当前版本与访问标签。顺序扫描得到精确 Top 20 作为对照，强制计划选择 HNSW。
- 关闭迭代扫描时返回 1 条，命中精确 Top 20 中的 1 条；设置 `relaxed_order` 后返回 20 条，命中精确 Top 20 中的 20 条；样例越权数为 0。
- 首次使用默认并行维护进程建索引时，容器默认共享内存不足导致失败；关闭并行维护后构建成功。该单次样例不能代表真实数据上的召回、p95 延迟、容量或安全验收。

## S3：River 迟到提交

- 环境：隔离 PostgreSQL 17.9、River 0.49.0（仅 Spike 依赖）；[实验程序](river/main.go) 限制数据库为本机 `tuskrag_test_`，创建随机 schema 并在退出时删除。
- 两个 River worker 处理同一任务：第一轮开始后，通过 SQL 将任务重新设为可领取以模拟救援；等待第二轮进入 `running`，再让第一轮在同一事务内写入副作用并调用 `JobCompleteTx`。
- 结果：第一轮提交成功，数据库中任务已为 `attempt=2, state=completed`，副作用 1 条。标准 `JobCompleteTx` 未阻止旧一轮提交，因此不满足 V02。实验模拟了救援状态转换，未覆盖 River 全部功能或自定义围栏扩展。
- 运行：在 `tests/spikes/river` 中对隔离测试库设置 `TUSKRAG_SPIKE_DATABASE_URL`，执行 `go run .`。自研队列存储已有真实数据库竞争与迟到提交测试；完整 worker 的 V02 仍是后续门禁。

## S5：bge-m3 token 计数（候选库不通过）

- 参考：合成的 24 个中英文、Unicode 与空白样例，使用 Hugging Face `tokenizers` 0.22.1 读取 [BAAI/bge-m3 的 tokenizer.json](https://huggingface.co/BAAI/bge-m3/blob/5617a9f61b028005a4858fdac845db406aefb181/tokenizer.json)，以 `add_special_tokens=True` 得到的 token ID，见 [reference.json](tokenizer/reference.json)。词表固定到 revision `5617a9f61b028005a4858fdac845db406aefb181`，SHA-256 为 `21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08`。
- 候选：纯 Go `github.com/tggo/goSentencePiece` v1.1.0；同样加载该文件并包含特殊 token。24 个样例中只有 7 个 token ID 完全相同，11 个样例的 token 数量不同；部分数量相同的样例仍有不同 ID。该候选不用于生产计数。
- 复现：下载上述 revision 的原始 `tokenizer.json`，在 [tokenizer](tokenizer) 目录执行 `go run . <tokenizer.json 的路径> reference.json`。程序校验文件 SHA-256 并逐样例输出参考数量、候选数量与 ID 是否相等。
- 第二个候选：纯 Go `github.com/sugarme/tokenizer` v0.3.0。24 个样例中 15 个 token ID 完全相同，6 个 token 数量不同，另 1 个数量相同但 ID 不同；两个包含组合字符或空白的样例在规范化时发生 panic。该候选同样不用于生产计数。复现：在 [tokenizer-sugarme](tokenizer-sugarme) 目录执行 `go run . <tokenizer.json 的路径> ../tokenizer/reference.json`；实验程序仅为逐样例记录结果而捕获 panic。
- 对同一 revision 的 [sentencepiece.bpe.model](https://huggingface.co/BAAI/bge-m3/blob/5617a9f61b028005a4858fdac845db406aefb181/sentencepiece.bpe.model)（SHA-256 `cfc8146abe2a0488e9e2a0c56de7952f7c11ab059eca145a0a727afce0db2865`）再次使用 `goSentencePiece`，按 [XLM-R token 编号规则](https://github.com/huggingface/transformers/blob/v4.57.1/src/transformers/models/xlm_roberta/tokenization_xlm_roberta.py) 映射并添加首尾特殊 token：24 个样例中 21 个 ID 完全一致，3 个数量不一致。实验性处理边界空白和特殊标记后，小样例为 24/24，但在固定种子 `20261028` 的 5,000 条合成 Unicode/特殊标记样例中仅 2,583 条 ID 一致；2,417 条不一致，其中 2,029 条数量不同。该适配仅为实验，不用于生产；合成随机分布不代表真实文档分布。
- 复现上述实验：在 [tokenizer-model](tokenizer-model) 目录使用 Python 3.13 和 `tokenizers==0.22.1`，运行 `python generate-reference.py <tokenizer.json> <random-reference.json>`；生成文件 SHA-256 为 `45b76e67ca05a8dd0bb1c711a141c8f3f0ecbaf981d298f77f9f73b98895e226`。分别运行 `go run . <sentencepiece.bpe.model> ../tokenizer/reference.json raw`、`go run . <sentencepiece.bpe.model> ../tokenizer/reference.json approximate` 以及 `go run . <sentencepiece.bpe.model> <random-reference.json> approximate`。
- 第三个候选：纯 Go `github.com/rostamlabs/rembed/tokenizer/sentencepiece` v0.3.0，对同一 `.model` 直接计数并保留首尾特殊 token；24 个样例中 21 个 ID 一致、3 个数量不一致，5,000 条合成样例中 229 条 ID 一致、4,771 条不一致（其中 4,719 条数量不同）。上游明确记录了与 Hugging Face fast tokenizer 的部分规范化差异；该候选不用于生产。复现：在 [tokenizer-rembed](tokenizer-rembed) 目录对上述 `.model` 和两份参考文件分别运行 `go run . <sentencepiece.bpe.model> <reference.json>`；只调用 tokenizer 子包，不加载推理模型。
- 紧凑的 BGE-M3 固定词表计数器已作为独立组件加入；对上述 5,000 条及 Unicode 16 断字、基本多文种平面字符和全角特殊标记等合成输入进行逐 ID 对照，173,198 条一致，另两条含 NUL 的输入按组件约束拒绝。它使用固定的 Unicode 16 切分和双精度 Unigram 评分，未接入 worker，实际模型服务的计数仍待验证。旧候选在一次 Windows 试验中加载后存活堆约 1.83 GB，紧凑实现同机单次约增加 18.1 MB；评测方法、限制与复现见 [计数报告](../docs/eval-reports/2026-10-28-bgem3-counter.md)。
- 这只验证本地参考 tokenizer，不代表与实际嵌入服务的用量一致；其他 Go 实现、真实服务端计数和 Go/pg_search 中文切词一致性仍待验证。S5 尚未完成。

## S6：解析进程隔离

- 在 WSL Linux 上使用 `unshare --user --map-root-user --net` 运行解析器 pytest（`UV_OFFLINE=1`），88 个测试通过；非特权账户创建相同网络命名空间也通过。
- 在 WSL Linux 使用 bubblewrap 0.9.0、cgroup v2 和 `prlimit`，以 root 和委派 cgroup 内的非特权用户分别运行 Go 解析沙箱测试。测试覆盖网络与宿主文件隔离、禁止嵌套用户命名空间、输入和运行时只读、环境变量隔离、退出码映射、输出/诊断大小、进程树内存与 CPU、墙钟超时及取消后子进程消失；另使用 Python 3.13 真实解析器验证版本、CSV/DOCX/XLSX 输出与加密及不支持内容的退出码。
- 进程树使用原子 `CLONE_INTO_CGROUP` 启动；CPU 用量每 100 ms 检查，因此存在检查间隔内的超出量。
- 容器实验：WSL2 Linux 6.18.54.1、Docker Engine 28.5.2、bubblewrap 0.9.0、Debian 13 测试镜像；默认容器的 cgroup 只读，真实解析显式失败。仅委派可写 cgroup 后，默认 seccomp 使 `clone3` 返回 `ENOSYS`，仍显式失败。使用专用 cgroup、实验性 seccomp 和 `systempaths=unconfined` 后，容器内以 UID 65534、有效 capabilities 为 0 运行的隔离、进程树限额、超时/取消、输出校验和真实 CSV/DOCX/XLSX 解析测试通过；一次实测宿主 `user.max_user_namespaces` 前后均为 31296。实验初始进程仅保留 `SETUID`/`SETGID` 用于降权，测试进程不保留 capabilities。
- [容器实验脚本](s6-container.sh)仅用于隔离环境，不是部署配置。它使用 `--network none`、只读根文件系统、宿主 cgroup 命名空间和专用 cgroup；放宽 Docker 系统路径会扩大容器暴露面，尚需确定更窄的生产方案与安全评估。测试仍未覆盖所有目标格式的恶意样本；Windows 当前显式拒绝运行，Job Object 与网络/文件系统隔离尚未实现。沙箱尚未接入 worker，S6 未完成。

复现实验需隔离 Docker daemon、可委派 cgroup v2、Node、Go、bubblewrap 和自包含的 Python 3.13 解析运行目录（包含 `python/`、`src/` 和 `tuskrag-parse`）。固定 Docker 28.5.2 原始 profile 后生成实验 profile：

```sh
curl -fsSLo /tmp/docker-default-seccomp.json https://raw.githubusercontent.com/moby/moby/v28.5.2/vendor/github.com/moby/profiles/seccomp/default.json
node tests/spikes/s6-seccomp.mjs /tmp/docker-default-seccomp.json > /tmp/tuskrag-s6-seccomp.json
(cd services/core && CGO_ENABLED=0 go test -c -o /tmp/tuskrag-parser.test ./internal/parser)
```

生成器校验原始 profile SHA-256 `01536f1d1df938ae611eba20d6349e0de7a99b6ecdee1549427a0b01b8301e28`；移除默认 `clone3` 的 ENOSYS 规则，并允许 `clone3`、`clone`、`unshare`、`mount`、`umount2`、`pivot_root`。在 Linux root 环境设置 `TUSKRAG_S6_DOCKER`、`TUSKRAG_S6_DOCKER_HOST`、`TUSKRAG_S6_IMAGE`、`TUSKRAG_S6_TEST_BINARY`、`TUSKRAG_S6_BWRAP`、`TUSKRAG_S6_RUNTIME` 和 `TUSKRAG_S6_SECCOMP`，然后运行 `sh tests/spikes/s6-container.sh`。不应将该实验 profile 和系统路径设置直接用于生产。
