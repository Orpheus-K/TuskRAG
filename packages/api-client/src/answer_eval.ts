import { ApiError } from "./index";
import type { components } from "./schema";

export type AnswerAssemblyReport = components["schemas"]["AnswerAssemblyReport"];
export type AnswerJudgeConfig = components["schemas"]["AnswerJudgeConfig"];
export type AnswerEvalMetrics = components["schemas"]["AnswerMetrics"];
export type AnswerEvalSummary = components["schemas"]["AnswerSummary"];
export type AnswerEvalRow = components["schemas"]["AnswerRow"];
export type AnswerEvalReport = components["schemas"]["AnswerReport"];
export type StoredAnswerEvalReport = components["schemas"]["StoredAnswerReport"];
export type AnswerEvalReportListItem =
  components["schemas"]["AnswerReportListItem"];
export type AnswerEvalReportPage = components["schemas"]["AnswerReportPage"];
export type AnswerEvalMetricDelta = components["schemas"]["AnswerMetricDelta"];
export type AnswerEvalRowComparison =
  components["schemas"]["AnswerRowComparison"];
export type AnswerEvalComparison =
  components["schemas"]["AnswerReportComparisonView"];

const evalDigest = /^[0-9a-f]{64}$/;

const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const positive = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v > 0;
const nonnegative = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const nullableRate = (v: unknown): v is number | null =>
  v === null || (typeof v === "number" && Number.isFinite(v) && v >= 0 && v <= 1);
const nullableDeltaRate = (v: unknown): v is number | null =>
  v === null ||
  (typeof v === "number" && Number.isFinite(v) && v >= -1 && v <= 1);

async function checked(response: Response) {
  if (response.ok) return response;
  const value: unknown = await response.json();
  throw new ApiError(
    response.status,
    record(value) && record(value.error) && typeof value.error.code === "string"
      ? value.error.code
      : "request_failed",
    record(value) &&
      record(value.error) &&
      typeof value.error.trace_id === "string"
      ? value.error.trace_id
      : "",
  );
}

function isAssembly(value: unknown): value is AnswerAssemblyReport {
  return (
    record(value) &&
    nonnegative(value.context_budget_tokens) &&
    typeof value.parent_backtrack === "boolean"
  );
}

function isJudge(value: unknown): value is AnswerJudgeConfig {
  return (
    value === undefined ||
    value === null ||
    (record(value) &&
      typeof value.chat_model_id === "number" &&
      Number.isSafeInteger(value.chat_model_id) &&
      typeof value.model_label === "string" &&
      (value.prompt_version === undefined ||
        typeof value.prompt_version === "string"))
  );
}

function isAnswerMetrics(value: unknown): value is AnswerEvalMetrics {
  return (
    record(value) &&
    typeof value.valid_citation_rate === "number" &&
    Number.isFinite(value.valid_citation_rate) &&
    typeof value.source_integrity_rate === "number" &&
    Number.isFinite(value.source_integrity_rate) &&
    nullableRate(value.evidence_hit_rate) &&
    nullableRate(value.deterministic_pass_rate) &&
    nullableRate(value.refusal_correct_rate) &&
    nullableRate(value.false_refusal_rate) &&
    nullableRate(value.unsupported_rate) &&
    nullableRate(value.citation_support_rate)
  );
}

function isAnswerSummary(value: unknown): value is AnswerEvalSummary {
  return (
    record(value) &&
    positive(value.cases) &&
    nonnegative(value.answerable_cases) &&
    nonnegative(value.unanswerable_cases) &&
    value.answerable_cases + value.unanswerable_cases === value.cases &&
    nonnegative(value.answered_cases) &&
    nonnegative(value.refused_cases) &&
    nonnegative(value.error_cases) &&
    isAnswerMetrics(value.metrics)
  );
}

