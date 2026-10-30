# 许可证盘点（未完成）

2026-10-30 从腾讯云本轮 PostgreSQL 运行镜像补录 [142 个 Debian 包及版本](postgres-runtime-debian.tsv)和 [139 份 copyright 普通文件的 SHA-256](postgres-runtime-copyright-sha256.txt)。命令为容器内 `dpkg-query -W` 与 `find /usr/share/doc -name copyright -type f -exec sha256sum {} +`。该镜像使用仓库 Dockerfile 的固定上游摘要，镜像 ID 为 `sha256:ee9344e5fb03711272fead35ba7244cc9ce4b5018a09fd34720b5ca00d5e36e0`。这些是操作系统运行包的可追溯元数据，不覆盖手动复制的 PostgreSQL 扩展、Python wheel 内置组件或源码提供义务，也不替代法务意见。

2026-10-29 增量盘点：刷新 [Go 运行依赖](go-runtime.csv)，补充 [前端生产及开发依赖](frontend-all.json) 与 [锁文件、Python 安装元数据、许可原文 SHA-256 清单](inventory.json)。复现元数据清单：`node scripts/license-inventory.mjs <parser-site-packages>`；前端使用 `pnpm licenses list --json`。元数据来自已安装包，不等于已审核的交付 SBOM；项目自有代码已根据所有者授权采用 MIT（根目录 LICENSE）；pg_search 仍保留 AGPL-3.0，法务结论待取得。

