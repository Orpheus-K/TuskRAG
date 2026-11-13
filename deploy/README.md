# 部署、运行与运维手册

本目录提供 TuskRAG 的容器化生产编排（[Compose 编排](compose/docker-compose.lite.yml)）、Linux 二进制打包与 systemd 部署（[Linux 交付](linux/README.md)）、PostgreSQL 镜像（[PostgreSQL 说明](postgres/README.md)）及依赖许可盘点（[许可证盘点](licenses/README.md)）。

## 1. 容器化 Compose 档位（M5）

- **镜像构建**：[Dockerfile](Dockerfile) 采用多阶段构建（`pnpm build` → `CGO_ENABLED=0` Go 静态二进制 → 非 root 运行时 + `bubblewrap` + Python 解析器）。
- **单机 `lite` 档位**：`docker compose -f deploy/compose/docker-compose.lite.yml --env-file deploy/compose/.env up -d`（`postgres` + `tuskrag` `role=all`，只读根文件系统）。
- **标准 `standard` 档位**：`docker compose -f deploy/compose/docker-compose.standard.yml --env-file deploy/compose/.env up -d`（`postgres` + `tuskrag-migrate` + `tuskrag-api` + `tuskrag-worker` 读写分离）。
- **可选叠加档位**：
  - 内网模型节点：[docker-compose.model-node.yml](compose/docker-compose.model-node.yml)（TEI 嵌入/重排）。
  - 旧版 Office 转换：[docker-compose.legacy-office.yml](compose/docker-compose.legacy-office.yml)（无网络 LibreOffice sidecar）。
- **监控与告警**：启用 `GET /metrics`（回环地址免鉴权或配置 `TUSKRAG_METRICS_TOKEN` Bearer 鉴权），告警规则模板见 [prometheus-alerts.yml](compose/prometheus-alerts.yml)；SBOM 与依赖锁文件校验生成见 [sbom.sh](linux/sbom.sh)。

## 2. 管理员初始化与运维命令

数据库迁移至最新版本后，使用 `tuskrag_app` 连接执行一次 `tuskrag bootstrap`。口令放在独立文件中，Linux 文件必须为普通文件且仅所有者可访问（例如 `0600`）；Windows 开发机需自行将文件 ACL 限制到当前用户和受信任的系统管理员。命令只输出租户与用户 ID，不输出口令。

```sh
export TUSKRAG_DATABASE_URL='postgres://tuskrag_app:<password>@127.0.0.1:5432/tuskrag?sslmode=disable'
export TUSKRAG_BOOTSTRAP_TENANT='example'
export TUSKRAG_BOOTSTRAP_LOGIN='admin'
export TUSKRAG_BOOTSTRAP_DISPLAY_NAME='Administrator'
export TUSKRAG_BOOTSTRAP_PASSWORD_FILE='/run/secrets/tuskrag-initial-password'
./tuskrag bootstrap
```

口令至少 12 字节；密码文件末尾的单个换行会被移除。租户名称必须唯一。成功后按本地密钥管理规则移除初始化口令文件，保管租户 ID；数据出域上限初始为 `deny`。许可评估仍未完成，见 [许可证盘点](licenses/README.md)。

API 默认使用仅可通过 HTTPS 传输的会话 Cookie。仅在本机开发时，可将 `TUSKRAG_LISTEN` 绑定回环地址并设置 `TUSKRAG_INSECURE_LOCAL_COOKIES=true`，以便通过 HTTP 访问；生产环境应使用 HTTPS。

如需保存模型提供方的 API 密钥或 OIDC 客户端密钥，启动 API/worker 前设置 `TUSKRAG_MASTER_KEY_FILE` 为绝对路径。文件内容为随机生成的 32 字节密钥的 64 个十六进制字符，权限要求与初始化口令文件相同；密钥不可与数据库放在一起。如需轮换主密钥，可停止写入后设置 `TUSKRAG_OLD_MASTER_KEY_FILE` 与新 `TUSKRAG_MASTER_KEY_FILE`，执行 `tuskrag ops rotate-master-key <tenant-id>` 在单事务内完成重加密。可用 `tuskrag ops verify-storage <tenant-id>` 校验原件完整性，用 `tuskrag ops diagnose <tenant-id>` 导出脱敏诊断快照。

如需接收文档，设置 `TUSKRAG_STORAGE_DIR` 为持久化原件目录的绝对路径；未设置时上传接口返回服务不可用。运行账号还须能写入系统临时目录，multipart 文件会先写入该目录、处理完后删除；应限制该目录的访问权限并预留空间。单文件上限 200 MiB，同时最多读取两个上传请求。

支持范围为 UTF-8 Markdown/TXT、CSV、静态 HTML、PPTX，以及受限 DOCX、XLSX、数字 PDF，并支持按需 VLM 低分页重解析与带 SSRF 防护的 URL 导入；详细边界和黄金样例见 [解析说明](../docs/05-文档解析与入库.md)。复杂版面（未启用 VLM 时）、加密文件与未配置 sidecar 时的旧版 Office 不属于默认支持范围。低质量解析或未确认表头会显式失败，不会以“已上传”表示“可检索”。仅数字 PDF 的页码表示物理页，其他格式按章节定位。Windows 完整解析沙箱尚未实现，生产与完整链路验收使用 Linux。

Markdown 当前不接受带语言标注的代码块、图片、HTML 等结构。保留原字节内容并改用 `.txt` 扩展名可按纯文本入库；这不等于支持这些 Markdown 结构，也不会产生其结构化表格或物理页码。
