import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  compareEvalReports,
  type EvalComparison,
  type EvalMetrics,
  type EvalRow,
  getKnowledgeBase,
  type Identity,
  listEvalReports,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";

export type ComparisonSelection = {
  baseline: string;
  current: string;
  cursor: string;
  offset: number;
};
const metrics: [keyof EvalMetrics, string][] = [
  ["RecallAt20", "Recall@20"],
  ["HitAt5", "Hit@5"],
  ["MRRAt10", "MRR@10"],
  ["NDCGAt10", "nDCG@10"],
];
function Difference({
  before,
  after,
  delta,
}: {
  before: EvalMetrics | null;
  after: EvalMetrics | null;
  delta: EvalMetrics | null;
}) {
  if (!before || !after || !delta)
    return (
      <p className="text-sm text-muted-foreground">
        无答案题，指标与差值不适用。
      </p>
    );
  return (
    <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      {metrics.map(([key, label]) => (
        <div key={key} className="rounded-lg border p-3">
          <dt className="text-sm font-medium">{label}</dt>
          <dd className="mt-2 space-y-1 text-sm">
            <p>
              基线 <span className="font-mono">{before[key].toFixed(4)}</span>
            </p>
            <p>
              当前 <span className="font-mono">{after[key].toFixed(4)}</span>
            </p>
            <p>
              差值{" "}
              <span className="font-mono">
                {delta[key] > 0 ? "+" : ""}
                {delta[key].toFixed(4)}
              </span>
            </p>
          </dd>
        </div>
      ))}
    </dl>
  );
}
function EvidenceResult({ title, row }: { title: string; row: EvalRow }) {
  return (
    <section className="min-w-0 space-y-2">
      <h4 className="font-medium">{title}</h4>
      <p className="break-words text-sm">
        降级：{row.degraded?.length ? row.degraded.join("、") : "无"}
      </p>
      <p className="break-words text-sm">
        {Object.entries(row.timings_ms)
          .map(([stage, ms]) => `${stage} ${ms.toFixed(2)} ms`)
          .join(" · ")}
      </p>
      {(["fused", "ranked"] as const).map((kind) => (
        <div key={kind}>
          <h5 className="text-sm">
            {kind === "fused" ? "融合候选" : "最终排序"}
          </h5>
          <p className="whitespace-pre-wrap break-words text-sm">
            {row[kind]?.length
              ? row[kind]
                  .map(
                    (e, i) =>
                      `${i + 1}. 文档 ${e.DocumentID} · 节 ${e.SectionID} · 块 ${e.ChunkID}${e.PageStart > 0 ? ` · 页 ${e.PageStart}–${e.PageEnd}` : " · 无页码"}`,
                  )
                  .join("\n")
              : "无证据"}
          </p>
        </div>
      ))}
    </section>
  );
}