| 组件 | 版本 | 许可证 | 原文 |
|---|---|---|---|
| pg_search | 0.22.6 | AGPL-3.0 | [原文](../postgres/licenses/pg_search-0.22.6.txt)；[上游](https://github.com/paradedb/paradedb/blob/v0.22.6/LICENSE) |
| pgvector | 0.8.1 | PostgreSQL | [原文](../postgres/licenses/pgvector-0.8.1.txt)；[上游](https://github.com/pgvector/pgvector/blob/v0.8.1/LICENSE) |
| gse（E4 分词） | 0.80.3 | Apache-2.0 | [原文](lexical/gse-0.80.3.txt) |
| cedar（gse 依赖） | 0.20.2 | BSD-2-Clause | [原文](lexical/cedar-0.20.2.txt) |
| River（仅 S3 实验） | 0.49.0 | MPL-2.0 | [上游](https://github.com/riverqueue/river/blob/v0.49.0/LICENSE) |
| golang.org/x/crypto（身份组件） | 0.58.0 | BSD-3-Clause | [上游](https://cs.opensource.google/go/x/crypto/+/v0.58.0:LICENSE) |
| golang.org/x/sys（crypto 依赖） | 0.49.0 | BSD-3-Clause | [上游](https://cs.opensource.google/go/x/sys/+/v0.49.0:LICENSE) |
| jsonschema/v6（解析契约校验） | 6.0.3 | Apache-2.0 | [上游](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/LICENSE) |
| goldmark（Go 原生 Markdown 解析） | 1.8.6 | MIT | [原文](parser/goldmark-1.8.6.txt) |
| goSentencePiece（字符映射） | 1.1.0 | MIT | [原文](tokenizer/go-sentencepiece-1.1.0.txt) |
| uax29（内置 Unicode 16 字素切分子集） | 2.4.0 | MIT | [原文](tokenizer/uax29-2.4.0.txt)；[Unicode 数据许可](tokenizer/unicode-license.txt) |
| stringish（字素切分依赖） | 0.1.1 | MIT | [原文](tokenizer/stringish-0.1.1.txt) |
| protobuf-go（字符映射库传递依赖） | 1.36.12 | BSD-3-Clause | [原文](tokenizer/protobuf-1.36.12.txt) |
| BGE-M3 tokenizer 词表（不含权重） | revision `5617a9f` | 模型卡声明 MIT | [来源与校验](../../services/core/internal/tokenizer/assets/README.md)；版权与交付材料仍需核验 |
| TanStack Router（前端路由） | 1.170.46 | MIT | [上游](https://github.com/TanStack/router/blob/main/LICENSE)；传递依赖见前端元数据 |
| react-markdown（章节渲染） | 10.1.0 | MIT | [原文](frontend/react-markdown-10.1.0.txt)；传递依赖见前端元数据 |
| remark-gfm（表格渲染） | 4.0.1 | MIT | [原文](frontend/remark-gfm-4.0.1.txt)；传递依赖见前端元数据 |
| python-docx（解析工具） | 1.2.0 | MIT | [原文](parser/python-docx-1.2.0.txt) |
| python-calamine（解析工具） | 0.9.0 | MIT | [原文](parser/python-calamine-0.9.0.txt)；Rust 二进制的依赖许可待核验 |
| openpyxl（解析工具） | 3.1.5 | MIT | [原文](parser/openpyxl-3.1.5.txt) |
| et-xmlfile（openpyxl 依赖） | 2.0.0 | MIT 及 Python License | [原文](parser/et-xmlfile-2.0.0.txt)、[Python 代码许可](parser/et-xmlfile-python-2.0.0.txt) |
| defusedxml（解析工具） | 0.7.1 | PSF License | [原文](parser/defusedxml-0.7.1.txt) |
| olefile（解析工具） | 0.47 | BSD-2-Clause | [原文](parser/olefile-0.47.txt) |
| lxml（解析工具） | 6.1.3 | BSD-3-Clause 及附带组件许可 | [原文](parser/lxml-6.1.3.txt)、[附带组件](parser/lxml-bundled-6.1.3.txt) |
| typing-extensions（解析工具） | 4.16.0 | PSF-2.0 | [原文](parser/typing-extensions-4.16.0.txt) |
| pdfplumber（数字 PDF 解析） | 0.11.10 | MIT | [原文](parser/pdfplumber-0.11.10/LICENSE.txt) |
| pdfminer.six（pdfplumber 依赖） | 20260107 | MIT | [原文](parser/pdfminer.six-20260107/LICENSE) |
| pypdfium2（PDF 独立文本校验） | 5.14.0 | BSD-3-Clause、Apache-2.0 及二进制依赖许可 | [Linux x64 wheel](parser/pypdfium2-5.14.0/linux-x64/)、[Windows x64 wheel](parser/pypdfium2-5.14.0/windows-x64/) |
| Pillow（pdfplumber 依赖） | 13.0.0 | MIT-CMU | [原文](parser/pillow-13.0.0/LICENSE) |
| cryptography（pdfminer.six 依赖） | 50.0.2 | Apache-2.0 或 BSD-3-Clause | [原文](parser/cryptography-50.0.2/LICENSE)、[Apache](parser/cryptography-50.0.2/LICENSE.APACHE)、[BSD](parser/cryptography-50.0.2/LICENSE.BSD) |
| cffi（cryptography 依赖） | 2.1.1 | MIT-0 | [原文](parser/cffi-2.1.1/LICENSE) |
| pycparser（cffi 依赖） | 3.12 | BSD-3-Clause | [原文](parser/pycparser-3.12/LICENSE) |
| charset-normalizer（pdfminer.six 依赖） | 3.5.2 | MIT | [原文](parser/charset-normalizer-3.5.2/LICENSE) |

原文 SHA-256：`pg_search` 为 `8486a10c4393cee1c25392769ddd3b2d6c242d6ec7928e1414efff7dfb2f07ef`；`pgvector` 为 `3959f8e3ea8b08e35d1c26fc25bc7b6dcf7c2d80f372e4fbff6f6b0412758456`。

- [Go 运行依赖报告](go-runtime.csv)：由 Windows amd64 环境的 `go-licenses` 2.0.1 对 `./cmd/tuskrag` 生成。项目自有代码采用 MIT。工具对私有模块的源码 URL 显示 Unknown，不表示许可证未知；扫描时将根 LICENSE 复制到镜像工作目录的 Go 模块根，许可证真源仍是根 LICENSE。
- [Go 解析包依赖报告](go-parser.csv)：由同一工具对尚未接入 worker 的 `./internal/parser` 生成；项目自身同样为 `Unknown`。
- [Go tokenizer 包依赖报告](go-tokenizer.csv)：由同一工具对尚未接入 worker 的 `./internal/tokenizer` 生成；项目自身为 `Unknown`，内置字素切分代码的许可证原文已随包保存。
- [前端生产依赖元数据](frontend-runtime.json)：由 `pnpm licenses list --prod --json` 提取包名、版本、许可与主页，不等同于最终构建产物的完整许可证核验。
- Python 运行依赖由 `uv tree --no-dev` 核对；已保存本次 Linux x64 与 Windows x64 测试 wheel 的 PDFium 附带许可。python-calamine 的 Rust 二进制传递依赖，以及 lxml、Pillow、cryptography 和 PDFium wheel 的全部内置组件仍需按实际交付平台核对版本、许可和义务；开发依赖也需单独盘点。Windows 与 Linux 测试 wheel 的 libxml2 版本分别为 2.11.9 和 2.14.6，不把组件版本列为安全评估结论。

首发为客户内网私有化。向其他组织交付包含 `pg_search` 的二进制镜像时，即使组件未修改，也需要按 [AGPL-3.0 第 6 节](https://www.gnu.org/licenses/agpl-3.0.html)核对对应源码的提供方式；[GNU FAQ](https://www.gnu.org/licenses/gpl-faq.html#InternalDistribution)区分同一组织内部复制与向其他组织转移副本。修改后的程序通过网络供用户交互时，还需评估第 13 节。TuskRAG 与数据库扩展的作品边界、具体交付方式和适用义务尚无法务结论。

当前镜像直接从固定 digest 的上游镜像复制 `pg_search.so`；该镜像的 OCI 标签记录源码仓库，但未记录源码修订号。仅附带许可证文本不能证明已提供与该二进制对应的完整源码及构建资料，需在交付前核实并留存对应版本及编译依赖。Go 与前端其他平台依赖、开发工具的完整盘点及法务评估仍未完成。
