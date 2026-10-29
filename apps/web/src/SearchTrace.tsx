import type { SearchResult } from "@tuskrag/api-client";

export function SearchTrace({
  trace,
}: {
  trace: NonNullable<SearchResult["trace"]>;
}) {
  return (
    <details className="rounded-md border p-3 text-sm">
      <summary className="cursor-pointer font-medium">
        检索调试 · {trace.candidates?.length ?? 0} 个可见候选
      </summary>
      <p className="mt-3 text-muted-foreground">
        {Object.entries(trace.timings_ms)
          .map(([name, ms]) => `${name}: ${ms.toFixed(1)} ms`)
          .join(" · ")}
      </p>
      <p className="mt-2 text-muted-foreground">
        过滤：{JSON.stringify(trace.filters)} · 降级：
        {trace.degraded?.join("、") || "无"}
      </p>
      <div className="mt-3 overflow-x-auto">
        <table className="w-full text-left text-xs">
          <caption className="sr-only">
            BM25、向量、RRF 和重排分数与排名
          </caption>
          <thead>
            <tr>
              {[
                "文档 / 块",
                "BM25 排名 / 分数",
                "向量排名 / 距离",
                "RRF 排名 / 分数",
                "重排排名 / 分数",
              ].map((title) => (
                <th className="whitespace-nowrap border-b p-2" key={title}>
                  {title}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {trace.candidates?.map((c) => (
              <tr key={c.ChunkID}>
                <td className="max-w-48 break-words border-b p-2">
                  {c.Title} / {c.ChunkID}
                </td>
                <td className="border-b p-2">
                  {c.LexicalRank
                    ? `${c.LexicalRank} / ${c.LexicalScore.toFixed(4)}`
                    : "—"}
                </td>
                <td className="border-b p-2">
                  {c.SemanticRank
                    ? `${c.SemanticRank} / ${c.SemanticDistance.toFixed(4)}`
                    : "—"}
                </td>
                <td className="border-b p-2">
                  {c.RRFRank} / {c.Score.toFixed(6)}
                </td>
                <td className="border-b p-2">
                  {c.RerankScore === null
                    ? "—"
                    : `${c.RerankRank} / ${c.RerankScore.toFixed(4)}`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </details>
  );
}