export function EvaluationCompare({
  identity,
  id,
  selection,
  onChange,
}: {
  identity: Identity;
  id: number;
  selection: ComparisonSelection;
  onChange: (value: Partial<ComparisonSelection>) => void;
}) {
  const { baseline, current, cursor, offset } = selection;
  const scope = ["eval", identity.tenant_id, identity.user_id, id];
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const list = useQuery({
    queryKey: [...scope, "list", cursor],
    queryFn: ({ signal }) => listEvalReports(id, cursor, signal),
    enabled: admin,
    retry: false,
    gcTime: 0,
  });
  const comparison = useQuery({
    queryKey: [...scope, "comparison", baseline, current, offset],
    queryFn: ({ signal }) =>
      compareEvalReports(id, baseline, current, offset, signal),
    enabled: admin && Boolean(baseline && current),
    retry: false,
    gcTime: 0,
  });
  const revoked =
    comparison.error instanceof ApiError &&
    [401, 403, 404].includes(comparison.error.status);
  const fatal = base.error || list.error || (revoked ? comparison.error : null);
  if (fatal)
    return (
      <section className="space-y-4 py-12">
        <p role="alert">
          知识库、报告或权限已不可用，无法显示对比。请重新加载。
        </p>
        <Button
          onClick={() => {
            void base.refetch();
            if (admin) {
              void list.refetch();
              if (baseline && current) void comparison.refetch();
            }
          }}
        >
          重新加载对比
        </Button>
        <Link
          to="/kb/$kbId/eval"
          params={{ kbId: String(id) }}
          search={{ cursor: "", digest: "", row: 0 }}
        >
          返回报告列表
        </Link>
      </section>
    );
  if (base.isPending)
    return (
      <p role="status" className="py-12">
        正在加载知识库
      </p>
    );
  if (!admin)
    return (
      <p role="alert" className="py-12">
        当前账号无评测报告权限。
      </p>
    );
  const result = comparison.data;
  return (
    <section className="space-y-6 py-12">
      <Link
        to="/kb/$kbId/eval"
        params={{ kbId: String(id) }}
        search={{ cursor: "", digest: "", row: 0 }}
        className="text-sm underline"
      >
        返回报告列表
      </Link>
      <h1 className="break-words text-3xl font-semibold">
        {base.data?.name} · 报告对比
      </h1>
      <p className="text-sm text-muted-foreground">
        选择基线和当前报告。差值为当前减基线；任一汇总指标下降超过两百分点即未通过回归门禁。外部导入报告的对比不证明模型质量。
      </p>
      <div className="space-y-2 rounded-xl border p-5">
        <p className="break-all text-sm">基线：{baseline || "尚未选择"}</p>
        <p className="break-all text-sm">当前：{current || "尚未选择"}</p>
        <div className="flex flex-wrap gap-3">
          <Button
            variant="outline"
            disabled={!baseline || !current}
            onClick={() =>
              onChange({
                baseline: current,
                current: baseline,
                offset: 0,
              })
            }
          >
            交换方向
          </Button>
          <Button
            variant="outline"
            onClick={() =>
              onChange({ baseline: "", current: "", cursor: "", offset: 0 })
            }
          >
            清空选择
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              void list.refetch();
              if (baseline && current) void comparison.refetch();
            }}
          >
            刷新对比
          </Button>
        </div>
      </div>
      <section className="space-y-3" aria-label="选择报告">
        <h2 className="text-xl font-medium">可见报告</h2>
        {list.isPending ? (
          <p role="status">正在加载报告列表</p>
        ) : list.data?.items?.length === 0 ? (
          <p>暂无可见报告。</p>
        ) : (
          <ul className="space-y-3">
            {list.data?.items?.map((item) => (
              <li key={item.digest} className="space-y-3 rounded-xl border p-4">
                <p className="break-words font-medium">
                  {item.dataset_version} · {item.mode} · {item.model_label}
                </p>
                <p className="break-all text-xs text-muted-foreground">
                  外部导入 ·{" "}
                  {item.model_kind === "stub" ? "模型替身" : "真实模型标记"} ·{" "}
                  {item.digest.slice(0, 12)} · {item.created_at}
                </p>
                <div className="flex flex-wrap gap-3">
                  <Button
                    variant="outline"
                    aria-pressed={baseline === item.digest}
                    onClick={() =>
                      onChange({
                        baseline: item.digest,
                        offset: 0,
                      })
                    }
                  >
                    设为基线
                  </Button>
                  <Button
                    variant="outline"
                    aria-pressed={current === item.digest}
                    onClick={() =>
                      onChange({
                        current: item.digest,
                        offset: 0,
                      })
                    }
                  >
                    设为当前
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <nav className="flex flex-wrap gap-4" aria-label="可选报告分页">
          {cursor && (
            <Button variant="outline" onClick={() => onChange({ cursor: "" })}>
              报告首页
            </Button>
          )}
          {list.data?.next_cursor && (
            <Button
              variant="outline"
              onClick={() => {
                const next = list.data?.next_cursor;
                if (next) onChange({ cursor: next });
              }}
            >
              下一页报告
            </Button>
          )}
        </nav>
      </section>
      {!baseline || !current ? (
        <p>请选择两份报告后查看差异。</p>
      ) : comparison.isError ? (
        <div role="alert">
          <p>
            {comparison.error instanceof ApiError &&
            comparison.error.code === "reports_incomparable"
              ? "两份报告不可比：需要相同数据集、检索模式、模型类别和题目集合，且包含有答案题。"
              : "无法确认对比结果，请刷新后重试。"}
          </p>
        </div>
      ) : comparison.isPending ? (
        <p role="status">正在比较报告</p>
      ) : result ? (
        <ComparisonResult
          result={result}
          offset={offset}
          onOffset={(offset) => onChange({ offset })}
        />
      ) : null}
    </section>
  );
}

export function ComparisonResult({
  result,
  offset,
  onOffset,
}: {
  result: EvalComparison;
  offset: number;
  onOffset: (offset: number) => void;
}) {
  return (
    <section className="space-y-4" aria-label="对比结果">
      <h2 className="text-xl font-medium">指标差异</h2>
      <p role="status" className="rounded-lg border p-4 font-medium">
        {result.gate === "regressed"
          ? "未通过回归门禁：存在超过两百分点的下降。"
          : "通过回归门禁：未发现超过两百分点的下降。"}
      </p>
      <p className="text-sm">
        {result.total_rows} 题 · 有答案 {result.baseline.AnswerableCases} ·
        无答案 {result.baseline.UnanswerableCases}
      </p>
      <Difference
        before={result.baseline.Metrics}
        after={result.current.Metrics}
        delta={result.delta}
      />
      <h3 className="text-lg font-medium">逐题差异</h3>
      <p className="text-sm text-muted-foreground">
        按基线题目顺序排列；每页 20
        题。指标原值和差值不包含尚未实现的引用、拒答评测。
      </p>
      {result.rows?.map((row) => (
        <details
          key={`${result.baseline_digest}:${result.current_digest}:${row.id}`}
          className="rounded-lg border p-4"
        >
          <summary className="cursor-pointer break-words">
            {row.id} · {row.evidence_changed ? "证据已变化" : "证据未变化"}
          </summary>
          <div className="mt-4 space-y-4">
            <Difference
              before={row.baseline.metrics}
              after={row.current.metrics}
              delta={row.delta}
            />
            <div className="grid gap-4 sm:grid-cols-2">
              <EvidenceResult title="基线结果" row={row.baseline} />
              <EvidenceResult title="当前结果" row={row.current} />
            </div>
          </div>
        </details>
      ))}
      <nav className="flex flex-wrap gap-4" aria-label="逐题差异分页">
        {offset > 0 && (
          <Button variant="outline" onClick={() => onOffset(0)}>
            差异第一页
          </Button>
        )}
        {offset > 0 && (
          <Button
            variant="outline"
            onClick={() => onOffset(Math.max(0, offset - 20))}
          >
            上一页差异
          </Button>
        )}
        {result.next_offset !== null && (
          <Button
            variant="outline"
            onClick={() => {
              if (result.next_offset !== null) onOffset(result.next_offset);
            }}
          >
            下一页差异
          </Button>
        )}
      </nav>
    </section>
  );
}
