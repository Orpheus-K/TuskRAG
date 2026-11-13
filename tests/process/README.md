# Linux 同机进程验收

测试进程、HTTP 模型替身与测试 PostgreSQL 放在同一 Linux 主机。PG 只监听回环地址；使用独立 `tuskrag_test_` 数据库与 app/admin/migrator 三种角色。运行时和测试容器可复用，各测试自行建立、清理夹具或临时数据库。

在源码变更后编译一次，未修改对应代码时直接复用二进制：

```sh
cd services/core
go test -race -c -o /absolute/artifacts/jobs.test ./internal/jobs
go test -race -c -o /absolute/artifacts/core.test ./cmd/tuskrag
```

二进制可在兼容 Linux 环境编译后传输；执行时必须与 PG 同机。保留二进制摘要和对应工作树信息，避免运行旧产物。跨用户运行时，二进制须具有沙箱执行用户需要的权限，例如受控测试目录内的 `chmod 755 core.test`。

设置环境变量，调用 [执行脚本](../../scripts/test-process-linux.sh)：

| 变量 | 内容 |
|---|---|
| `TUSKRAG_TEST_PROCESS_BINARY` | cmd/tuskrag race 二进制绝对路径 |
| `TUSKRAG_TEST_JOBS_BINARY` | internal/jobs race 二进制绝对路径 |
| `TUSKRAG_TEST_ADMIN_URL` / `TUSKRAG_TEST_APP_URL` / `TUSKRAG_TEST_MIGRATOR_URL` | 同一回环测试库的三种角色连接串 |
| `TUSKRAG_TEST_WORKSPACE` | 仓库根目录，或保留相同相对路径的夹具树 |
| `TUSKRAG_TEST_BWRAP` | 支持 `--disable-userns`、`--ro-bind-fd`、`--bind-fd` 的 Bubblewrap |
| `TUSKRAG_TEST_PARSER_RUNTIME` / `TUSKRAG_TEST_PARSER` | 完整解析器运行时目录及入口绝对路径 |
| `TUSKRAG_TEST_LOG_DIR` | 日志目录，默认 `./test-results/linux-process` |
| `TUSKRAG_TEST_CGROUP` | 可选，已有的委派 cgroup；省略时脚本创建并清理专用组，需要相应权限 |

夹具树须包含 `services/core/cmd/tuskrag` 工作目录、`services/core/internal/parser/testdata/native-text`、`services/parser/tests/fixtures`。不要从模块根目录直接运行二进制，否则夹具相对路径不正确。

```sh
bash scripts/test-process-linux.sh shutdown
bash scripts/test-process-linux.sh native
# 定向检查通过后，再执行完整链路；process 会先重验原生解析隔离。
bash scripts/test-process-linux.sh process
```

`shutdown` 执行真实 PG 队列测试和启动层停机回归。`native` 仅执行隔离解析探测。`process` 执行生产 run() 的 worker/all 子进程、上传/解析、版本替换、清理及 T13 创建/同键重试/切换/查询/取消。模型采用可控 HTTP 替身，不调用客户模型。

脚本不自动编译、不下载或安装依赖、不创建 PostgreSQL、不跳过失败步骤。缺少必需测试或 Bubblewrap 参数时直接失败，避免“没有测试执行”造成伪成功。环境预检失败时先运行 `native` 定位，不重复执行整条进程链路。

已验证环境见 [生产闭环证据](../../docs/eval-reports/2026-11-07-rebuild-production.md)。Ubuntu 的部分 Bubblewrap 0.9.0 发行包带有 FD 绑定回补；不能只根据版本号认定功能齐全。
