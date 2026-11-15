import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  type EvalMetrics,
  type EvalSummary,
  getEvalReport,
  getKnowledgeBase,
  type Identity,
  importEvalReport,
  isEvalReport,
  listEvalReports,
  type StoredEvalReport,
} from "@tuskrag/api-client";
import { Alert } from "@tuskrag/ui/alert";
import { Badge } from "@tuskrag/ui/badge";
import { Button } from "@tuskrag/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@tuskrag/ui/collapsible";
import { Dialog } from "@tuskrag/ui/dialog";
import { EmptyState } from "@tuskrag/ui/empty-state";
import { Field } from "@tuskrag/ui/field";
import { Input } from "@tuskrag/ui/input";
import { SectionHeader } from "@tuskrag/ui/section-header";
import { WorkspaceSplit } from "@tuskrag/ui/workspace-split";
import {
  GitCompare,
  History,
  ListChecks,
  MessageSquareText,
  RefreshCw,
  Upload,
} from "lucide-react";
import { type FormEvent, useState } from "react";

function errorText(error: unknown) {
  if (error instanceof ApiError) {
    if (error.status === 401) return "登录已过期，请重新登录。";
    if (error.status === 403) return "当前账号无评测报告权限。";
    if (error.status === 404) return "知识库、报告或证据已不可见，请刷新列表。";
    if (error.code === "knowledge_base_archived")
      return "知识库已归档，不能导入报告。";
    if (error.status === 400 || error.status === 422)
      return "报告不完整或指标不一致，请检查原始 JSON。";
  }
  return "操作未确认，请检查文件与服务状态后重试。";
}

const metrics: [keyof EvalMetrics, string][] = [
  ["RecallAt20", "Recall@20"],
  ["HitAt5", "Hit@5"],
  ["MRRAt10", "MRR@10"],
  ["NDCGAt10", "nDCG@10"],
];
function Metrics({ value }: { value: EvalMetrics | null }) {
  if (value === null)
    return (
      <p className="text-sm text-muted-foreground">
        无答案题，不计算检索相关性指标。
      </p>
    );
  return (
    <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      {metrics.map(([key, label]) => (
        <div key={key}>
          <dt className="text-xs text-muted-foreground">{label}</dt>
          <dd className="font-mono">{value[key].toFixed(4)}</dd>
        </div>
      ))}
    </dl>
  );
}
function Summary({ value }: { value: EvalSummary }) {
  return (
    <div className="space-y-3">
      <p className="text-sm">
        {value.Cases} 题 · 有答案 {value.AnswerableCases} · 无答案{" "}
        {value.UnanswerableCases}
      </p>
      <Metrics value={value.Metrics} />
    </div>
  );
}

