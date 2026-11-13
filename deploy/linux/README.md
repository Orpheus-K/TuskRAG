# Linux 单机开发预览

关联 R08/R17/R20、T06—T09/T16。当前交付 Go 静态二进制、内嵌页面、27 批迁移和按任务调用的解析工具；采用 `all` 进程配合独立 PostgreSQL 17。多实例备份、PITR、S3、Windows 沙箱和通用生产容器安全配置不在本流程范围。文档支持范围以[部署入口](../README.md)为准。

## 安装与启动

准备项目 [PG 镜像与角色](../postgres/README.md)，保持已验证的扩展版本。Linux 需 cgroup v2、systemd、支持 `--disable-userns`/`--ro-bind-fd`/`--bind-fd` 的 Bubblewrap 和 `prlimit`；解析运行时为已构建的 Python 3.13 + `tuskrag-parse` 0.5.0。主机的用户命名空间、AppArmor 策略必须允许该 Bubblewrap 程序运行，不能通过取消解析隔离解决启动失败。

构建命令见根 `package.json` 的 `build` 和 CI；Linux Go 构建须 `CGO_ENABLED=0`，先生成并嵌入当前前端产物。对已构建的 core 和可迁移解析目录打包：

```sh
bash deploy/linux/package.sh /absolute/tuskrag /absolute/parser-runtime /absolute/new-output
```

包内含 SHA-256 清单、OpenAPI、解析器版本和现有许可证清单；不包含数据库镜像、凭据、模型权重，也不代表许可证审查或完整离线发行门禁通过。

先用随包清单核验 `sha256sum -c tuskrag-linux-amd64.tar.gz.sha256`，在新位置解包后进入 `tuskrag` 核验 `sha256sum -c SHA256SUMS`，再把目录放到 `/opt/tuskrag`。创建系统账号 `tuskrag`，程序和解析器由 root 持有且服务账号只读。复制 `tuskrag.env.example` 到 `/etc/tuskrag/tuskrag.env`，root 持有、权限 0600，填写实际数据库与租户 ID。密钥文件由服务账号持有、权限 0600。初始化与主密钥规则见[部署入口](../README.md)。

依次完成：以迁移角色运行 `TUSKRAG_ROLE=migrate`；以应用角色运行 `tuskrag bootstrap`；配置模型与知识库；以迁移角色执行 `tuskrag provision-indexes <tenant-id> <space-id>`。每个新嵌入空间均须准备索引，应用角色不负责执行索引 DDL。

```sh
install -m 0644 /opt/tuskrag/deploy/linux/tuskrag.service /etc/systemd/system/tuskrag.service
systemd-analyze verify /etc/systemd/system/tuskrag.service
systemctl daemon-reload
systemctl start tuskrag.service
curl --fail http://127.0.0.1:8080/api/health/ready
```

服务由 systemd 创建状态与临时目录。启动脚本将主进程移到 `manager` 子组，再启用解析子组的 CPU、内存和 PID 控制器，依据 [systemd 委派规则](https://systemd.io/CGROUP_DELEGATION/)。安装确认后再按运维策略启用开机启动。外部访问须配置 HTTPS 反向代理；SSE 禁止缓冲，代理读取超时须覆盖 120 秒回答期限。仅回环 HTTP 测试可显式设置 `TUSKRAG_INSECURE_LOCAL_COOKIES=true`。

## 冷备份与恢复

需要 PostgreSQL 17 的 `psql`、`pg_dump`、`pg_restore` 客户端及 GNU tar/coreutils。脚本用于单个 `all` 服务的独占维护窗口；停止所有读写客户端、自动重启源和外部 CLI。脚本检查指定服务为 inactive、数据库无其他客户端，并验证应用连接和管理连接指向同一数据库。无需暂停 PostgreSQL。

管理连接通过 `PGHOST`/`PGPORT`/`PGUSER`/`PGDATABASE`/`PGPASSFILE` 注入；密码文件 0600。备份角色必须可读全部租户（管理角色），禁止用应用角色得到部分备份。`TUSKRAG_DATABASE_URL` 则使用同一数据库的 `tuskrag_app`，用于强制 RLS 下逐租户校验。

```sh
systemctl stop tuskrag.service
export TUSKRAG_SERVICE_UNIT=tuskrag.service
export TUSKRAG_BINARY=/opt/tuskrag/tuskrag
export TUSKRAG_STORAGE_DIR=/var/lib/tuskrag/files
# 在安全环境中设置 PG* 和 TUSKRAG_DATABASE_URL；不要写入仓库。
bash /opt/tuskrag/deploy/linux/backup-local.sh /backup/new-snapshot
```

脚本先逐版本校验，再运行 [pg_dump 自定义格式](https://www.postgresql.org/docs/17/app-pgdump.html)并保存原件。数据库、原件归档、版本清单、存储计数均有校验和；最后写入 `COMPLETE`。失败或中断目录不能恢复。加密后的提供方凭据随 PG 保存；主密钥须独立、安全备份，不能只保存数据库。服务配置、角色凭据、制品和解析运行时也须分别保管。

恢复前建立**全新空库**（`template0`）、预建同名应用/迁移角色、安装相同扩展二进制；不要先运行迁移或创建业务表。设置 `PGDATABASE` 和应用 URL 为新库，保持目标服务停止，指定不存在的新文件目录：

```sh
bash /opt/tuskrag/deploy/linux/restore-local.sh /backup/new-snapshot /var/lib/tuskrag-restored/files
```

脚本先核验归档，拒绝覆盖已有库表或文件目录；使用 [pg_restore 单事务、遇错退出](https://www.postgresql.org/docs/17/app-pgrestore.html)，随后验证迁移版本、RLS 及每个原件的规范路径、字节数和 SHA-256。校验是只读操作，不自动修改失败版本。正在清理且文件已移除的版本会失败，须先完成清理再备份。

恢复后核对文件所有权、配置与原主密钥；比较业务表/块/向量/成员/审计计数与固定检索结果、会话和反馈，再启动目标服务。恢复失败时保留失败目录和空库现场，不自动清空、不自动切回。恢复验收不以“命令返回 0”替代内容一致性比较。