function isAnswerRow(row: unknown): row is AnswerEvalRow {
  return (
    record(row) &&
    typeof row.id === "string" &&
    row.id.length > 0 &&
    typeof row.answerable === "boolean" &&
    (row.outcome === "answered" ||
      row.outcome === "refused" ||
      row.outcome === "error") &&
    typeof row.valid_citation === "boolean" &&
    typeof row.source_integrity === "boolean" &&
    nonnegative(row.citation_count) &&
    nonnegative(row.invalid_citations) &&
    (row.evidence_hit === null || typeof row.evidence_hit === "boolean") &&
    (row.deterministic_pass === null ||
      typeof row.deterministic_pass === "boolean") &&
    (row.evidence_ids === null || Array.isArray(row.evidence_ids)) &&
    (row.evidence_chunk_counts === null ||
      Array.isArray(row.evidence_chunk_counts)) &&
    (row.evidence_documents === null ||
      Array.isArray(row.evidence_documents)) &&
    (row.degraded === null || Array.isArray(row.degraded)) &&
    record(row.timings_ms)
  );
}

export function isAnswerEvalReport(value: unknown): value is AnswerEvalReport {
  if (
    !record(value) ||
    typeof value.dataset_version !== "string" ||
    value.dataset_version.length === 0 ||
    typeof value.dataset_sha256 !== "string" ||
    !evalDigest.test(value.dataset_sha256) ||
    typeof value.config_sha256 !== "string" ||
    !evalDigest.test(value.config_sha256) ||
    typeof value.model_label !== "string" ||
    value.model_label.length === 0 ||
    (value.model_kind !== "real" && value.model_kind !== "stub") ||
    typeof value.prompt_version !== "string" ||
    value.prompt_version.length === 0 ||
    (value.status !== "passed" && value.status !== "failed") ||
    !isAssembly(value.assembly) ||
    !isJudge(value.judge) ||
    !isAnswerSummary(value.summary) ||
    !Array.isArray(value.rows) ||
    value.rows.length !== value.summary.cases ||
    !value.rows.every(isAnswerRow) ||
    !record(value.latency_ms_p50_p95)
  ) {
    return false;
  }
  return Object.values(value.latency_ms_p50_p95).every(
    (pair) =>
      Array.isArray(pair) &&
      pair.length === 2 &&
      pair.every(
        (v) => typeof v === "number" && Number.isFinite(v) && v >= 0,
      ) &&
      pair[1] >= pair[0],
  );
}

function isStoredAnswerEvalReport(
  value: unknown,
): value is StoredAnswerEvalReport {
  return (
    record(value) &&
    typeof value.digest === "string" &&
    evalDigest.test(value.digest) &&
    value.source === "imported" &&
    typeof value.created_at === "string" &&
    Number.isFinite(Date.parse(value.created_at)) &&
    isAnswerEvalReport(value.report)
  );
}

function isListItem(item: unknown): item is AnswerEvalReportListItem {
  return (
    record(item) &&
    typeof item.digest === "string" &&
    evalDigest.test(item.digest) &&
    item.source === "imported" &&
    typeof item.created_at === "string" &&
    Number.isFinite(Date.parse(item.created_at)) &&
    typeof item.dataset_version === "string" &&
    item.dataset_version.length > 0 &&
    typeof item.model_label === "string" &&
    item.model_label.length > 0 &&
    (item.model_kind === "real" || item.model_kind === "stub") &&
    typeof item.prompt_version === "string" &&
    item.prompt_version.length > 0 &&
    isAssembly(item.assembly) &&
    isJudge(item.judge) &&
    isAnswerSummary(item.summary)
  );
}

export async function listAnswerEvalReports(
  id: number,
  cursor: string,
  signal: AbortSignal,
): Promise<AnswerEvalReportPage> {
  if (!positive(id) || (cursor !== "" && !evalDigest.test(cursor)))
    throw new Error("invalid answer report address");
  const response = await checked(
    await fetch(
      `/api/v1/kbs/${id}/answer-eval-reports?${new URLSearchParams({ cursor, limit: "10" })}`,
      { signal, credentials: "same-origin" },
    ),
  );
  const data: unknown = await response.json();
  if (!record(data) || !Array.isArray(data.items) || data.items.length > 10)
    throw new Error("invalid answer report page");
  let previous = cursor;
  for (const item of data.items) {
    if (!isListItem(item) || item.digest <= previous)
      throw new Error("invalid answer report list item");
    previous = item.digest;
  }
  if (
    data.next_cursor !== null &&
    (data.items.length === 0 || data.next_cursor !== previous)
  )
    throw new Error("invalid answer report cursor");
  return data as AnswerEvalReportPage;
}

