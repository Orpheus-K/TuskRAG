import { Label } from "@tuskrag/ui/label";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  type EvalRunInput,
  getEvalRun,
  getEvalRunResult,
  getKnowledgeBase,
  type Identity,
  startEvalRun,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { Select, SelectItem } from "@tuskrag/ui/select";
import { useState } from "react";
import { ReportDetail } from "./Evaluation";

const denied = (e: unknown) =>
  e instanceof ApiError && [401, 403, 404].includes(e.status);
export const evalRunStatusLabels = {
  queued: "排队中",
  running: "运行中",
  succeeded: "已完成",
  failed: "运行失败",
  dead: "运行失败",
  cancelled: "已取消",
};
export function EvaluationRun({
  identity,
  id,
  selection,
  onChange,
}: {
  identity: Identity;
  id: number;
  selection: { set: number; revision: number; run: number; row: number };
  onChange: (v: { run?: number; row?: number }) => void;
}) {
  const { set, revision, run, row } = selection;
  const [mode, setMode] = useState<EvalRunInput["mode"]>("rrf");
  const [key] = useState(() => crypto.randomUUID());
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const start = useMutation({
    mutationFn: (input: EvalRunInput) =>
      startEvalRun(id, input, identity.csrf_token, key),
    retry: false,
    onSuccess: (v) => onChange({ run: v.id, row: 0 }),
  });
  const scope = ["eval-run", identity.tenant_id, identity.user_id, id, run];
  const status = useQuery({
    queryKey: [...scope, "status"],
    queryFn: ({ signal }) => getEvalRun(id, run, signal),
    enabled: admin && !base.isError && run > 0,
    retry: false,
    gcTime: 0,
    refetchInterval: (q) =>
      q.state.status !== "error" &&
      q.state.data &&
      ["queued", "running"].includes(q.state.data.status)
        ? 2000
        : false,
  });
  const result = useQuery({
    queryKey: [...scope, "result"],
    queryFn: ({ signal }) => getEvalRunResult(id, run, signal),
    enabled:
      admin &&
      !base.isError &&
      !status.isError &&
      status.data?.status === "succeeded",
    retry: false,
    gcTime: 0,
  });
  if (base.isPending) return <p role="status">正在加载知识库</p>;
  if (
    base.isError ||
    denied(start.error) ||
    denied(status.error) ||
    denied(result.error)
  )
    return (
      <p role="alert" className="py-10">
        知识库、运行或证据已不可用，内容已隐藏。
      </p>
    );
  if (!admin) return <p role="alert">当前账号无评测运行权限。</p>;
  const archived =
    base.data?.status === "archived" ||
    (start.error instanceof ApiError &&
      start.error.code === "knowledge_base_archived");
  return (
    <section className="space-y-6 py-10">
      <Link
        className="underline text-sm"
        to="/kb/$kbId/eval/sets"
        params={{ kbId: String(id) }}
        search={{ cursor: 0, set }}
      >
        返回评测集
      </Link>
      <Link
        className="ml-4 underline text-sm"
        to="/kb/$kbId/eval/history"
        params={{ kbId: String(id) }}
        search={{ cursor: 0 }}
      >
        运行历史
      </Link>
      <h1 className="text-3xl font-semibold break-words">
        {base.data?.name} · 评测运行
      </h1>
      {archived && <p role="status">知识库已归档，仅供查看。</p>}
      {run === 0 ? (
        <section className="rounded-xl border p-5 space-y-4">
          <p>
            题集 {set} · 固定修订 {revision}
          </p>
          <p className="text-sm text-muted-foreground">
            执行已保存的题集修订。文档范围和配置由服务器确定；编辑页未保存的草稿不参与运行。
          </p>
          <Label className="block space-y-1 text-sm">
            <span>检索模式</span>
            <Select
              value={mode}
              disabled={
                archived || start.isPending || start.isError || start.isSuccess
              }
              onValueChange={(value) => {
                const v = value;
                if (
                  v === "lexical" ||
                  v === "vector" ||
                  v === "rrf" ||
                  v === "rerank"
                )
                  setMode(v);
              }}
            >
              {["lexical", "vector", "rrf", "rerank"].map((v) => (
                <SelectItem key={v} value={v}>
                  {v}
                </SelectItem>
              ))}
            </Select>
          </Label>
          {start.isError && (
            <p role="alert">
              {start.error instanceof ApiError &&
              start.error.code === "rerank_unavailable"
                ? "请先在知识库配置中保存重排模型，再原样重试。"
                : archived
                  ? "归档知识库无法创建运行。"
                  : "创建未确认或被拒绝。模式已锁定，原样重试沿用同一请求，避免重复入队。"}
            </p>
          )}
          <Button
            disabled={archived || start.isPending || start.isSuccess}
            onClick={() =>
              start.mutate(start.variables ?? { set_id: set, revision, mode })
            }
          >
            {start.isPending
              ? "正在创建"
              : start.isError
                ? "原样重试运行"
                : "开始运行"}
          </Button>
        </section>
      ) : (
        <>
          <section
            className="rounded-xl border p-5 space-y-3"
            aria-label="运行状态"
          >
            <h2 className="text-xl font-medium">运行 {run}</h2>
            {status.isPending ? (
              <p role="status">正在读取状态</p>
            ) : status.isError ? (
              <p role="alert">状态读取失败，自动轮询已停止，请重试。</p>
            ) : (
              status.data && (
                <>
                  <p role="status">{evalRunStatusLabels[status.data.status]}</p>
                  <p>
                    题集 {status.data.set.set_id} · 修订{" "}
                    {status.data.set.revision} · {status.data.mode}
                  </p>
                  <p>
                    尝试 {status.data.attempts} / {status.data.max_attempts}
                  </p>
                  {status.data.failure_code && (
                    <p className="break-words">
                      上次失败：{status.data.failure_code}
                    </p>
                  )}
                  {status.data.finished_at && (
                    <p>结束时间：{status.data.finished_at}</p>
                  )}
                </>
              )
            )}
            <Button
              variant="outline"
              disabled={status.isFetching}
              onClick={() => void status.refetch()}
            >
              刷新状态
            </Button>
          </section>
          {!status.isError &&
            status.data?.status === "succeeded" &&
            (result.isPending ? (
              <p role="status">正在读取执行结果</p>
            ) : result.isError ? (
              <section>
                <p role="alert">结果读取失败，未展示缓存报告。</p>
                <Button onClick={() => void result.refetch()}>
                  重试读取结果
                </Button>
              </section>
            ) : (
              result.data && (
                <ReportDetail
                  data={result.data}
                  id={id}
                  cursor=""
                  rowPage={row}
                  onPage={(row) => onChange({ row })}
                />
              )
            ))}
        </>
      )}
    </section>
  );
}
