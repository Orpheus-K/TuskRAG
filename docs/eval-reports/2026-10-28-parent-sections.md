# 父节切分结构对照（2026-10-28）

关联 R04、A08。样例为 4 段合成文本，跨 2 个逻辑页，不包含客户文档。

| 计数方法 | 不拆分 | 切分后 | 保留 |
|---|---:|---|---|
| Unicode 码点替身，上限 24 | 45 | 2 段，最大 23 | 4/4 块、顺序和页范围 |
| 固定 BGE-M3 词表，上限 20 | 30 token | 2 段，分别 16、16 token | 4/4 块、顺序和页范围 |

标题路径不进入父节正文计数；每段正文由连续的完整原始块组成。空标题节保留位置并以零表示没有正文。单块超过上限时显式失败，尚未在父节切分器中拆分该块；不返回部分结果。该组件只生成内存中的父节片段，不创建节树节点、写入索引或运行 worker。

复现：在 `services/core` 运行 `go test -count=1 -run TestSplitParentSection -v ./internal/ingest` 和 `go test -count=1 -run TestBGEM3CounterWithParentSections -v ./tests/integration`。以 `tokenizers==0.22.1` 和固定词表运行 [参考计数脚本](../../tests/spikes/tokenizer-hf/structured-reference.py)，独立得到 `parent_unsplit=30`、`parent_pieces=[16,16]`。测试文件 SHA-256：父节测试 `95bb71de1d5f0277718b3e8bc003742e6fbde7e397ca0a03267a81665de8dce2`；tokenizer 集成测试 `dabdd503d8f91a99fd46852991f157d27bdfb544c517ddcb48b05c9f9d2baddd`。

另覆盖空节、页码和标题路径、表格坐标及单元格复制、超长块、计数错误、取消和每组最多 512 块；Linux 竞争检测通过。Linux 无网络命名空间中的 15 秒模糊测试运行 89,898 次输入，未发现块丢失或重排。

计划构造器已连接父子块与暂存，表格、长段落和支持结构的长列表可在进入父节前拆分，见[计划对照](2026-10-28-ingest-plan.md)。实际嵌入服务计数一致性和检索评测尚未完成；Recall、MRR、越权证据数和 E3 未运行，不能判定 R04 完成。
