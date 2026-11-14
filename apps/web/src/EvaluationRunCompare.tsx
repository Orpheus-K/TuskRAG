import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  compareEvalRuns,
  getKnowledgeBase,
  type Identity,
  listEvalRuns,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { ComparisonResult } from "./EvaluationCompare";
import { evalRunStatusLabels } from "./EvaluationRun";
export type RunComparisonSelection = {
  baseline: number;
  current: number;
  cursor: number;
  offset: number;
};
export function EvaluationRunCompare({
  identity,
  id,
  selection,
  onChange,
}: {
  identity: Identity;
  id: number;
  selection: RunComparisonSelection;
  onChange: (value: Partial<RunComparisonSelection>) => void;
}) {
  const { baseline, current, cursor, offset } = selection;
  const scope = [
    "eval-run-comparison",
    identity.tenant_id,
    identity.user_id,
    id,
  ];
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const list = useQuery({
    queryKey: [...scope, "list", cursor],
    queryFn: ({ signal }) => listEvalRuns(id, cursor, signal),
    enabled: admin && !base.isError,
    retry: false,
    gcTime: 0,
  });
  const comparison = useQuery({
    queryKey: [...scope, "detail", baseline, current, offset],
    queryFn: ({ signal }) =>
      compareEvalRuns(id, baseline, current, offset, signal),
    enabled: admin && !base.isError && baseline > 0 && current > 0,
    retry: false,
    gcTime: 0,
  });
  const revoked =
    comparison.error instanceof ApiError &&
    [401, 403, 404].includes(comparison.error.status);
  if (base.isPending) return <p role="status">正在加载知识库</p>;
  if (base.isError || list.isError || revoked)
    return (
      <section className="py-10 space-y-4">
        <p role="alert">知识库、运行列表或证据不可用，对比内容已隐藏。</p>
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
      </section>
    );
  if (!admin) return <p role="alert">当前账号无评测运行权限。</p>;
  return (
    <section className="py-10 space-y-6">
      <Link
        className="text-sm underline"
        to="/kb/$kbId/eval/history"
        params={{ kbId: String(id) }}
        search={{ cursor: 0 }}
      >
        返回运行历史
      </Link>
      <h1 className="text-3xl font-semibold break-words">
        {base.data?.name} · 运行对比
      </h1>
      <p className="text-sm text-muted-foreground">
        差值为当前减基线；任一汇总指标下降超过两百分点即未通过回归门禁。
      </p>
      {base.data?.status === "archived" && (
        <p role="status">知识库已归档，仍可查看历史对比。</p>
      )}
      <section
        className="rounded-xl border p-5 space-y-3"
        aria-label="运行选择"
      >
        <p>基线运行：{baseline || "尚未选择"}</p>
        <p>当前运行：{current || "尚未选择"}</p>
        <div className="flex flex-wrap gap-3">
          <Button
            variant="outline"
            disabled={!baseline || !current}
            onClick={() =>
              onChange({ baseline: current, current: baseline, offset: 0 })
            }
          >
            交换方向
          </Button>
          <Button
            variant="outline"
            onClick={() =>
              onChange({ baseline: 0, current: 0, offset: 0, cursor: 0 })
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
      </section>
      <section className="space-y-3" aria-label="选择运行">
        <h2 className="text-xl font-medium">可见运行</h2>
        {list.isPending ? (
          <p role="status">正在加载运行</p>
        ) : list.data?.items.length === 0 ? (
          <p>当前页没有可见运行。</p>
        ) : (
          <ul className="space-y-3">
            {list.data?.items.map((item) => (
              <li key={item.id} className="rounded-xl border p-4 space-y-3">
                <p>
                  运行 {item.id} · 题集 {item.set.set_id} · 修订{" "}
                  {item.set.revision} · {item.mode}
                </p>
                <p>
                  {evalRunStatusLabels[item.status]}
                  {!item.result_available ? " · 尚无成功结果" : " · 服务端执行"}
                </p>
                <div className="flex flex-wrap gap-3">
                  <Button
                    variant="outline"
                    disabled={!item.result_available}
                    aria-pressed={baseline === item.id}
                    onClick={() => onChange({ baseline: item.id, offset: 0 })}
                  >
                    设为基线
                  </Button>
                  <Button
                    variant="outline"
                    disabled={!item.result_available}
                    aria-pressed={current === item.id}
                    onClick={() => onChange({ current: item.id, offset: 0 })}
                  >
                    设为当前
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <nav className="flex flex-wrap gap-3" aria-label="可选运行分页">
          {cursor > 0 && (
            <Button variant="outline" onClick={() => onChange({ cursor: 0 })}>
              运行首页
            </Button>
          )}
          {list.data?.next_cursor != null && (
            <Button
              variant="outline"
              onClick={() => onChange({ cursor: list.data?.next_cursor ?? 0 })}
            >
              下一页运行
            </Button>
          )}
        </nav>
      </section>
      {!baseline || !current ? (
        <p>请选择两次已完成运行。</p>
      ) : comparison.isError ? (
        <p role="alert">
          {comparison.error instanceof ApiError &&
          comparison.error.code === "reports_incomparable"
            ? "两次运行不可比：需要相同题集内容、模式、模型类别和题目集合，且包含有答案题。"
            : comparison.error instanceof ApiError &&
                comparison.error.code === "result_unavailable"
              ? "所选运行尚无成功结果，请查看运行状态。"
              : "无法确认对比结果，请刷新后重试。"}
        </p>
      ) : comparison.isPending ? (
        <p role="status">正在比较运行</p>
      ) : (
        comparison.data && (
          <ComparisonResult
            result={comparison.data.comparison}
            offset={offset}
            onOffset={(offset) => onChange({ offset })}
          />
        )
      )}
    </section>
  );
}
