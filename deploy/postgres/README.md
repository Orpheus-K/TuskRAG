# PostgreSQL 开发镜像

使用固定的 PostgreSQL 17 镜像，从对应版本的 ParadeDB 镜像复制 `vector` 与 `pg_search` 扩展；首次初始化时创建扩展和分离的应用、迁移角色。设置 `POSTGRES_USER`、`POSTGRES_PASSWORD`、`POSTGRES_DB`、`TUSKRAG_APP_PASSWORD`、`TUSKRAG_MIGRATOR_PASSWORD`；密码应由部署环境注入，勿写入版本库。初始化后以迁移角色运行 `TUSKRAG_ROLE=migrate`，应用使用 `tuskrag_app` 连接。

镜像已在 Linux amd64 的隔离环境中构建并启动；`pg_search` 的许可评估仍是发布门禁（A03）。初始化脚本仅在数据库卷首次创建时运行。

在 Docker 默认共享内存配置下，10 万条合成向量的并行 HNSW 建索引曾因共享内存不足失败；同一实验使用串行构建成功。部署时应结合索引规模验证共享内存与维护并行度，见 [S2 证据](../../tests/spikes/README.md#s2过滤后的-hnsw-迭代扫描单次合成实验)。
