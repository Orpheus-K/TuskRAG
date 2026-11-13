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
import { Button } from "@tuskrag/ui/button";
import type { FormEvent } from "react";

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
        <Link to="/">返回知识库</Link>
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
  return (
    <section className="space-y-6 py-12">
      <Link
        to="/kb/$kbId/documents"
        params={{ kbId: String(id) }}
        search={{ cursor: 0 }}
        className="text-sm underline"
      >
        返回文档
      </Link>
      <Link
        className="inline-block text-sm underline"
        to="/kb/$kbId/eval/history"
        params={{ kbId: String(id) }}
        search={{ cursor: 0 }}
      >
        运行历史
      </Link>
      <h1 className="break-words text-3xl font-semibold">
        {base.data?.name} · 评测报告
      </h1>
      <Link
        className="inline-block text-sm underline"
        to="/kb/$kbId/eval/compare"
        params={{ kbId: String(id) }}
        search={{ baseline: digest, current: "", cursor: "", offset: 0 }}
      >
        对比两份报告
      </Link>
      <Link
        className="ml-4 inline-block text-sm underline"
        to="/kb/$kbId/eval/sets"
        params={{ kbId: String(id) }}
        search={{ cursor: 0, set: 0 }}
      >
        管理评测集
      </Link>
      <p className="text-sm text-muted-foreground">
        查看 CLI
        评测报告。导入报告不代表服务端执行或验证了模型质量；从评测集页面可启动后台运行。
      </p>
      {base.data?.status === "archived" ? (
        <p role="status">知识库已归档，报告仅供查看。</p>
      ) : (
        <form className="space-y-3 rounded-xl border p-5" onSubmit={submit}>
          <label className="block text-sm" htmlFor="eval-file">
            完整报告 JSON（最大 8 MiB）
          </label>
          <input
            id="eval-file"
            name="report"
            type="file"
            accept=".json,application/json"
            required
            disabled={upload.isPending}
            className="block w-full min-w-0 text-sm"
          />
          <Button type="submit" disabled={upload.isPending}>
            {upload.isPending ? "正在导入" : "导入报告"}
          </Button>
          {upload.isError && <p role="alert">{errorText(upload.error)}</p>}
          {upload.isSuccess && (
            <p role="status">报告已导入。相同内容自动去重。</p>
          )}
        </form>
      )}
      <div className="flex flex-wrap items-center gap-4">
        <h2 className="text-xl font-medium">可见报告</h2>
        <Link
          to="/kb/$kbId/eval"
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
      <p className="text-xs text-muted-foreground">
        按报告摘要排序；失效证据对应的报告不会显示。新增报告请刷新首页。
      </p>
      {list.isPending ? (
        <p role="status">正在加载报告列表</p>
      ) : list.data?.items?.length === 0 ? (
        <p>暂无可见报告。</p>
      ) : (
        <ul className="space-y-3">
          {list.data?.items?.map((item) => (
            <li key={item.digest} className="space-y-3 rounded-xl border p-5">
              <Link
                to="/kb/$kbId/eval"
                params={{ kbId: String(id) }}
                search={{ cursor, digest: item.digest, row: 0 }}
                className="block break-words font-medium underline"
              >
                {item.dataset_version} · {item.mode} · {item.model_label}
              </Link>
              <p className="break-all text-xs text-muted-foreground">
                外部导入 ·{" "}
                {item.model_kind === "stub" ? "模型替身" : "真实模型标记"} ·{" "}
                {item.created_at} · {item.digest.slice(0, 12)}
              </p>
              <Summary value={item.summary} />
            </li>
          ))}
        </ul>
      )}
      {list.data?.next_cursor && (
        <Link
          to="/kb/$kbId/eval"
          params={{ kbId: String(id) }}
          search={{ cursor: list.data.next_cursor, digest: "", row: 0 }}
          className="inline-block underline"
        >
          下一页报告
        </Link>
      )}
      {digest &&
        (detail.isPending ? (
          <p role="status">正在加载逐题结果</p>
        ) : detail.data ? (
          <ReportDetail
            key={digest}
            data={detail.data}
            id={id}
            cursor={cursor}
            rowPage={rowPage}
          />
        ) : null)}
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
    <section className="space-y-4 rounded-xl border p-5" aria-label="报告详情">
      <h2 className="text-xl font-medium">逐题结果</h2>
      <p className="break-words text-sm">
        {report.dataset_version} · {report.model_label} · {report.mode} ·
        {data.source === "executed" ? "服务端执行" : "外部导入"}
      </p>
      <Summary value={report.summary} />
      <details>
        <summary className="cursor-pointer text-sm">配置摘要与阶段耗时</summary>
        <p className="mt-3 break-all text-xs">
          数据集 SHA-256：{report.dataset_sha256}
          <br />
          配置 SHA-256：{report.config_sha256}
        </p>
        <ul className="mt-3 text-sm">
          {Object.entries(report.latency_ms_p50_p95).map(([stage, pair]) => (
            <li key={stage} className="break-words">
              {stage}：p50 {pair[0]?.toFixed(2)} ms / p95 {pair[1]?.toFixed(2)}{" "}
              ms
            </li>
          ))}
        </ul>
      </details>
      <p className="text-xs text-muted-foreground">
        报告仅含题号及证据标识，不含问题与文档正文。未实现的引用、拒答指标不显示为零。
      </p>
      {start >= rows.length ? (
        <p role="alert">逐题页码超出范围，请返回第一页。</p>
      ) : (
        rows.slice(start, start + 20).map((row) => (
          <details key={row.id} className="rounded-lg border p-3">
            <summary className="cursor-pointer break-words">
              {row.id} · {row.metrics === null ? "无答案题" : "有答案题"}
              {row.degraded?.length ? " · 存在降级" : ""}
            </summary>
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
          </details>
        ))
      )}
      {onPage ? (
        <nav aria-label="逐题分页" className="flex flex-wrap gap-4">
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
        <nav aria-label="逐题分页" className="flex flex-wrap gap-4">
          {rowPage > 0 && (
            <Link
              to="/kb/$kbId/eval"
              params={{ kbId: String(id) }}
              search={{ cursor, digest: data.digest, row: 0 }}
            >
              逐题第一页
            </Link>
          )}
          {rowPage > 0 && (
            <Link
              to="/kb/$kbId/eval"
              params={{ kbId: String(id) }}
              search={{ cursor, digest: data.digest, row: rowPage - 1 }}
            >
              上一页题目
            </Link>
          )}
          {start + 20 < rows.length && (
            <Link
              to="/kb/$kbId/eval"
              params={{ kbId: String(id) }}
              search={{ cursor, digest: data.digest, row: rowPage + 1 }}
            >
              下一页题目
            </Link>
          )}
        </nav>
      )}
    </section>
  );
}
