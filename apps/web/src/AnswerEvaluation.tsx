import { Label } from "@tuskrag/ui/label";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  type AnswerEvalMetrics,
  ApiError,
  getAnswerEvalReport,
  getKnowledgeBase,
  type Identity,
  importAnswerEvalReport,
  isAnswerEvalReport,
  listAnswerEvalReports,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { Input } from "@tuskrag/ui/input";
import type { FormEvent } from "react";

function errorText(error: unknown) {
  if (error instanceof ApiError) {
    if (error.status === 401 || error.status === 403)
      return "当前账号无权查看或导入生成评测报告。";
    if (error.status === 404) return "知识库或生成评测报告不存在或已失效。";
    if (error.status === 409) return "知识库已归档，无法导入新报告。";
    if (error.status === 400 || error.status === 422)
      return "仅支持导入完整的 E6 生成评测 JSON 报告。";
    return `请求失败（${error.code} · 追踪 ${error.traceId || "无"}）。`;
  }
  if (error instanceof Error && error.message === "report file size")
    return "报告文件大小需在 1 B 到 8 MiB 之间。";
  if (
    error instanceof SyntaxError ||
    (error instanceof Error && error.message === "report file format")
  )
    return "请选择结构完整的 E6 生成评测 JSON 文件。";
  return "加载或导入生成评测报告失败。";
}

function formatRate(value: number | null) {
  return value === null ? "不适用" : value.toFixed(4);
}

const metricRows: [keyof AnswerEvalMetrics, string][] = [
  ["valid_citation_rate", "Valid Citation"],
  ["source_integrity_rate", "Source Integrity"],
  ["evidence_hit_rate", "Evidence Hit"],
  ["deterministic_pass_rate", "Deterministic Pass"],
  ["refusal_correct_rate", "Refusal Correct"],
  ["false_refusal_rate", "False Refusal"],
  ["unsupported_rate", "Unsupported Rate"],
  ["citation_support_rate", "Citation Support"],
];

