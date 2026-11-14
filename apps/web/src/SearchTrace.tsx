import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@tuskrag/ui/collapsible";
import type { SearchResult } from "@tuskrag/api-client";
import { DataTable, DataTableCell, DataTableRow } from "@tuskrag/ui/data-table";

export function SearchTrace({
  trace,
}: {
  trace: NonNullable<SearchResult["trace"]>;
}) {
  return (
    <Collapsible className="rounded-md border p-3 text-sm">
      <CollapsibleTrigger className="cursor-pointer font-medium">
        检索调试 · {trace.candidates?.length ?? 0} 个可见候选
      </CollapsibleTrigger>
      <CollapsibleContent>
        <p className="mt-3 text-muted-foreground">
          {Object.entries(trace.timings_ms)
            .map(([name, ms]) => `${name}: ${ms.toFixed(1)} ms`)
            .join(" · ")}
        </p>
        <p className="mt-2 text-muted-foreground">
          过滤：{JSON.stringify(trace.filters)} · 降级：
          {trace.degraded?.join("、") || "无"}
        </p>
        <DataTable
          className="mt-3 text-xs"
          headers={[
            "文档 / 块",
            "BM25 排名 / 分数",
            "向量排名 / 距离",
            "RRF 排名 / 分数",
            "重排排名 / 分数",
          ]}
        >
          {trace.candidates?.map((c) => (
            <DataTableRow key={c.ChunkID}>
              <DataTableCell className="max-w-48 break-words">
                {c.Title} / {c.ChunkID}
              </DataTableCell>
              <DataTableCell>
                {c.LexicalRank
                  ? `${c.LexicalRank} / ${c.LexicalScore.toFixed(4)}`
                  : "—"}
              </DataTableCell>
              <DataTableCell>
                {c.SemanticRank
                  ? `${c.SemanticRank} / ${c.SemanticDistance.toFixed(4)}`
                  : "—"}
              </DataTableCell>
              <DataTableCell>
                {c.RRFRank} / {c.Score.toFixed(6)}
              </DataTableCell>
              <DataTableCell>
                {c.RerankScore === null
                  ? "—"
                  : `${c.RerankRank} / ${c.RerankScore.toFixed(4)}`}
              </DataTableCell>
            </DataTableRow>
          ))}
        </DataTable>
      </CollapsibleContent>
    </Collapsible>
  );
}
