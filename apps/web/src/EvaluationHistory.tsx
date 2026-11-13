import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  getKnowledgeBase,
  type Identity,
  listEvalRuns,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { evalRunStatusLabels } from "./EvaluationRun";

export function EvaluationHistory({
  identity,
  id,
  cursor,
  onCursor,
}: {
  identity: Identity;
  id: number;
  cursor: number;
  onCursor: (cursor: number) => void;
}) {
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const list = useQuery({
    queryKey: [
      "eval-run-history",
      identity.tenant_id,
      identity.user_id,
      id,
      cursor,
    ],
    queryFn: ({ signal }) => listEvalRuns(id, cursor, signal),
    enabled: admin && !base.isError,
    retry: false,
    gcTime: 0,
  });
  if (base.isPending) return <p role="status">正在加载知识库</p>;
  if (
    base.isError ||
    (list.error instanceof ApiError &&
      [401, 403, 404].includes(list.error.status))
  )
    return (
      <section className="py-10 space-y-4">
        <p role="alert">知识库或运行权限已不可用，历史内容已隐藏。</p>
        <Button onClick={() => window.location.reload()}>重新加载</Button>
      </section>
    );
  if (!admin) return <p role="alert">当前账号无评测运行权限。</p>;
  return (
    <section className="py-10 space-y-6">
      <nav className="flex flex-wrap gap-4">
        <Link
          className="underline text-sm"
          to="/kb/$kbId/eval"
          params={{ kbId: String(id) }}
          search={{ cursor: "", digest: "", row: 0 }}
        >
          评测报告
        </Link>
        <Link
          className="underline text-sm"
          to="/kb/$kbId/eval/sets"
          params={{ kbId: String(id) }}
          search={{ cursor: 0, set: 0 }}
        >
          管理评测集
        </Link>
      </nav>
      <Link
        className="text-sm underline"
        to="/kb/$kbId/eval/run-compare"
        params={{ kbId: String(id) }}
        search={{ baseline: 0, current: 0, cursor, offset: 0 }}
      >
        对比两次运行
      </Link>
      <h1 className="text-3xl font-semibold break-words">
        {base.data?.name} · 运行历史
      </h1>
      <p className="text-sm text-muted-foreground">
        按运行编号升序排列，仅显示当前有权查看的运行。打开详情可跟踪状态；新增运行请从首页查看。
      </p>
      {base.data?.status === "archived" && (
        <p role="status">知识库已归档，历史仅供查看。</p>
      )}
      <Button
        variant="outline"
        disabled={list.isFetching}
        onClick={() => void list.refetch()}
      >
        刷新当前页
      </Button>
      {list.isPending ? (
        <p role="status">正在加载运行历史</p>
      ) : list.isError ? (
        <p role="alert">运行历史读取失败，请重试。未展示缓存列表。</p>
      ) : list.data?.items.length === 0 ? (
        <p>当前页没有可见运行。</p>
      ) : (
        <ul className="space-y-3" aria-label="运行历史列表">
          {list.data?.items.map((item) => (
            <li key={item.id} className="rounded-xl border p-5 space-y-2">
              <Link
                className="font-medium underline"
                to="/kb/$kbId/eval/runs"
                params={{ kbId: String(id) }}
                search={{
                  set: item.set.set_id,
                  revision: item.set.revision,
                  run: item.id,
                  row: 0,
                }}
              >
                运行 {item.id}
              </Link>
              <p>
                {evalRunStatusLabels[item.status]} · {item.mode} · 尝试{" "}
                {item.attempts}/{item.max_attempts}
              </p>
              <p>
                题集 {item.set.set_id} · 修订 {item.set.revision}
              </p>
              <p className="text-sm break-words text-muted-foreground">
                创建：{item.created_at}
              </p>
              {item.failure_code && (
                <p className="text-sm break-words">
                  上次失败：{item.failure_code}
                </p>
              )}
              <p className="text-sm">
                {item.result_available
                  ? "服务端执行结果可查看"
                  : "尚无成功结果"}
              </p>
            </li>
          ))}
        </ul>
      )}
      <nav className="flex flex-wrap gap-4" aria-label="运行历史分页">
        {cursor > 0 && (
          <Button
            variant="outline"
            disabled={list.isFetching}
            onClick={() => onCursor(0)}
          >
            返回历史首页
          </Button>
        )}
        {!list.isError && list.data?.next_cursor != null && (
          <Button
            variant="outline"
            disabled={list.isFetching}
            onClick={() => onCursor(list.data?.next_cursor ?? 0)}
          >
            下一页运行
          </Button>
        )}
      </nav>
    </section>
  );
}