export function AnswerEvaluation({
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
  const scope = ["answer-eval", identity.tenant_id, identity.user_id, id];
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const list = useQuery({
    queryKey: [...scope, "list", cursor],
    queryFn: ({ signal }) => listAnswerEvalReports(id, cursor, signal),
    enabled: admin,
    retry: false,
    gcTime: 0,
  });
  const detail = useQuery({
    queryKey: [...scope, "report", digest],
    queryFn: ({ signal }) => getAnswerEvalReport(id, digest, signal),
    enabled: admin && digest !== "",
    retry: false,
    gcTime: 0,
  });
  const upload = useMutation({
    mutationFn: async (file: File) => {
      if (file.size === 0 || file.size > 8 * 1024 * 1024)
        throw new Error("report file size");
      const data: unknown = JSON.parse(await file.text());
      if (!isAnswerEvalReport(data)) throw new Error("report file format");
      return importAnswerEvalReport(id, data, identity.csrf_token);
    },
    retry: false,
    onSuccess: async (result) => {
      client.setQueryData([...scope, "report", result.digest], result);
      await client.invalidateQueries({ queryKey: [...scope, "list"] });
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
      <section className="space-y-4 py-12">
        <p role="alert">{errorText(failure || upload.error)}</p>
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
        <Link
          to="/kb/$kbId/answer-evaluation"
          params={{ kbId: String(id) }}
          search={{ cursor: "", digest: "", row: 0 }}
          onClick={() => {
            upload.reset();
            void client.invalidateQueries({ queryKey: scope });
          }}
        >
          返回生成评测列表
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
        当前账号无生成评测报告权限。
      </p>
    );

  const report = detail.data?.report;
  const rows = report?.rows ?? [];
  const totalPages = Math.max(1, Math.ceil(rows.length / 20));
  const safePage = Math.min(rowPage, totalPages - 1);
  const pageRows = rows.slice(safePage * 20, (safePage + 1) * 20);

  return (
    <section className="space-y-6 py-12">
      <div className="flex flex-wrap gap-4 text-sm">
        <Link
          to="/kb/$kbId/documents"
          params={{ kbId: String(id) }}
          search={{ cursor: 0 }}
          className="underline"
        >
          返回文档
        </Link>
        <Link
          to="/kb/$kbId/eval"
          params={{ kbId: String(id) }}
          search={{ cursor: "", digest: "", row: 0 }}
          className="underline"
        >
          检索评测
        </Link>
        <Link
          to="/kb/$kbId/answer-evaluation/compare"
          params={{ kbId: String(id) }}
          search={{ baseline: digest, current: "", cursor: "", offset: 0 }}
          className="underline"
        >
          生成对比
        </Link>
      </div>
      <h1 className="break-words text-3xl font-semibold">
        {base.data?.name} · 生成评测
      </h1>
      {base.data?.status === "archived" ? (
        <p role="status">知识库已归档，报告仅供查看。</p>
      ) : (
        <form className="space-y-3 rounded-xl border p-5" onSubmit={submit}>
          <Label className="block text-sm" htmlFor="answer-eval-file">
            完整 E6 生成评测报告 JSON（最大 8 MiB）
          </Label>
          <Input
            id="answer-eval-file"
            name="report"
            type="file"
            accept=".json,application/json"
            required
            disabled={upload.isPending}
          />
          <Button type="submit" disabled={upload.isPending}>
            {upload.isPending ? "正在导入" : "导入生成报告"}
          </Button>
          {upload.isError && <p role="alert">{errorText(upload.error)}</p>}
          {upload.isSuccess && (
            <p role="status">生成评测报告已导入。相同内容自动去重。</p>
          )}
        </form>
      )}

      <div className="flex flex-wrap items-center gap-4">
        <h2 className="text-xl font-medium">可见生成报告</h2>
        <Link
          to="/kb/$kbId/answer-evaluation"
          params={{ kbId: String(id) }}
          search={{ cursor: "", digest: "", row: 0 }}
          onClick={() => {
            void client.invalidateQueries({ queryKey: scope });
            upload.reset();
          }}
          className="text-sm underline"
        >
          刷新首页
        </Link>
      </div>

      {list.isPending ? (
        <p role="status">正在加载生成报告列表</p>
      ) : list.data?.items?.length === 0 ? (
        <p>暂无可见生成评测报告。</p>
      ) : (
        <ul className="space-y-3">
          {list.data?.items?.map((item) => (
            <li key={item.digest} className="rounded-xl border p-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="space-y-1 text-sm">
                  <p className="font-medium">
                    {item.dataset_version} · {item.model_label} (
                    {item.model_kind}) · {item.prompt_version}
                  </p>
                  <p className="text-muted-foreground">
                    样本 {item.summary.cases} · Valid Citation{" "}
                    {formatRate(item.summary.metrics.valid_citation_rate)} ·
                    Source Integrity{" "}
                    {formatRate(item.summary.metrics.source_integrity_rate)} ·
                    Unsupported{" "}
                    {formatRate(item.summary.metrics.unsupported_rate)}
                  </p>
                </div>
                <Link
                  to="/kb/$kbId/answer-evaluation"
                  params={{ kbId: String(id) }}
                  search={{ cursor, digest: item.digest, row: 0 }}
                  className="text-sm underline"
                >
                  查看详情
                </Link>
              </div>
            </li>
          ))}
        </ul>
      )}

      {list.data?.next_cursor && (
        <Link
          to="/kb/$kbId/answer-evaluation"
          params={{ kbId: String(id) }}
          search={{ cursor: list.data.next_cursor, digest: "", row: 0 }}
          className="inline-block text-sm underline"
        >
          下一页报告
        </Link>
      )}

      {digest !== "" && detail.isPending && (
        <p role="status">正在加载生成评测详情</p>
      )}

      {report && (
        <section className="space-y-4 rounded-xl border p-5">
          <div className="space-y-1">
            <h2 className="text-xl font-semibold">
              报告详情 · {report.dataset_version}
            </h2>
            <p className="text-sm text-muted-foreground">
              模型：{report.model_label} ({report.model_kind}) · Prompt：
              {report.prompt_version} · 状态：{report.status} · 样本：
              {report.summary.cases}（可答 {report.summary.answerable_cases} /
              拒答 {report.summary.unanswerable_cases}）
            </p>
            <p className="text-sm text-muted-foreground">
              上下文预算：{report.assembly.context_budget_tokens} tokens ·
              父段落回溯：
              {report.assembly.parent_backtrack ? "开启" : "关闭"}
            </p>
          </div>
          <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            {metricRows.map(([key, label]) => (
              <div key={key} className="rounded-lg border p-3">
                <dt className="text-sm font-medium">{label}</dt>
                <dd className="mt-1 font-mono text-lg">
                  {formatRate(report.summary.metrics[key])}
                </dd>
              </div>
            ))}
          </dl>
          <div className="space-y-2">
            <h3 className="text-lg font-medium">
              逐题结果（第 {safePage + 1} / {totalPages} 页）
            </h3>
            <ul className="space-y-2">
              {pageRows.map((row) => (
                <li key={row.id} className="rounded-lg border p-3 text-sm">
                  <p className="font-medium">
                    {row.id} · {row.answerable ? "可答" : "不可答"} · 结果{" "}
                    {row.outcome} · 引用有效 {row.valid_citation ? "是" : "否"}{" "}
                    · 来源一致 {row.source_integrity ? "是" : "否"}
                  </p>
                  <p className="mt-1 text-muted-foreground">
                    引用数 {row.citation_count}（无效 {row.invalid_citations}）
                    · 证据命中{" "}
                    {row.evidence_hit === null
                      ? "不适用"
                      : row.evidence_hit
                        ? "是"
                        : "否"}{" "}
                    · 降级：
                    {row.degraded?.length ? row.degraded.join("、") : "无"}
                  </p>
                </li>
              ))}
            </ul>
            <div className="flex gap-4 text-sm">
              {safePage > 0 && (
                <Link
                  to="/kb/$kbId/answer-evaluation"
                  params={{ kbId: String(id) }}
                  search={{ cursor, digest, row: safePage - 1 }}
                  className="underline"
                >
                  上一页题目
                </Link>
              )}
              {safePage + 1 < totalPages && (
                <Link
                  to="/kb/$kbId/answer-evaluation"
                  params={{ kbId: String(id) }}
                  search={{ cursor, digest, row: safePage + 1 }}
                  className="underline"
                >
                  下一页题目
                </Link>
              )}
            </div>
          </div>
        </section>
      )}
    </section>
  );
}
