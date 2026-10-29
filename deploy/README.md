# 开发预览：本地管理员初始化

当前开发预览支持本地账号登录、配置嵌入模型、创建知识库和上传文档并排队；后台处理和检索尚未接入，上传的文档暂不能检索。数据库镜像和角色初始化见 [PostgreSQL 说明](postgres/README.md)。

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

如需保存模型提供方的 API 密钥，启动 API 前设置 `TUSKRAG_MASTER_KEY_FILE` 为绝对路径。文件内容为随机生成的 32 字节密钥的 64 个十六进制字符，权限要求与初始化口令文件相同；密钥不可与数据库放在一起。未设置主密钥时可保存无密钥的模型配置，保存 API 密钥会明确失败。当前尚不支持主密钥轮换，必须妥善保管原密钥。配置保存时不会验证模型连接；新租户的出域上限为 `deny`，出域策略管理尚未开放。

如需接收文档，设置 `TUSKRAG_STORAGE_DIR` 为持久化原件目录的绝对路径；未设置时上传接口返回服务不可用。运行账号还须能写入系统临时目录，multipart 文件会先写入该目录、处理完后删除；应限制该目录的访问权限并预留空间。当前接受单个 CSV、Markdown、TXT、DOCX、XLSX 文件，文件上限 200 MiB，同时最多读取两个上传请求。