export async function getAnswerEvalReport(
  id: number,
  digest: string,
  signal: AbortSignal,
): Promise<StoredAnswerEvalReport> {
  if (!positive(id) || !evalDigest.test(digest))
    throw new Error("invalid answer report address");
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}/answer-eval-reports/${digest}`, {
      signal,
      credentials: "same-origin",
    }),
  );
  const data: unknown = await response.json();
  if (!isStoredAnswerEvalReport(data) || data.digest !== digest)
    throw new Error("invalid answer report response");
  return data;
}

export async function importAnswerEvalReport(
  id: number,
  report: AnswerEvalReport,
  csrf: string,
): Promise<StoredAnswerEvalReport> {
  if (!positive(id) || !isAnswerEvalReport(report))
    throw new Error("invalid answer report import");
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}/answer-eval-reports`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(report),
    }),
  );
  const data: unknown = await response.json();
  if (!isStoredAnswerEvalReport(data))
    throw new Error("invalid answer report import response");
  return data;
}

function isMetricDelta(value: unknown): value is AnswerEvalMetricDelta {
  return (
    record(value) &&
    typeof value.valid_citation_rate === "number" &&
    Number.isFinite(value.valid_citation_rate) &&
    typeof value.source_integrity_rate === "number" &&
    Number.isFinite(value.source_integrity_rate) &&
    nullableDeltaRate(value.evidence_hit_rate) &&
    nullableDeltaRate(value.deterministic_pass_rate) &&
    nullableDeltaRate(value.refusal_correct_rate) &&
    nullableDeltaRate(value.false_refusal_rate) &&
    nullableDeltaRate(value.unsupported_rate) &&
    nullableDeltaRate(value.citation_support_rate)
  );
}

export async function compareAnswerEvalReports(
  id: number,
  baseline: string,
  current: string,
  offset: number,
  signal: AbortSignal,
): Promise<AnswerEvalComparison> {
  if (
    !positive(id) ||
    !evalDigest.test(baseline) ||
    !evalDigest.test(current) ||
    !Number.isSafeInteger(offset) ||
    offset < 0 ||
    offset > 10000
  )
    throw new Error("invalid answer comparison address");
  const response = await checked(
    await fetch(
      `/api/v1/kbs/${id}/answer-eval-report-comparison?${new URLSearchParams({ baseline, current, offset: String(offset), limit: "20" })}`,
      { signal, credentials: "same-origin" },
    ),
  );
  const data: unknown = await response.json();
  if (
    !record(data) ||
    (data.gate !== "passed" && data.gate !== "regressed") ||
    !isListItem(data.baseline) ||
    !isListItem(data.current) ||
    data.baseline.digest !== baseline ||
    data.current.digest !== current ||
    !isMetricDelta(data.delta) ||
    !positive(data.total) ||
    data.offset !== offset ||
    data.limit !== 20 ||
    !Array.isArray(data.rows) ||
    data.rows.length !== Math.min(20, data.total - offset)
  ) {
    throw new Error("invalid answer comparison response");
  }
  const ids = new Set<string>();
  for (const row of data.rows) {
    if (
      !record(row) ||
      typeof row.id !== "string" ||
      ids.has(row.id) ||
      typeof row.answerable !== "boolean" ||
      typeof row.baseline_outcome !== "string" ||
      typeof row.current_outcome !== "string" ||
      typeof row.baseline_valid_citation !== "boolean" ||
      typeof row.current_valid_citation !== "boolean" ||
      typeof row.baseline_source_integrity !== "boolean" ||
      typeof row.current_source_integrity !== "boolean"
    ) {
      throw new Error("invalid answer comparison row");
    }
    ids.add(row.id);
  }
  return data as AnswerEvalComparison;
}
