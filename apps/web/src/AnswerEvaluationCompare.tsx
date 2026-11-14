import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  type AnswerEvalMetrics,
  ApiError,
  compareAnswerEvalReports,
  getKnowledgeBase,
  type Identity,
  listAnswerEvalReports,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";

export type AnswerComparisonSelection = {
  baseline: string;
  current: string;
  cursor: string;
  offset: number;
};

const metrics: [keyof AnswerEvalMetrics, string][] = [
  ["valid_citation_rate", "Valid Citation"],
  ["source_integrity_rate", "Source Integrity"],
  ["evidence_hit_rate", "Evidence Hit"],
  ["deterministic_pass_rate", "Deterministic Pass"],
  ["refusal_correct_rate", "Refusal Correct"],
  ["false_refusal_rate", "False Refusal"],
  ["unsupported_rate", "Unsupported Rate"],
  ["citation_support_rate", "Citation Support"],
];

function formatRate(value: number | null) {
  return value === null ? "不适用" : value.toFixed(4);
}

function formatDelta(value: number | null) {
  if (value === null) return "不适用";
  return `${value > 0 ? "+" : ""}${value.toFixed(4)}`;
}

function errorText(error: unknown) {
  if (error instanceof ApiError) {
    if (error.status === 401 || error.status === 403)
      return "当前账号无权对比生成评测报告。";
    if (error.status === 404) return "选中的生成评测报告不存在或已失效。";
    if (error.status === 409)
      return "两份生成评测报告必须具有相同数据集版本、模型类型、Prompt 版本与题目集合。";
    return `请求失败（${error.code} · 追踪 ${error.traceId || "无"}）。`;
  }
  return "加载生成评测对比失败。";
}

export function AnswerEvaluationCompare({
  identity,
  id,
  selection,
  onChange,
}: {
  identity: Identity;
  id: number;
  selection: AnswerComparisonSelection;
  onChange: (next: Partial<AnswerComparisonSelection>) => void;
}) {
  const scope = ["answer-eval", identity.tenant_id, identity.user_id, id];
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const list = useQuery({
    queryKey: [...scope, "list", selection.cursor],
    queryFn: ({ signal }) =>
      listAnswerEvalReports(id, selection.cursor, signal),
    enabled: admin,
    retry: false,
    gcTime: 0,
  });
  const ready = admin && selection.baseline !== "" && selection.current !== "";
  const compare = useQuery({
    queryKey: [
      ...scope,
      "compare",
      selection.baseline,
      selection.current,
      selection.offset,
    ],
    queryFn: ({ signal }) =>
      compareAnswerEvalReports(
        id,
        selection.baseline,
        selection.current,
        selection.offset,
        signal,
      ),
    enabled: ready,
    retry: false,
    gcTime: 0,
  });

  if (base.error || list.error)
    return (
      <section className="space-y-4 py-12">
        <p role="alert">{errorText(base.error || list.error)}</p>
        <Button
          onClick={() => {
            void base.refetch();
            if (admin) void list.refetch();
          }}
        >
          重新加载
        </Button>
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
        当前账号无生成评测对比权限。
      </p>
    );

  const data = compare.data;
  const rows = data?.rows ?? [];

  return (
    <section className="space-y-6 py-12">
      <div className="flex flex-wrap gap-4 text-sm">
        <Link
          to="/kb/$kbId/answer-evaluation"
          params={{ kbId: String(id) }}
          search={{ cursor: "", digest: selection.baseline, row: 0 }}
          className="underline"
        >
          返回生成评测
        </Link>
        <Link
          to="/kb/$kbId/eval/compare"
          params={{ kbId: String(id) }}
          search={{ baseline: "", current: "", cursor: "", offset: 0 }}
          className="underline"
        >
          检索对比
        </Link>
      </div>
      <h1 className="break-words text-3xl font-semibold">
        {base.data?.name} · 生成评测对比
      </h1>

      {list.isPending ? (
        <p role="status">正在加载生成报告列表</p>
      ) : (
        <ul className="space-y-2">
          {list.data?.items?.map((item) => (
            <li
              key={item.digest}
              className="flex flex-wrap items-center justify-between gap-3 rounded-xl border p-3 text-sm"
            >
              <div>
                <p className="font-medium">
                  {item.dataset_version} · {item.model_label} ({item.model_kind}
                  ) · {item.prompt_version}
                </p>
                <p className="font-mono text-xs text-muted-foreground">
                  {item.digest.slice(0, 12)}
                </p>
              </div>
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant={
                    selection.baseline === item.digest ? "default" : "outline"
                  }
                  onClick={() => onChange({ baseline: item.digest, offset: 0 })}
                >
                  设为基线
                </Button>
                <Button
                  type="button"
                  variant={
                    selection.current === item.digest ? "default" : "outline"
                  }
                  onClick={() => onChange({ current: item.digest, offset: 0 })}
                >
                  设为当前
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {compare.isError && <p role="alert">{errorText(compare.error)}</p>}
      {ready && compare.isPending && <p role="status">正在生成对比结果</p>}

      {data && (
        <section className="space-y-5 rounded-xl border p-5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-xl font-semibold">
              门禁状态：
              {data.gate === "passed" ? "通过 (passed)" : "回退 (regressed)"}
            </h2>
            <p className="text-sm text-muted-foreground">
              基线 {data.baseline.model_label} vs 当前{" "}
              {data.current.model_label}
            </p>
          </div>
          <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            {metrics.map(([key, label]) => (
              <div key={key} className="rounded-lg border p-3 text-sm">
                <dt className="font-medium">{label}</dt>
                <dd className="mt-2 space-y-1 font-mono">
                  <p>基线 {formatRate(data.baseline.summary.metrics[key])}</p>
                  <p>当前 {formatRate(data.current.summary.metrics[key])}</p>
                  <p>差值 {formatDelta(data.delta[key])}</p>
                </dd>
              </div>
            ))}
          </dl>
          <div className="space-y-2">
            <h3 className="text-lg font-medium">
              逐题对比（共 {data.total} 题）
            </h3>
            <ul className="space-y-2">
              {rows.map((row) => (
                <li key={row.id} className="rounded-lg border p-3 text-sm">
                  <p className="font-medium">
                    {row.id} · {row.answerable ? "可答" : "不可答"}
                  </p>
                  <p className="text-muted-foreground">
                    基线：{row.baseline_outcome}（引用{" "}
                    {row.baseline_valid_citation ? "有效" : "无效"} / 来源{" "}
                    {row.baseline_source_integrity ? "一致" : "越界"}） · 当前：
                    {row.current_outcome}（引用{" "}
                    {row.current_valid_citation ? "有效" : "无效"} / 来源{" "}
                    {row.current_source_integrity ? "一致" : "越界"}）
                  </p>
                </li>
              ))}
            </ul>
            <div className="flex gap-4 text-sm">
              {data.offset > 0 && (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() =>
                    onChange({ offset: Math.max(0, data.offset - 20) })
                  }
                >
                  上一页
                </Button>
              )}
              {data.offset + rows.length < data.total && (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => onChange({ offset: data.offset + 20 })}
                >
                  下一页
                </Button>
              )}
            </div>
          </div>
        </section>
      )}
    </section>
  );
}
