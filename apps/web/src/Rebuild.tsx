import { useMutation, useQuery } from "@tanstack/react-query";
import {
  ApiError,
  cancelRebuild,
  createRebuild,
  getKnowledgeBase,
  getRebuild,
  type Identity,
  listEmbeddingSpaces,
  type RebuildInput,
} from "@tuskrag/api-client";
import { Alert } from "@tuskrag/ui/alert";
import { Badge } from "@tuskrag/ui/badge";
import { Button } from "@tuskrag/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@tuskrag/ui/card";
import { Field } from "@tuskrag/ui/field";
import { Input } from "@tuskrag/ui/input";
import { SectionHeader } from "@tuskrag/ui/section-header";
import { ArrowRight, Loader2, RefreshCw } from "lucide-react";
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
  const spaces = useQuery({
    queryKey: ["embedding-spaces", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listEmbeddingSpaces(signal),
    enabled: admin,
    retry: false,
  });
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
  if (base.isPending)
    return (
      <p role="status" className="py-6 text-sm text-muted-foreground">
        正在加载知识库
      </p>
    );
  if (
    base.isError ||
    denied(start.error) ||
    denied(status.error) ||
    denied(cancel.error)
  )
    return (
      <p role="alert" className="py-6">
        知识库或重建任务已不可用，内容已隐藏。
      </p>
    );
  if (!admin)
    return (
      <p role="alert" className="py-6">
        当前账号无重建权限。
      </p>
    );
  const archived =
    base.data.status === "archived" ||
    (start.error instanceof ApiError &&
      start.error.code === "knowledge_base_archived");
  const targetId = Number(target);
  const valid =
    /^[1-9]\d*$/.test(target) &&
    Number.isSafeInteger(targetId) &&
    targetId !== base.data.embedding_space_id;
  const describe = (spaceId: number) => {
    const space = spaces.data?.find((item) => item.id === spaceId);
    return space
      ? `#${space.id} · ${space.provider_name} · ${space.model} · ${space.dimensions} 维`
      : `#${spaceId}`;
  };
  const candidates = (spaces.data ?? []).filter(
    (space) => space.id !== base.data.embedding_space_id,
  );
  return (
    <section className="space-y-5">
      <SectionHeader title={`${base.data.name} · 嵌入重建`} />
      {archived && (
        <Alert variant="warning">
          知识库已归档，不能创建重建；已有任务仍可查看或取消。
        </Alert>
      )}
      {job === 0 ? (
        <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          <Card>
            <CardHeader>
              <CardTitle>创建重建任务</CardTitle>
              <CardDescription>
                重建完成并通过覆盖核验后才切换，处理中继续使用原空间。
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form
                className="space-y-4"
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
                <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 rounded-lg bg-muted/40 px-3 py-2.5 text-sm">
                  <dt className="text-muted-foreground">当前空间</dt>
                  <dd className="break-words">
                    {describe(base.data.embedding_space_id)}
                  </dd>
                  <dt className="text-muted-foreground">配置修订</dt>
                  <dd>{base.data.config_revision}</dd>
                </dl>
                <Field
                  htmlFor="rebuild-target"
                  label="目标空间编号"
                  hint="填写管理员已配置的嵌入空间编号，不能与当前空间相同。"
                >
                  <Input
                    id="rebuild-target"
                    inputMode="numeric"
                    className="max-w-48"
                    value={target}
                    disabled={archived || !!submitted}
                    onChange={(e) => setTarget(e.target.value)}
                  />
                </Field>
                {start.isError && (
                  <Alert variant="danger">
                    创建未确认或被拒绝（
                    {start.error instanceof ApiError
                      ? start.error.code
                      : "网络错误"}
                    ）。原样重试保留相同目标、修订及请求编号，避免重复入队。
                  </Alert>
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
            </CardContent>
          </Card>
          {candidates.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>可用嵌入空间</CardTitle>
              </CardHeader>
              <CardContent>
                <ul className="divide-y rounded-lg border text-sm">
                  {candidates.map((space) => (
                    <li
                      key={space.id}
                      className="flex items-center justify-between gap-3 px-3 py-2"
                    >
                      <span className="min-w-0 break-words">
                        {describe(space.id)}
                      </span>
                      <Button
                        type="button"
                        size="xs"
                        variant={
                          target === String(space.id) ? "secondary" : "outline"
                        }
                        disabled={archived || !!submitted}
                        aria-label={`选用空间 ${space.id}`}
                        onClick={() => setTarget(String(space.id))}
                      >
                        {target === String(space.id) ? "已选用" : "选用"}
                      </Button>
                    </li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          )}
        </div>
      ) : (
        <Card className="max-w-3xl">
          <section aria-label="重建任务" className="space-y-4 p-5">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <h2 className="text-base font-semibold">任务 {job}</h2>
              {!status.isError && status.data && (
                <Badge
                  role="status"
                  variant={
                    status.data.status === "succeeded"
                      ? "success"
                      : ["failed", "dead"].includes(status.data.status)
                        ? "danger"
                        : "neutral"
                  }
                >
                  {["queued", "running"].includes(status.data.status) && (
                    <Loader2
                      aria-hidden="true"
                      className="h-3 w-3 animate-spin"
                    />
                  )}
                  {labels[status.data.status]}
                </Badge>
              )}
            </div>
            {status.isPending && (
              <p role="status" className="text-sm text-muted-foreground">
                正在读取任务状态
              </p>
            )}
            {status.isError ? (
              <Alert variant="danger">
                任务状态读取失败，不能确认当前进度，请刷新。
              </Alert>
            ) : (
              status.data && (
                <>
                  <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2 text-sm">
                    <dt className="text-muted-foreground">空间切换</dt>
                    <dd className="flex flex-wrap items-center gap-1.5 break-words">
                      源空间 {status.data.source_space_id}
                      <ArrowRight
                        aria-hidden="true"
                        className="size-3.5 text-muted-foreground"
                      />
                      目标空间 {status.data.target_space_id}
                    </dd>
                    <dt className="text-muted-foreground">源修订</dt>
                    <dd>{status.data.source_revision}</dd>
                    <dt className="text-muted-foreground">尝试次数</dt>
                    <dd>
                      {status.data.attempts}/{status.data.max_attempts}
                    </dd>
                    <dt className="text-muted-foreground">创建时间</dt>
                    <dd>{new Date(status.data.created_at).toLocaleString()}</dd>
                    {status.data.finished_at && (
                      <>
                        <dt className="text-muted-foreground">结束时间</dt>
                        <dd>
                          {new Date(status.data.finished_at).toLocaleString()}
                        </dd>
                      </>
                    )}
                  </dl>
                  {status.data.status === "succeeded" ? (
                    <Alert>重建完成时已切换至目标空间。</Alert>
                  ) : (
                    status.data.failure_code && (
                      <p className="break-all text-sm text-danger">
                        最近一次失败：{status.data.failure_code}
                      </p>
                    )
                  )}
                </>
              )
            )}
            {cancel.isError && (
              <Alert variant="danger">取消未确认，请刷新状态后重试。</Alert>
            )}
            <div className="flex flex-wrap gap-2 border-t pt-4">
              <Button
                variant="outline"
                disabled={status.isFetching || cancel.isPending}
                onClick={() => void status.refetch()}
              >
                <RefreshCw aria-hidden="true" className="h-3.5 w-3.5" />
                刷新状态
              </Button>
              {!status.isError &&
                status.data &&
                ["queued", "running"].includes(status.data.status) && (
                  <Button
                    variant="outline"
                    className="text-danger"
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
            </div>
          </section>
        </Card>
      )}
    </section>
  );
}