export function Evaluation({
  identity,
  id,
  cursor,
  digest,
  rowPage,
  onSelect,
}: {
  identity: Identity;
  id: number;
  cursor: string;
  digest: string;
  rowPage: number;
  onSelect: (digest: string) => void;
}) {
  const client = useQueryClient();
  const [importing, setImporting] = useState(false);
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
  const detail = useQuery({
    queryKey: [...scope, "report", digest],
    queryFn: ({ signal }) => getEvalReport(id, digest, signal),
    enabled: admin && digest !== "",
    retry: false,
    gcTime: 0,
  });
  const upload = useMutation({
    mutationFn: async (file: File) => {
      if (file.size === 0 || file.size > 8 * 1024 * 1024)
        throw new Error("report file size");
      const data: unknown = JSON.parse(await file.text());
      if (!isEvalReport(data)) throw new Error("report file format");
      return importEvalReport(id, data, identity.csrf_token);
    },
    retry: false,
    onSuccess: async (result) => {
      client.setQueryData([...scope, "report", result.digest], result);
      await client.invalidateQueries({ queryKey: [...scope, "list"] });
      setImporting(false);
      onSelect(result.digest);
    },
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (base.data?.status !== "active" || !admin || upload.isPending) return;
    const file = new FormData(event.currentTarget).get("report");
    if (file instanceof File) upload.mutate(file);
  }
  const failure = base.error || list.error || detail.error;
  const revoked =
    upload.error instanceof ApiError &&
    [401, 403, 404].includes(upload.error.status);
  if (failure || revoked)
    return (
      <section className="space-y-4 py-6">
        <p role="alert">{errorText(failure || upload.error)}</p>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            onClick={() => {
              upload.reset();
              void base.refetch();
              if (admin) {
                void list.refetch();
                if (digest) void detail.refetch();
              }
            }}
          >
            重新加载报告
          </Button>
          <Button asChild variant="outline">
            <Link
              to="/kb/$kbId/eval"
              params={{ kbId: String(id) }}
              search={{ cursor: "", digest: "", row: 0 }}
              onClick={() => {
                upload.reset();
                void client.invalidateQueries({ queryKey: scope });
              }}
            >
              返回报告列表
            </Link>
          </Button>
          <Button asChild variant="ghost">
            <Link to="/">返回知识库</Link>
          </Button>
        </div>
      </section>
    );
  if (base.isPending)
    return (
      <p role="status" className="py-6 text-sm text-muted-foreground">
        正在加载知识库
      </p>
    );
  if (!admin)
    return (
      <p role="alert" className="py-6">
        当前账号无评测报告权限。
      </p>
    );
  const archived = base.data?.status === "archived";
  const listPane = (
    <div className="space-y-3">
      {list.isPending ? (
        <p role="status" className="text-sm text-muted-foreground">
          正在加载报告列表
        </p>
      ) : list.data?.items?.length === 0 ? (
        <p className="text-sm text-muted-foreground">暂无可见报告。</p>
      ) : (
        <ul className="space-y-2" aria-label="报告列表">
          {list.data?.items?.map((item) => {
            const selected = item.digest === digest;
            return (
              <li key={item.digest}>
                <Link
                  to="/kb/$kbId/eval"
                  params={{ kbId: String(id) }}
                  search={{ cursor, digest: item.digest, row: 0 }}
                  aria-current={selected ? "page" : undefined}
                  className={`block space-y-2 rounded-lg border p-3 transition-colors hover:border-foreground/25 ${
                    selected ? "border-foreground/40 bg-muted/60" : ""
                  }`}
                >
                  <span className="block break-words text-sm font-medium">
                    {item.dataset_version} · {item.mode} · {item.model_label}
                  </span>
                  <span className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                    <Badge>外部导入</Badge>
                    <Badge
                      variant={
                        item.model_kind === "stub" ? "warning" : "neutral"
                      }
                    >
                      {item.model_kind === "stub" ? "模型替身" : "真实模型标记"}
                    </Badge>
                    <span className="break-all">
                      {item.created_at} · {item.digest.slice(0, 12)}
                    </span>
                  </span>
                  <Summary value={item.summary} />
                </Link>
              </li>
            );
          })}
        </ul>
      )}
      {list.data?.next_cursor && (
        <Button asChild size="xs" variant="outline">
          <Link
            to="/kb/$kbId/eval"
            params={{ kbId: String(id) }}
            search={{ cursor: list.data.next_cursor, digest: "", row: 0 }}
          >
            下一页报告
          </Link>
        </Button>
      )}
    </div>
  );
  const detailPane = !digest ? (
    <EmptyState
      title="未选择报告"
      description="从报告列表中选择一份报告查看指标与逐题结果。"
    />
  ) : detail.isPending ? (
    <p role="status" className="text-sm text-muted-foreground">
      正在加载逐题结果
    </p>
  ) : detail.data ? (
    <ReportDetail
      key={digest}
      data={detail.data}
      id={id}
      cursor={cursor}
      rowPage={rowPage}
    />
  ) : null;
  return (
    <section className="space-y-5">
      <SectionHeader
        title={`${base.data?.name} · 评测报告`}
        actions={
          <>
            <Button asChild size="sm" variant="ghost">
              <Link
                to="/kb/$kbId/eval/history"
                params={{ kbId: String(id) }}
                search={{ cursor: 0 }}
              >
                <History aria-hidden="true" className="h-3.5 w-3.5" />
                运行历史
              </Link>
            </Button>
            <Button asChild size="sm" variant="ghost">
              <Link
                to="/kb/$kbId/eval/sets"
                params={{ kbId: String(id) }}
                search={{ cursor: 0, set: 0 }}
              >
                <ListChecks aria-hidden="true" className="h-3.5 w-3.5" />
                管理评测集
              </Link>
            </Button>
            <Button asChild size="sm" variant="ghost">
              <Link
                to="/kb/$kbId/answer-evaluation"
                params={{ kbId: String(id) }}
                search={{ cursor: "", digest: "", row: 0 }}
              >
                <MessageSquareText aria-hidden="true" className="h-3.5 w-3.5" />
                生成评测
              </Link>
            </Button>
            <Button asChild size="sm" variant="outline">
              <Link
                to="/kb/$kbId/eval/compare"
                params={{ kbId: String(id) }}
                search={{
                  baseline: digest,
                  current: "",
                  cursor: "",
                  offset: 0,
                }}
              >
                <GitCompare aria-hidden="true" className="h-3.5 w-3.5" />
                对比两份报告
              </Link>
            </Button>
            {!archived && (
              <Dialog
                open={importing}
                onOpenChange={(open) => {
                  if (upload.isPending) return;
                  setImporting(open);
                  if (!open) upload.reset();
                }}
                title="导入评测报告"
                description="外部导入的报告未经服务端验证；相同内容自动去重。"
                trigger={
                  <Button size="sm">
                    <Upload aria-hidden="true" className="h-3.5 w-3.5" />
                    导入报告
                  </Button>
                }
              >
                <form className="space-y-4" onSubmit={submit}>
                  <Field
                    htmlFor="eval-file"
                    label="完整报告 JSON（最大 8 MiB）"
                  >
                    <Input
                      id="eval-file"
                      name="report"
                      type="file"
                      accept=".json,application/json"
                      required
                      disabled={upload.isPending}
                    />
                  </Field>
                  {upload.isError && (
                    <Alert variant="danger">{errorText(upload.error)}</Alert>
                  )}
                  <div className="flex justify-end gap-2 border-t pt-4">
                    <Button
                      type="button"
                      variant="outline"
                      disabled={upload.isPending}
                      onClick={() => {
                        setImporting(false);
                        upload.reset();
                      }}
                    >
                      取消
                    </Button>
                    <Button type="submit" disabled={upload.isPending}>
                      {upload.isPending ? "正在导入" : "开始导入"}
                    </Button>
                  </div>
                </form>
              </Dialog>
            )}
          </>
        }
      />
      {archived && (
        <Alert variant="warning">知识库已归档，报告仅供查看。</Alert>
      )}
      {upload.isSuccess && <Alert>报告已导入。相同内容自动去重。</Alert>}
      <WorkspaceSplit
        className="lg:min-h-[calc(100dvh-11rem)]"
        panes={[
          {
            id: "eval-reports",
            title: "可见报告",
            defaultWidth: 380,
            minWidth: 300,
            maxWidth: 560,
            collapsible: true,
            actions: (
              <Button asChild size="xs" variant="ghost">
                <Link
                  to="/kb/$kbId/eval"
                  params={{ kbId: String(id) }}
                  search={{ cursor: "", digest: "", row: 0 }}
                  onClick={() => {
                    void client.invalidateQueries({ queryKey: scope });
                    upload.reset();
                  }}
                >
                  <RefreshCw aria-hidden="true" className="h-3.5 w-3.5" />
                  刷新列表
                </Link>
              </Button>
            ),
            content: listPane,
          },
          { id: "eval-detail", title: "选中报告", content: detailPane },
        ]}
        storageKey={`tuskrag.eval.${identity.tenant_id}`}
      />
    </section>
  );
}

export function ReportDetail({
  data,
  id,
  cursor,
  rowPage,
  onPage,
}: {
  data: StoredEvalReport;
  onPage?: (page: number) => void;
  id: number;
  cursor: string;
  rowPage: number;
}) {
  const report = data.report;
  const rows = report.rows ?? [];
  const start = rowPage * 20;
  return (
    <section className="space-y-4" aria-label="报告详情">
      <h2 className="text-lg font-semibold">逐题结果</h2>
      <p className="break-words text-sm">
        {report.dataset_version} · {report.model_label} · {report.mode} ·
        {data.source === "executed" ? "服务端执行" : "外部导入"}
      </p>
      <Summary value={report.summary} />
      <Collapsible>
        <CollapsibleTrigger className="cursor-pointer text-sm">
          配置摘要与阶段耗时
        </CollapsibleTrigger>
        <CollapsibleContent>
          <p className="mt-3 break-all text-xs">
            数据集 SHA-256：{report.dataset_sha256}
            <br />
            配置 SHA-256：{report.config_sha256}
          </p>
          <ul className="mt-3 text-sm">
            {Object.entries(report.latency_ms_p50_p95).map(([stage, pair]) => (
              <li key={stage} className="break-words">
                {stage}：p50 {pair[0]?.toFixed(2)} ms / p95{" "}
                {pair[1]?.toFixed(2)} ms
              </li>
            ))}
          </ul>
        </CollapsibleContent>
      </Collapsible>
      <p className="text-xs text-muted-foreground">
        报告仅含题号及证据标识，不含问题与文档正文。未实现的引用、拒答指标不显示为零。
      </p>
      {start >= rows.length ? (
        <p role="alert">逐题页码超出范围，请返回第一页。</p>
      ) : (
        rows.slice(start, start + 20).map((row) => (
          <Collapsible key={row.id} className="rounded-lg border p-3">
            <CollapsibleTrigger className="cursor-pointer break-words">
              {row.id} · {row.metrics === null ? "无答案题" : "有答案题"}
              {row.degraded?.length ? " · 存在降级" : ""}
            </CollapsibleTrigger>
            <CollapsibleContent>
              <div className="mt-3 space-y-3">
                <Metrics value={row.metrics} />
                {Boolean(row.degraded?.length) && (
                  <p className="break-words text-sm">
                    降级：{row.degraded?.join("、")}
                  </p>
                )}
                <p className="text-sm">
                  总耗时：{row.timings_ms.total?.toFixed(2)} ms
                </p>
                {(["fused", "ranked"] as const).map((kind) => (
                  <div key={kind}>
                    <h3 className="text-sm font-medium">
                      {kind === "fused" ? "融合候选" : "最终排序"}
                    </h3>
                    {!row[kind]?.length ? (
                      <p className="text-sm">无证据</p>
                    ) : (
                      <p className="whitespace-pre-wrap break-words text-sm">
                        {row[kind]
                          ?.map(
                            (e, index) =>
                              `${index + 1}. 文档 ${e.DocumentID} · 节 ${e.SectionID} · 块 ${e.ChunkID}${e.PageStart > 0 ? ` · 页 ${e.PageStart}–${e.PageEnd}` : " · 无页码"}`,
                          )
                          .join("\n")}
                      </p>
                    )}
                  </div>
                ))}
              </div>
            </CollapsibleContent>
          </Collapsible>
        ))
      )}
      {onPage ? (
        <nav aria-label="逐题分页" className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={rowPage === 0}
            onClick={() => onPage(0)}
          >
            逐题第一页
          </Button>
          <Button
            variant="outline"
            disabled={rowPage === 0}
            onClick={() => onPage(rowPage - 1)}
          >
            上一页题目
          </Button>
          <Button
            variant="outline"
            disabled={start + 20 >= rows.length}
            onClick={() => onPage(rowPage + 1)}
          >
            下一页题目
          </Button>
        </nav>
      ) : (
        <nav aria-label="逐题分页" className="flex flex-wrap gap-2">
          {rowPage > 0 && (
            <Button asChild size="xs" variant="outline">
              <Link
                to="/kb/$kbId/eval"
                params={{ kbId: String(id) }}
                search={{ cursor, digest: data.digest, row: 0 }}
              >
                逐题第一页
              </Link>
            </Button>
          )}
          {rowPage > 0 && (
            <Button asChild size="xs" variant="outline">
              <Link
                to="/kb/$kbId/eval"
                params={{ kbId: String(id) }}
                search={{ cursor, digest: data.digest, row: rowPage - 1 }}
              >
                上一页题目
              </Link>
            </Button>
          )}
          {start + 20 < rows.length && (
            <Button asChild size="xs" variant="outline">
              <Link
                to="/kb/$kbId/eval"
                params={{ kbId: String(id) }}
                search={{ cursor, digest: data.digest, row: rowPage + 1 }}
              >
                下一页题目
              </Link>
            </Button>
          )}
        </nav>
      )}
    </section>
  );
}
