import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  cancelRebuild,
  createRebuild,
  getKnowledgeBase,
  getRebuild,
  type Identity,
  type RebuildInput,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { useState } from "react";

const labels = {
  queued: "排队中",
  running: "运行中",
  succeeded: "已完成",
  failed: "重建失败",
  dead: "重建失败",
  cancelled: "已取消",
};
const denied = (e: unknown) =>
  e instanceof ApiError && [401, 403, 404].includes(e.status);

export function RebuildPage({
  identity,
  id,
  job,
  onCreated,
}: {
  identity: Identity;
  id: number;
  job: number;
  onCreated: (job: number) => void;
}) {
  const [target, setTarget] = useState("");
  const [accessLost, setAccessLost] = useState(false);
  const [key] = useState(() => crypto.randomUUID());
  const [submitted, setSubmitted] = useState<RebuildInput>();
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const start = useMutation({
    mutationFn: (input: RebuildInput) =>
      createRebuild(id, input, identity.csrf_token, key),
    retry: false,
    onSuccess: (v) => onCreated(v.job_id),
  });
  const status = useQuery({
    queryKey: ["rebuild", identity.tenant_id, identity.user_id, id, job],
    queryFn: ({ signal }) => getRebuild(id, job, signal),
    enabled: admin && !base.isError && !accessLost && job > 0,
    retry: false,
    gcTime: 0,
    refetchInterval: (q) =>
      q.state.status !== "error" &&
      q.state.data &&
      ["queued", "running"].includes(q.state.data.status)
        ? 2000
        : false,
  });
  const cancel = useMutation({
    mutationFn: () => cancelRebuild(id, job, identity.csrf_token),
    onError: (error) => {
      if (denied(error)) setAccessLost(true);
    },
    retry: false,
    onSuccess: () => {
      void status.refetch();
    },
  });
  if (base.isPending) return <p role="status">正在加载知识库</p>;
  if (
    base.isError ||
    denied(start.error) ||
    denied(status.error) ||
    denied(cancel.error)
  )
    return (
      <p role="alert" className="py-10">
        知识库或重建任务已不可用，内容已隐藏。
      </p>
    );
  if (!admin) return <p role="alert">当前账号无重建权限。</p>;
  const archived =
    base.data.status === "archived" ||
    (start.error instanceof ApiError &&
      start.error.code === "knowledge_base_archived");
  const targetId = Number(target);
  const valid =
    /^[1-9]\d*$/.test(target) &&
    Number.isSafeInteger(targetId) &&
    targetId !== base.data.embedding_space_id;
  return (
    <section className="space-y-6 py-10">
      <Link
        className="underline text-sm"
        to="/kb/$kbId/documents"
        params={{ kbId: String(id) }}
        search={{ cursor: 0 }}
      >
        返回知识库
      </Link>
      <h1 className="text-3xl font-semibold break-words">
        {base.data.name} · 嵌入重建
      </h1>
      {archived && (
        <p role="status">
          知识库已归档，不能创建重建；已有任务仍可查看或取消。
        </p>
      )}
      {job === 0 ? (
        <form
          className="rounded-xl border p-5 space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (
              archived ||
              start.isPending ||
              start.isSuccess ||
              (!submitted && !valid)
            )
              return;
            const input = submitted ?? {
              target_space_id: targetId,
              revision: base.data.config_revision,
            };
            setSubmitted(input);
            start.mutate(input);
          }}
        >
          <p>
            当前空间 {base.data.embedding_space_id} · 配置修订{" "}
            {base.data.config_revision}
          </p>
          <p className="text-sm text-muted-foreground">
            输入管理员已配置的目标嵌入空间编号。重建完成并通过覆盖核验后才切换，处理中继续使用原空间。
          </p>
          <label className="block">
            目标空间编号
            <input
              className="mt-1 block w-full rounded-md border bg-background px-3 py-2"
              inputMode="numeric"
              value={target}
              disabled={archived || !!submitted}
              onChange={(e) => setTarget(e.target.value)}
            />
          </label>
          {start.isError && (
            <p role="alert">
              创建未确认或被拒绝（
              {start.error instanceof ApiError ? start.error.code : "网络错误"}
              ）。原样重试保留相同目标、修订及请求编号，避免重复入队。
            </p>
          )}
          <Button
            type="submit"
            disabled={
              archived ||
              start.isPending ||
              start.isSuccess ||
              (!submitted && !valid)
            }
          >
            {start.isPending
              ? "正在提交"
              : start.isError
                ? "原样重试重建"
                : "开始重建"}
          </Button>
        </form>
      ) : (
        <section
          aria-label="重建任务"
          className="rounded-xl border p-5 space-y-4"
        >
          <h2 className="text-lg font-medium">任务 {job}</h2>
          {status.isPending && <p role="status">正在读取任务状态</p>}
          {status.isError ? (
            <p role="alert">任务状态读取失败，不能确认当前进度，请刷新。</p>
          ) : (
            status.data && (
              <>
                <p role="status">{labels[status.data.status]}</p>
                <p>
                  源空间 {status.data.source_space_id} → 目标空间{" "}
                  {status.data.target_space_id}
                </p>
                <p>
                  源修订 {status.data.source_revision} · 尝试{" "}
                  {status.data.attempts}/{status.data.max_attempts}
                </p>
                <p className="text-sm">
                  创建时间：{new Date(status.data.created_at).toLocaleString()}
                </p>
                {status.data.finished_at && (
                  <p className="text-sm">
                    结束时间：
                    {new Date(status.data.finished_at).toLocaleString()}
                  </p>
                )}
                {status.data.status === "succeeded" ? (
                  <p>重建完成时已切换至目标空间。</p>
                ) : (
                  status.data.failure_code && (
                    <p className="break-all">
                      最近一次失败：{status.data.failure_code}
                    </p>
                  )
                )}
                {["queued", "running"].includes(status.data.status) && (
                  <Button
                    variant="outline"
                    disabled={cancel.isPending || cancel.isSuccess}
                    onClick={() => cancel.mutate()}
                  >
                    {cancel.isPending
                      ? "正在取消"
                      : cancel.isSuccess
                        ? "取消已确认"
                        : "取消重建"}
                  </Button>
                )}
              </>
            )
          )}
          {cancel.isError && <p role="alert">取消未确认，请刷新状态后重试。</p>}
          <Button
            className="ml-2"
            variant="outline"
            disabled={status.isFetching || cancel.isPending}
            onClick={() => void status.refetch()}
          >
            刷新状态
          </Button>
        </section>
      )}
    </section>
  );
}
