import type { components, paths } from "./schema";

export * from "./acl";
export * from "./answer";
export * from "./answer_eval";
export * from "./members";

export type Identity = components["schemas"]["IdentityBody"];
export type Login = components["schemas"]["LoginInputBody"];
export type KnowledgeBase = components["schemas"]["KnowledgeBaseBody"];
export type KnowledgeConfig = components["schemas"]["KnowledgeConfig"];
export type RetrievalConfig = components["schemas"]["RetrievalConfig"];
export type KnowledgeConfigSnapshot = { config: KnowledgeConfig; etag: string };
export type EvalReport = components["schemas"]["RunReport"];
export type StoredEvalReport = components["schemas"]["StoredReport"];
export type EvalReportPage = components["schemas"]["ReportPage"];
export type EvalMetrics = components["schemas"]["Metrics"];
export type EvalSummary = components["schemas"]["Summary"];
export type EvalComparison = components["schemas"]["ReportComparison"];
export type EvalRow = components["schemas"]["RunRow"];
export type Document = components["schemas"]["DocumentBody"];
export type DocumentPage = components["schemas"]["DocumentListOutputBody"];
export type PurgeJob = components["schemas"]["PurgeJobBody"];
export type PurgeJobPage = { items: PurgeJob[]; next_cursor: number | null };
export type UploadedDocument = components["schemas"]["UploadedDocumentBody"];
export type EmbeddingSpace = components["schemas"]["EmbeddingSpaceBody"];
export type NewEmbeddingSpace =
  components["schemas"]["CreateEmbeddingSpaceInputBody"];
export type NewKnowledgeBase =
  components["schemas"]["CreateKnowledgeBaseInputBody"];
export type SearchResult = components["schemas"]["SearchOutputBody"];
export type SearchOptions = NonNullable<
  components["schemas"]["SearchInputBody"]["options"]
>;
export type DocumentContents =
  components["schemas"]["DocumentContentsOutputBody"];
export type SectionContent = components["schemas"]["SectionOutputBody"];
type SectionReference = components["schemas"]["SectionReferenceBody"];
type SearchEvidence = components["schemas"]["SearchEvidence"];

const evalDigest = /^[0-9a-f]{64}$/;
function isEvalMetrics(value: unknown): value is EvalMetrics {
  return (
    isRecord(value) &&
    [value.RecallAt20, value.HitAt5, value.MRRAt10, value.NDCGAt10].every(
      (v) => typeof v === "number" && Number.isFinite(v) && v >= 0 && v <= 1,
    )
  );
}
function isEvalSummary(value: unknown): value is EvalSummary {
  return (
    isRecord(value) &&
    isPositiveInteger(value.Cases) &&
    typeof value.AnswerableCases === "number" &&
    Number.isSafeInteger(value.AnswerableCases) &&
    value.AnswerableCases >= 0 &&
    typeof value.UnanswerableCases === "number" &&
    Number.isSafeInteger(value.UnanswerableCases) &&
    value.UnanswerableCases >= 0 &&
    value.AnswerableCases + value.UnanswerableCases === value.Cases &&
    (value.AnswerableCases === 0
      ? value.Metrics === null
      : isEvalMetrics(value.Metrics))
  );
}
function isTimingMap(value: unknown): value is Record<string, number> {
  return (
    isRecord(value) &&
    Object.values(value).every(
      (v) => typeof v === "number" && Number.isFinite(v) && v >= 0,
    )
  );
}
function isEvalEvidence(
  value: unknown,
): value is components["schemas"]["Evidence"] {
  return (
    isRecord(value) &&
    isPositiveInteger(value.ChunkID) &&
    isPositiveInteger(value.DocumentID) &&
    [value.SectionID, value.PageStart, value.PageEnd].every(
      (v) => typeof v === "number" && Number.isSafeInteger(v) && v >= 0,
    ) &&
    Number(value.PageEnd) >= Number(value.PageStart) &&
    (value.PageStart === 0) === (value.PageEnd === 0)
  );
}
function isEvalIdentity(value: Record<string, unknown>) {
  return (
    typeof value.dataset_version === "string" &&
    value.dataset_version.length > 0 &&
    typeof value.model_label === "string" &&
    value.model_label.length > 0 &&
    (value.model_kind === "real" || value.model_kind === "stub") &&
    ["lexical", "vector", "rrf", "rerank"].includes(String(value.mode))
  );
}
function isEvalRow(row: unknown): row is EvalRow {
  return (
    isRecord(row) &&
    typeof row.id === "string" &&
    row.id.length > 0 &&
    (row.metrics === null || isEvalMetrics(row.metrics)) &&
    isTimingMap(row.timings_ms) &&
    row.timings_ms.total !== undefined &&
    [row.fused, row.ranked].every(
      (list) =>
        list === null ||
        (Array.isArray(list) &&
          list.length <= 100 &&
          list.every(isEvalEvidence)),
    ) &&
    (row.degraded === null ||
      (Array.isArray(row.degraded) &&
        row.degraded.every((v) => typeof v === "string")))
  );
}
export function isEvalReport(value: unknown): value is EvalReport {
  if (
    !isRecord(value) ||
    !isEvalIdentity(value) ||
    value.status !== "passed" ||
    value.unauthorized_evidence !== 0 ||
    typeof value.dataset_sha256 !== "string" ||
    !evalDigest.test(value.dataset_sha256) ||
    typeof value.config_sha256 !== "string" ||
    !evalDigest.test(value.config_sha256) ||
    !isEvalSummary(value.summary) ||
    !Array.isArray(value.rows) ||
    value.rows.length !== value.summary.Cases ||
    value.rows.length > 10000 ||
    !isRecord(value.latency_ms_p50_p95)
  )
    return false;
  const ids = new Set<string>();
  let answerable = 0;
  for (const row of value.rows) {
    if (!isEvalRow(row) || ids.has(row.id)) return false;
    if (
      value.mode === "rerank" &&
      Array.isArray(row.degraded) &&
      row.degraded.length > 0
    )
      return false;
    ids.add(row.id);
    if (row.metrics !== null) answerable++;
  }
  return (
    answerable === value.summary.AnswerableCases &&
    Object.values(value.latency_ms_p50_p95).every(
      (pair) =>
        Array.isArray(pair) &&
        pair.length === 2 &&
        pair.every(
          (v) => typeof v === "number" && Number.isFinite(v) && v >= 0,
        ) &&
        pair[0] <= pair[1],
    )
  );
}
const metricKeys: (keyof EvalMetrics)[] = [
  "RecallAt20",
  "HitAt5",
  "MRRAt10",
  "NDCGAt10",
];
function isMetricDelta(
  value: unknown,
  before: EvalMetrics | null,
  after: EvalMetrics | null,
): boolean {
  if (before === null || after === null)
    return before === null && after === null && value === null;
  return (
    isRecord(value) &&
    metricKeys.every(
      (key) =>
        typeof value[key] === "number" &&
        Number.isFinite(value[key]) &&
        Math.abs(value[key] - (after[key] - before[key])) <= 1e-12,
    )
  );
}
export async function compareEvalReports(
  id: number,
  baseline: string,
  current: string,
  offset: number,
  signal: AbortSignal,
): Promise<EvalComparison> {
  if (
    !isPositiveInteger(id) ||
    !evalDigest.test(baseline) ||
    !evalDigest.test(current) ||
    !Number.isSafeInteger(offset) ||
    offset < 0 ||
    offset > 10000
  )
    throw new Error("invalid comparison address");
  const response = await checked(
    await fetch(
      `/api/v1/kbs/${id}/eval-report-comparison?${new URLSearchParams({ baseline, current, offset: String(offset), limit: "20" })}`,
      { signal, credentials: "same-origin" },
    ),
  );
  const data: unknown = await response.json();
  return decodeComparison(data, baseline, current, offset);
}
function decodeComparison(
  data: unknown,
  baseline: string,
  current: string,
  offset: number,
): EvalComparison {
  if (
    !isRecord(data) ||
    data.baseline_digest !== baseline ||
    data.current_digest !== current ||
    !isEvalSummary(data.baseline) ||
    !isEvalSummary(data.current) ||
    data.baseline.AnswerableCases === 0 ||
    data.baseline.Cases !== data.current.Cases ||
    data.baseline.AnswerableCases !== data.current.AnswerableCases ||
    !isMetricDelta(data.delta, data.baseline.Metrics, data.current.Metrics) ||
    !isRecord(data.delta) ||
    !isPositiveInteger(data.total_rows) ||
    data.total_rows !== data.baseline.Cases ||
    data.total_rows > 10000 ||
    offset > data.total_rows ||
    !Array.isArray(data.rows) ||
    data.rows.length !== Math.min(20, data.total_rows - offset)
  )
    throw new Error("invalid comparison response");
  const delta = data.delta;
  const gate = metricKeys.some((key) => Number(delta[key]) < -0.02 - 1e-12)
    ? "regressed"
    : "passed";
  if (data.gate !== gate) throw new Error("invalid comparison gate");
  const ids = new Set<string>();
  for (const row of data.rows) {
    if (
      !isRecord(row) ||
      typeof row.id !== "string" ||
      ids.has(row.id) ||
      !isEvalRow(row.baseline) ||
      !isEvalRow(row.current) ||
      row.id !== row.baseline.id ||
      row.id !== row.current.id ||
      typeof row.evidence_changed !== "boolean" ||
      !isMetricDelta(row.delta, row.baseline.metrics, row.current.metrics)
    )
      throw new Error("invalid comparison row");
    ids.add(row.id);
  }
  const next = offset + data.rows.length;
  if (data.next_offset !== (next < data.total_rows ? next : null))
    throw new Error("invalid comparison offset");
  return data as EvalComparison;
}
function isStoredEvalReport(value: unknown): value is StoredEvalReport {
  return (
    isRecord(value) &&
    typeof value.digest === "string" &&
    evalDigest.test(value.digest) &&
    value.source === "imported" &&
    typeof value.created_at === "string" &&
    Number.isFinite(Date.parse(value.created_at)) &&
    isEvalReport(value.report)
  );
}
export async function listEvalReports(
  id: number,
  cursor: string,
  signal: AbortSignal,
): Promise<EvalReportPage> {
  if (!isPositiveInteger(id) || (cursor !== "" && !evalDigest.test(cursor)))
    throw new Error("invalid report address");
  const response = await checked(
    await fetch(
      `/api/v1/kbs/${id}/eval-reports?${new URLSearchParams({ cursor, limit: "10" })}`,
      { signal, credentials: "same-origin" },
    ),
  );
  const data: unknown = await response.json();
  if (!isRecord(data) || !Array.isArray(data.items) || data.items.length > 10)
    throw new Error("invalid report page");
  let previous = cursor;
  for (const item of data.items) {
    if (
      !isRecord(item) ||
      typeof item.digest !== "string" ||
      !evalDigest.test(item.digest) ||
      item.digest <= previous ||
      item.source !== "imported" ||
      typeof item.created_at !== "string" ||
      !Number.isFinite(Date.parse(item.created_at)) ||
      !isEvalIdentity(item) ||
      !isEvalSummary(item.summary)
    )
      throw new Error("invalid report list item");
    previous = item.digest;
  }
  if (
    data.next_cursor !== null &&
    (data.items.length === 0 || data.next_cursor !== previous)
  )
    throw new Error("invalid report cursor");
  return data as EvalReportPage;
}
export async function getEvalReport(
  id: number,
  digest: string,
  signal: AbortSignal,
): Promise<StoredEvalReport> {
  if (!isPositiveInteger(id) || !evalDigest.test(digest))
    throw new Error("invalid report address");
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}/eval-reports/${digest}`, {
      signal,
      credentials: "same-origin",
    }),
  );
  const data: unknown = await response.json();
  if (!isStoredEvalReport(data) || data.digest !== digest)
    throw new Error("invalid report response");
  return data;
}
export async function importEvalReport(
  id: number,
  report: EvalReport,
  csrf: string,
): Promise<StoredEvalReport> {
  if (!isPositiveInteger(id) || !isEvalReport(report))
    throw new Error("invalid report import");
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}/eval-reports`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(report),
    }),
  );
  const data: unknown = await response.json();
  if (!isStoredEvalReport(data))
    throw new Error("invalid report import response");
  return data;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly traceId: string,
  ) {
    super(`API request failed: ${status} (${code})`);
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function isIdentity(value: unknown): value is Identity {
  return (
    isRecord(value) &&
    isPositiveInteger(value.tenant_id) &&
    isPositiveInteger(value.user_id) &&
    typeof value.csrf_token === "string" &&
    value.csrf_token.length > 0
  );
}

function isKnowledgeBase(value: unknown): value is KnowledgeBase {
  return (
    isRecord(value) &&
    isPositiveInteger(value.id) &&
    isPositiveInteger(value.embedding_space_id) &&
    isPositiveInteger(value.config_revision) &&
    typeof value.name === "string" &&
    typeof value.created_at === "string" &&
    (value.egress_policy === "deny" ||
      value.egress_policy === "internal_only" ||
      value.egress_policy === "allow") &&
    (value.status === "active" || value.status === "archived") &&
    (value.role === "owner" ||
      value.role === "admin" ||
      value.role === "editor" ||
      value.role === "viewer")
  );
}

function isRetrievalConfig(value: unknown): value is RetrievalConfig {
  if (
    !isRecord(value) ||
    !isPositiveInteger(value.top_k) ||
    Number(value.top_k) > 30
  )
    return false;
  const rank = value.reranker;
  return (
    rank === undefined ||
    (isRecord(rank) &&
      isPositiveInteger(rank.provider_id) &&
      typeof rank.model === "string" &&
      rank.model.trim().length > 0 &&
      new TextEncoder().encode(rank.model).length <= 200 &&
      (rank.top_n === 20 || rank.top_n === 30 || rank.top_n === 50) &&
      (rank.fallback === undefined || typeof rank.fallback === "boolean") &&
      (rank.min_score === undefined ||
        (typeof rank.min_score === "number" &&
          Number.isFinite(rank.min_score))))
  );
}

async function readKnowledgeConfig(
  response: Response,
): Promise<KnowledgeConfigSnapshot> {
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    typeof data.revision !== "number" ||
    !Number.isSafeInteger(data.revision) ||
    data.revision < 0 ||
    data.revision > 2147483647 ||
    (data.revision === 0
      ? data.retrieval !== undefined
      : !isRetrievalConfig(data.retrieval))
  ) {
    throw new Error("invalid knowledge configuration response");
  }
  const etag = response.headers.get("ETag");
  if (etag !== `"${data.revision}"`)
    throw new Error("invalid knowledge configuration ETag");
  const config: KnowledgeConfig = { revision: data.revision };
  if (isRetrievalConfig(data.retrieval)) config.retrieval = data.retrieval;
  return { config, etag };
}

export async function getKnowledgeConfig(
  id: number,
  signal: AbortSignal,
): Promise<KnowledgeConfigSnapshot> {
  if (!isPositiveInteger(id)) throw new Error("invalid knowledge base ID");
  return readKnowledgeConfig(
    await checked(
      await fetch(`/api/v1/kbs/${id}/config`, {
        signal,
        credentials: "same-origin",
      }),
    ),
  );
}

export async function saveKnowledgeConfig(
  id: number,
  previous: KnowledgeConfigSnapshot,
  retrieval: RetrievalConfig,
  csrf: string,
): Promise<KnowledgeConfigSnapshot> {
  if (
    !isPositiveInteger(id) ||
    !isRetrievalConfig(retrieval) ||
    !Number.isSafeInteger(previous.config.revision) ||
    previous.config.revision < 0 ||
    previous.config.revision >= 2147483647 ||
    previous.etag !== `"${previous.config.revision}"`
  ) {
    throw new Error("invalid knowledge configuration request");
  }
  const body: components["schemas"]["PutKnowledgeConfigInputBody"] = {
    retrieval,
  };
  const result = await readKnowledgeConfig(
    await checked(
      await fetch(`/api/v1/kbs/${id}/config`, {
        method: "PUT",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrf,
          "If-Match": previous.etag,
        },
        body: JSON.stringify(body),
      }),
    ),
  );
  if (result.config.revision !== previous.config.revision + 1)
    throw new Error("configuration save was not confirmed");
  return result;
}

function isEmbeddingSpace(value: unknown): value is EmbeddingSpace {
  return (
    isRecord(value) &&
    isPositiveInteger(value.id) &&
    isPositiveInteger(value.provider_id) &&
    isPositiveInteger(value.dimensions) &&
    typeof value.provider_name === "string" &&
    typeof value.base_url === "string" &&
    typeof value.model === "string" &&
    typeof value.normalized === "boolean" &&
    typeof value.has_credential === "boolean" &&
    (value.locality === "internal" || value.locality === "external") &&
    (value.distance === "cosine" || value.distance === "ip")
  );
}

const documentStatuses: readonly string[] = [
  "queued",
  "parsing",
  "embedding",
  "indexing",
  "ready",
  "partial",
  "failed",
  "superseded",
  "purging",
];

function isDocument(value: unknown): value is Document {
  return (
    isRecord(value) &&
    isPositiveInteger(value.id) &&
    isPositiveInteger(value.latest_version_id) &&
    isPositiveInteger(value.byte_size) &&
    (value.current_version_id === null ||
      isPositiveInteger(value.current_version_id)) &&
    (value.latest_job_id === null || isPositiveInteger(value.latest_job_id)) &&
    typeof value.retryable === "boolean" &&
    (!value.retryable ||
      (isPositiveInteger(value.latest_job_id) &&
        value.latest_status === "failed")) &&
    (value.failure_code === null || typeof value.failure_code === "string") &&
    typeof value.title === "string" &&
    typeof value.mime === "string" &&
    typeof value.latest_created_at === "string" &&
    typeof value.latest_status === "string" &&
    documentStatuses.includes(value.latest_status)
  );
}

function isUploadedDocument(value: unknown): value is UploadedDocument {
  return (
    isRecord(value) &&
    isPositiveInteger(value.document_id) &&
    isPositiveInteger(value.version_id) &&
    isPositiveInteger(value.byte_size) &&
    typeof value.title === "string" &&
    typeof value.mime === "string" &&
    typeof value.created_at === "string" &&
    typeof value.status === "string" &&
    documentStatuses.includes(value.status)
  );
}

function isSectionReference(value: unknown): value is SectionReference {
  return (
    isRecord(value) &&
    isPositiveInteger(value.id) &&
    (value.parent_id === null || isPositiveInteger(value.parent_id)) &&
    isPositiveInteger(value.ordinal) &&
    typeof value.level === "number" &&
    Number.isInteger(value.level) &&
    value.level >= 0 &&
    value.level <= 9 &&
    typeof value.heading === "string" &&
    Array.isArray(value.heading_path) &&
    value.heading_path.every((part: unknown) => typeof part === "string") &&
    (value.pages === null ||
      (Array.isArray(value.pages) &&
        value.pages.length === 2 &&
        isPositiveInteger(value.pages[0]) &&
        isPositiveInteger(value.pages[1]) &&
        value.pages[1] >= value.pages[0]))
  );
}

function isContentDocument(
  value: unknown,
): value is components["schemas"]["ContentDocumentBody"] {
  return (
    isRecord(value) &&
    isPositiveInteger(value.id) &&
    isPositiveInteger(value.version_id) &&
    typeof value.title === "string"
  );
}

function isSearchEvidence(value: unknown): value is SearchEvidence {
  if (
    !isRecord(value) ||
    typeof value.id !== "string" ||
    !/^c_[1-9]\d*$/.test(value.id) ||
    typeof value.score !== "number" ||
    !Number.isFinite(value.score) ||
    (value.score_kind === "rrf" && value.score <= 0) ||
    (value.score_kind !== "rrf" && value.score_kind !== "rerank") ||
    typeof value.content !== "string" ||
    !isRecord(value.document) ||
    !isPositiveInteger(value.document.id) ||
    !isPositiveInteger(value.document.version_id) ||
    typeof value.document.title !== "string" ||
    !isRecord(value.section) ||
    !isPositiveInteger(value.section.id) ||
    !Array.isArray(value.section.heading_path) ||
    !value.section.heading_path.every(
      (part: unknown) => typeof part === "string",
    )
  ) {
    return false;
  }
  return (
    value.pages === null ||
    (Array.isArray(value.pages) &&
      value.pages.length === 2 &&
      isPositiveInteger(value.pages[0]) &&
      isPositiveInteger(value.pages[1]) &&
      value.pages[1] >= value.pages[0])
  );
}

function isSearchResult(value: unknown): value is SearchResult {
  return (
    isRecord(value) &&
    typeof value.retrieval_revision === "number" &&
    Number.isSafeInteger(value.retrieval_revision) &&
    value.retrieval_revision >= 0 &&
    (value.status === "ok" || value.status === "no_match") &&
    Array.isArray(value.degraded) &&
    value.degraded.every((item: unknown) => typeof item === "string") &&
    Array.isArray(value.evidence) &&
    value.evidence.every(isSearchEvidence) &&
    (value.status === "no_match") === (value.evidence.length === 0) &&
    typeof value.trace_id === "string" &&
    (value.trace === undefined || isSearchTrace(value.trace))
  );
}

function isSearchTrace(value: unknown): boolean {
  if (
    !isRecord(value) ||
    typeof value.retrieval_revision !== "number" ||
    !Number.isSafeInteger(value.retrieval_revision) ||
    value.retrieval_revision < 0 ||
    !Array.isArray(value.candidates) ||
    !isRecord(value.timings_ms) ||
    !Array.isArray(value.degraded) ||
    !value.degraded.every((v: unknown) => typeof v === "string") ||
    !isRecord(value.filters)
  )
    return false;
  return (
    Object.values(value.timings_ms).every(
      (v) => typeof v === "number" && Number.isFinite(v) && v >= 0,
    ) &&
    value.candidates.every((v: unknown) => {
      if (!isRecord(v)) return false;
      return (
        [v.ChunkID, v.DocumentID, v.VersionID, v.SectionID].every(
          isPositiveInteger,
        ) &&
        [v.Score, v.LexicalScore, v.SemanticDistance].every(
          (n) => typeof n === "number" && Number.isFinite(n),
        ) &&
        [v.LexicalRank, v.SemanticRank, v.RRFRank, v.RerankRank].every(
          (n) => typeof n === "number" && Number.isInteger(n) && n >= 0,
        ) &&
        (v.RerankScore === null ||
          (typeof v.RerankScore === "number" &&
            Number.isFinite(v.RerankScore))) &&
        typeof v.Title === "string"
      );
    })
  );
}

async function checked(response: Response): Promise<Response> {
  if (response.ok) return response;
  let data: unknown;
  try {
    data = await response.json();
  } catch {
    throw new ApiError(response.status, "invalid_response", "");
  }
  if (
    !isRecord(data) ||
    !isRecord(data.error) ||
    typeof data.error.code !== "string" ||
    typeof data.error.trace_id !== "string"
  ) {
    throw new ApiError(response.status, "invalid_response", "");
  }
  throw new ApiError(response.status, data.error.code, data.error.trace_id);
}

export async function getIdentity(signal: AbortSignal): Promise<Identity> {
  const response = await checked(
    await fetch("/api/v1/me", { signal, credentials: "same-origin" }),
  );
  const data: unknown = await response.json();
  if (!isIdentity(data)) throw new Error("invalid identity response");
  return data;
}

export async function login(input: Login): Promise<Identity> {
  const response = await checked(
    await fetch("/api/v1/auth/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-TuskRAG-Login": "1" },
      body: JSON.stringify(input),
    }),
  );
  const data: unknown = await response.json();
  if (!isIdentity(data)) throw new Error("invalid login response");
  return data;
}

export async function logout(csrf: string): Promise<void> {
  await checked(
    await fetch("/api/v1/auth/logout", {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
}

export async function listKnowledgeBases(
  signal: AbortSignal,
): Promise<KnowledgeBase[]> {
  const response = await checked(
    await fetch("/api/v1/kbs", { signal, credentials: "same-origin" }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !Array.isArray(data.items) ||
    !data.items.every(isKnowledgeBase)
  ) {
    throw new Error("invalid knowledge base list");
  }
  return data.items;
}

export async function getKnowledgeBase(
  id: number,
  signal: AbortSignal,
): Promise<KnowledgeBase> {
  if (!isPositiveInteger(id)) throw new Error("invalid knowledge base ID");
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}`, { signal, credentials: "same-origin" }),
  );
  const data: unknown = await response.json();
  if (!isKnowledgeBase(data))
    throw new Error("invalid knowledge base response");
  return data;
}

export async function listDocuments(
  id: number,
  cursor: number,
  signal: AbortSignal,
): Promise<DocumentPage> {
  if (!isPositiveInteger(id) || !Number.isSafeInteger(cursor) || cursor < 0) {
    throw new Error("invalid document page");
  }
  const params = new URLSearchParams({ cursor: String(cursor) });
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}/documents?${params}`, {
      signal,
      credentials: "same-origin",
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !Array.isArray(data.items) ||
    !data.items.every(isDocument) ||
    (data.next_cursor !== null &&
      (!isPositiveInteger(data.next_cursor) ||
        data.next_cursor <= cursor ||
        data.items.length === 0))
  ) {
    throw new Error("invalid document list");
  }
  return { items: data.items, next_cursor: data.next_cursor };
}

function isPurgeJob(value: unknown): value is PurgeJob {
  return (
    isRecord(value) &&
    isPositiveInteger(value.id) &&
    isPositiveInteger(value.kb_id) &&
    isPositiveInteger(value.document_id) &&
    isPositiveInteger(value.version_id) &&
    (value.document_title === null || typeof value.document_title === "string") &&
    typeof value.document_deleted === "boolean" &&
    [
      "queued",
      "running",
      "succeeded",
      "failed",
      "dead",
      "cancelled",
    ].includes(String(value.status)) &&
    typeof value.attempts === "number" &&
    Number.isSafeInteger(value.attempts) &&
    value.attempts >= 0 &&
    typeof value.max_attempts === "number" &&
    Number.isSafeInteger(value.max_attempts) &&
    value.max_attempts >= 1 &&
    (value.failure_code === null || typeof value.failure_code === "string") &&
    typeof value.retryable === "boolean" &&
    typeof value.created_at === "string" &&
    (value.finished_at === null || typeof value.finished_at === "string")
  );
}

export async function listPurgeJobs(
  id: number,
  cursor: number,
  signal: AbortSignal,
): Promise<PurgeJobPage> {
  if (!isPositiveInteger(id) || !Number.isSafeInteger(cursor) || cursor < 0) {
    throw new Error("invalid purge job page");
  }
  const params = new URLSearchParams({ cursor: String(cursor) });
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}/purge-jobs?${params}`, {
      signal,
      credentials: "same-origin",
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !Array.isArray(data.items) ||
    !data.items.every(isPurgeJob) ||
    (data.next_cursor !== null &&
      (!isPositiveInteger(data.next_cursor) ||
        (cursor > 0 && data.next_cursor >= cursor) ||
        data.items.length === 0))
  ) {
    throw new Error("invalid purge job list");
  }
  return { items: data.items, next_cursor: data.next_cursor };
}

export async function uploadDocument(
  id: number,
  file: File,
  csrf: string,
  idempotencyKey: string,
  allowDuplicate: boolean,
  confirmFirstRowHeaders: boolean,
): Promise<UploadedDocument> {
  if (!isPositiveInteger(id) || !idempotencyKey) {
    throw new Error("invalid document upload");
  }
  return uploadDocumentFile(
    `/api/v1/kbs/${id}/documents`,
    file,
    csrf,
    idempotencyKey,
    allowDuplicate,
    confirmFirstRowHeaders,
  );
}

export async function uploadDocumentVersion(
  id: number,
  documentId: number,
  file: File,
  csrf: string,
  idempotencyKey: string,
  allowDuplicate: boolean,
  confirmFirstRowHeaders: boolean,
): Promise<UploadedDocument> {
  if (
    !isPositiveInteger(id) ||
    !isPositiveInteger(documentId) ||
    !idempotencyKey
  ) {
    throw new Error("invalid document version upload");
  }
  return uploadDocumentFile(
    `/api/v1/kbs/${id}/documents/${documentId}/versions`,
    file,
    csrf,
    idempotencyKey,
    allowDuplicate,
    confirmFirstRowHeaders,
  );
}

async function uploadDocumentFile(
  url: string,
  file: File,
  csrf: string,
  idempotencyKey: string,
  allowDuplicate: boolean,
  confirmFirstRowHeaders: boolean,
): Promise<UploadedDocument> {
  const form = new FormData();
  form.append("file", file);
  const params = new URLSearchParams();
  if (allowDuplicate) params.set("allow_duplicate", "true");
  if (confirmFirstRowHeaders) params.set("confirm_first_row_headers", "true");
  const query = params.size > 0 ? `?${params}` : "";
  const response = await checked(
    await fetch(`${url}${query}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf, "Idempotency-Key": idempotencyKey },
      body: form,
    }),
  );
  const data: unknown = await response.json();
  if (!isUploadedDocument(data)) throw new Error("invalid upload response");
  return data;
}

export async function deleteDocument(
  id: number,
  documentId: number,
  csrf: string,
): Promise<void> {
  if (!isPositiveInteger(id) || !isPositiveInteger(documentId)) {
    throw new Error("invalid document deletion");
  }
  const response = await checked(
    await fetch(`/api/v1/kbs/${id}/documents/${documentId}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
  const data: unknown = await response.json();
  if (!isRecord(data) || data.status !== "deleting") {
    throw new Error("invalid document deletion response");
  }
}

export async function retryJob(jobId: number, csrf: string): Promise<number> {
  if (!isPositiveInteger(jobId)) throw new Error("invalid job ID");
  const response = await checked(
    await fetch(`/api/v1/jobs/${jobId}:retry`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.job_id) ||
    data.status !== "queued"
  ) {
    throw new Error("invalid job retry response");
  }
  return data.job_id;
}

export async function getDocumentContents(
  documentId: number,
  cursor: number,
  signal: AbortSignal,
): Promise<DocumentContents> {
  if (
    !isPositiveInteger(documentId) ||
    !Number.isSafeInteger(cursor) ||
    cursor < 0
  ) {
    throw new Error("invalid document contents request");
  }
  const params = new URLSearchParams({ cursor: String(cursor), limit: "30" });
  const response = await checked(
    await fetch(`/api/v1/documents/${documentId}/toc?${params}`, {
      signal,
      credentials: "same-origin",
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !isContentDocument(data.document) ||
    data.document.id !== documentId ||
    !Array.isArray(data.sections) ||
    data.sections.length > 30 ||
    !data.sections.every(isSectionReference)
  ) {
    throw new Error("invalid document contents response");
  }
  const sections = data.sections;
  let lastOrdinal = cursor;
  for (const item of sections) {
    if (item.ordinal <= lastOrdinal)
      throw new Error("invalid document contents order");
    lastOrdinal = item.ordinal;
  }
  const nextCursor = data.next_cursor;
  if (
    nextCursor !== null &&
    (!isPositiveInteger(nextCursor) ||
      sections.length === 0 ||
      nextCursor !== lastOrdinal)
  ) {
    throw new Error("invalid document contents cursor");
  }
  return {
    document: data.document,
    sections,
    next_cursor: nextCursor,
  };
}

export async function readSection(
  sectionId: number,
  signal: AbortSignal,
): Promise<SectionContent> {
  if (!isPositiveInteger(sectionId)) throw new Error("invalid section ID");
  const response = await checked(
    await fetch(`/api/v1/sections/${sectionId}`, {
      signal,
      credentials: "same-origin",
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !isContentDocument(data.document) ||
    !isSectionReference(data.section) ||
    data.section.id !== sectionId ||
    typeof data.markdown !== "string" ||
    typeof data.token_count !== "number" ||
    !Number.isSafeInteger(data.token_count) ||
    data.token_count < 0
  ) {
    throw new Error("invalid section response");
  }
  return {
    document: data.document,
    section: data.section,
    markdown: data.markdown,
    token_count: data.token_count,
  };
}

export async function searchKnowledgeBase(
  id: number,
  query: string,
  csrf: string,
  signal: AbortSignal,
  options?: components["schemas"]["SearchInputBody"]["options"],
): Promise<SearchResult> {
  if (!isPositiveInteger(id) || !query.trim()) {
    throw new Error("invalid search request");
  }
  const input: components["schemas"]["SearchInputBody"] = {
    query,
    kb_ids: [id],
    ...(options ? { options } : {}),
  };
  const response = await checked(
    await fetch("/api/v1/search", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(input),
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!isSearchResult(data)) throw new Error("invalid search response");
  return data;
}

export async function createKnowledgeBase(
  input: NewKnowledgeBase,
  csrf: string,
  idempotencyKey: string,
): Promise<KnowledgeBase> {
  const response = await checked(
    await fetch("/api/v1/kbs", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
        "Idempotency-Key": idempotencyKey,
      },
      body: JSON.stringify(input),
    }),
  );
  const data: unknown = await response.json();
  if (!isKnowledgeBase(data))
    throw new Error("invalid knowledge base response");
  return data;
}

export async function listEmbeddingSpaces(
  signal: AbortSignal,
): Promise<EmbeddingSpace[]> {
  const response = await checked(
    await fetch("/api/v1/admin/embedding-spaces", {
      signal,
      credentials: "same-origin",
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !Array.isArray(data.items) ||
    !data.items.every(isEmbeddingSpace)
  ) {
    throw new Error("invalid embedding space list");
  }
  return data.items;
}

export async function createEmbeddingSpace(
  input: NewEmbeddingSpace,
  csrf: string,
): Promise<EmbeddingSpace> {
  const response = await checked(
    await fetch("/api/v1/admin/embedding-spaces", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(input),
    }),
  );
  const data: unknown = await response.json();
  if (!isEmbeddingSpace(data))
    throw new Error("invalid embedding space response");
  return data;
}

export type EvalSetDefinition = components["schemas"]["SetDefinition"];
export type EvalSetCase = components["schemas"]["CaseDefinition"];
export type EvalSetTarget = components["schemas"]["ExpectedLocation"];
export type StoredEvalSet = components["schemas"]["StoredSet"];
export type EvalSetPage = components["schemas"]["SetPage"];
export type EvalSetSnapshot = { value: StoredEvalSet; etag: string };

const setBytes = (v: string) => new TextEncoder().encode(v).length;
const setText = (v: unknown, max: number): v is string =>
  typeof v === "string" &&
  v.trim().length > 0 &&
  !v.includes("\0") &&
  setBytes(v) <= max;

export function isEvalSetDefinition(
  value: unknown,
): value is EvalSetDefinition {
  if (
    !isRecord(value) ||
    !setText(value.name, 200) ||
    !Array.isArray(value.cases) ||
    value.cases.length < 1 ||
    value.cases.length > 10000
  )
    return false;
  const ids = new Set<string>();
  for (const c of value.cases) {
    if (
      !isRecord(c) ||
      !setText(c.id, 128) ||
      c.id !== c.id.trim() ||
      ids.has(c.id) ||
      !setText(c.question, 4096) ||
      typeof c.answerable !== "boolean"
    )
      return false;
    ids.add(c.id);
    if (
      c.reference_answer !== undefined &&
      c.reference_answer !== "" &&
      !setText(c.reference_answer, 32768)
    )
      return false;
    if (
      c.tags !== null &&
      (!Array.isArray(c.tags) ||
        c.tags.length > 32 ||
        !c.tags.every(
          (tag: unknown) => setText(tag, 128) && tag === tag.trim(),
        ) ||
        new Set(c.tags).size !== c.tags.length)
    )
      return false;
    if (c.expected !== null && !Array.isArray(c.expected)) return false;
    const expected = c.expected ?? [];
    if (expected.length > 100 || c.answerable !== expected.length > 0)
      return false;
    const targets = new Set<string>();
    for (const target of expected) {
      if (!isRecord(target) || !isPositiveInteger(target.document_id))
        return false;
      const section = target.section_id ?? 0,
        page = target.page ?? 0;
      if (
        typeof section !== "number" ||
        !Number.isSafeInteger(section) ||
        section < 0 ||
        typeof page !== "number" ||
        !Number.isInteger(page) ||
        page < 0 ||
        page > 2147483647
      )
        return false;
      const key = `${target.document_id}:${section}:${page}`;
      if (targets.has(key)) return false;
      targets.add(key);
    }
  }
  return setBytes(JSON.stringify(value)) <= 8 * 1024 * 1024;
}
function setComparable(d: EvalSetDefinition) {
  return JSON.stringify({
    name: d.name,
    cases: d.cases?.map((c) => ({
      id: c.id,
      question: c.question,
      answerable: c.answerable,
      reference_answer: c.reference_answer ?? "",
      tags: c.tags ?? [],
      expected: (c.expected ?? []).map((t) => [
        t.document_id,
        t.section_id ?? 0,
        t.page ?? 0,
      ]),
    })),
  });
}
async function setSnapshot(
  response: Response,
  id?: number,
  revision?: number,
  definition?: EvalSetDefinition,
): Promise<EvalSetSnapshot> {
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.id) ||
    !isPositiveInteger(data.revision) ||
    data.revision > 2147483647 ||
    typeof data.digest !== "string" ||
    !/^[0-9a-f]{64}$/.test(data.digest) ||
    typeof data.created_at !== "string" ||
    !Number.isFinite(Date.parse(data.created_at)) ||
    !isEvalSetDefinition(data.definition) ||
    (id !== undefined && data.id !== id) ||
    (revision !== undefined && data.revision !== revision) ||
    (definition &&
      setComparable(definition) !== setComparable(data.definition)) ||
    response.headers.get("ETag") !== `"${data.revision}"`
  )
    throw new Error("invalid evaluation set response");
  return { value: data as StoredEvalSet, etag: `"${data.revision}"` };
}
export async function listEvalSets(
  kb: number,
  cursor: number,
  signal: AbortSignal,
): Promise<EvalSetPage> {
  if (!isPositiveInteger(kb) || !Number.isSafeInteger(cursor) || cursor < 0)
    throw new Error("invalid evaluation set list request");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/eval-sets?cursor=${cursor}&limit=10`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!isRecord(data) || !Array.isArray(data.items) || data.items.length > 10)
    throw new Error("invalid evaluation set page");
  let previous = cursor;
  for (const item of data.items) {
    if (
      !isRecord(item) ||
      !isPositiveInteger(item.id) ||
      item.id <= previous ||
      !isPositiveInteger(item.revision) ||
      item.revision > 2147483647 ||
      !setText(item.name, 200) ||
      !isPositiveInteger(item.case_count) ||
      item.case_count > 10000 ||
      typeof item.digest !== "string" ||
      !/^[0-9a-f]{64}$/.test(item.digest) ||
      typeof item.created_at !== "string" ||
      !Number.isFinite(Date.parse(item.created_at))
    )
      throw new Error("invalid evaluation set page");
    previous = item.id;
  }
  if (
    data.next_cursor !== null &&
    (data.items.length !== 10 || data.next_cursor !== previous)
  )
    throw new Error("invalid evaluation set cursor");
  return data as EvalSetPage;
}
export async function getEvalSet(
  kb: number,
  id: number,
  signal: AbortSignal,
): Promise<EvalSetSnapshot> {
  if (!isPositiveInteger(kb) || !isPositiveInteger(id))
    throw new Error("invalid evaluation set request");
  return setSnapshot(
    await checked(
      await fetch(`/api/v1/kbs/${kb}/eval-sets/${id}`, {
        credentials: "same-origin",
        signal,
      }),
    ),
    id,
  );
}
export async function saveEvalSet(
  kb: number,
  snapshot: EvalSetSnapshot | null,
  definition: EvalSetDefinition,
  csrf: string,
  key: string,
): Promise<EvalSetSnapshot> {
  if (
    !isPositiveInteger(kb) ||
    !isEvalSetDefinition(definition) ||
    (!snapshot && !/^[\x21-\x7e]{1,128}$/.test(key)) ||
    (snapshot &&
      (!isPositiveInteger(snapshot.value.id) ||
        snapshot.etag !== `"${snapshot.value.revision}"`))
  )
    throw new Error("invalid evaluation set save");
  const response = await checked(
    await fetch(
      `/api/v1/kbs/${kb}/eval-sets${snapshot ? `/${snapshot.value.id}` : ""}`,
      {
        method: snapshot ? "PUT" : "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrf,
          ...(snapshot
            ? { "If-Match": snapshot.etag }
            : { "Idempotency-Key": key }),
        },
        body: JSON.stringify(definition),
      },
    ),
  );
  return setSnapshot(
    response,
    snapshot?.value.id,
    snapshot ? snapshot.value.revision + 1 : 1,
    definition,
  );
}

type Readiness =
  paths["/api/health/ready"]["get"]["responses"][200]["content"]["application/json"];

export async function getReadiness(signal: AbortSignal): Promise<Readiness> {
  const response = await fetch("/api/health/ready", { signal });
  if (!response.ok)
    throw new Error(`readiness request failed: ${response.status}`);
  const data: unknown = await response.json();
  if (
    typeof data !== "object" ||
    data === null ||
    !("status" in data) ||
    data.status !== "ok"
  ) {
    throw new Error("invalid readiness response");
  }
  return { status: data.status };
}

export type EvalRunInput = components["schemas"]["StartRequest"];
export type EvalRunStatus = components["schemas"]["RunStatus"];
export type StartedEvalRun = components["schemas"]["StartedRun"];
export async function startEvalRun(
  kb: number,
  input: EvalRunInput,
  csrf: string,
  key: string,
): Promise<StartedEvalRun> {
  if (
    !isPositiveInteger(kb) ||
    !isPositiveInteger(input.set_id) ||
    !isPositiveInteger(input.revision) ||
    input.revision > 2147483647 ||
    !["lexical", "vector", "rrf", "rerank"].includes(input.mode) ||
    !/^[\x21-\x7e]{1,128}$/.test(key)
  )
    throw new Error("invalid evaluation start");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/eval-runs`, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
        "Idempotency-Key": key,
      },
      body: JSON.stringify(input),
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.id) ||
    !isPositiveInteger(data.job_id)
  )
    throw new Error("invalid evaluation start response");
  return data as StartedEvalRun;
}
export async function getEvalRun(
  kb: number,
  id: number,
  signal: AbortSignal,
): Promise<EvalRunStatus> {
  if (!isPositiveInteger(kb) || !isPositiveInteger(id))
    throw new Error("invalid evaluation run request");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/eval-runs/${id}`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!isEvalRunStatus(data) || data.id !== id)
    throw new Error("invalid evaluation run status");
  return data;
}
function isEvalRunStatus(data: unknown): data is EvalRunStatus {
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.id) ||
    !isPositiveInteger(data.job_id) ||
    !isRecord(data.set) ||
    !isPositiveInteger(data.set.set_id) ||
    !isPositiveInteger(data.set.revision) ||
    typeof data.mode !== "string" ||
    !["lexical", "vector", "rrf", "rerank"].includes(data.mode) ||
    typeof data.status !== "string" ||
    !["queued", "running", "succeeded", "failed", "dead", "cancelled"].includes(
      data.status,
    ) ||
    typeof data.attempts !== "number" ||
    !Number.isInteger(data.attempts) ||
    data.attempts < 0 ||
    !isPositiveInteger(data.max_attempts) ||
    data.attempts > data.max_attempts ||
    (data.failure_code !== null && typeof data.failure_code !== "string") ||
    data.result_available !== (data.status === "succeeded") ||
    typeof data.created_at !== "string" ||
    !Number.isFinite(Date.parse(data.created_at)) ||
    (data.finished_at !== null &&
      (typeof data.finished_at !== "string" ||
        !Number.isFinite(Date.parse(data.finished_at))))
  )
    return false;
  return true;
}
export async function getEvalRunResult(
  kb: number,
  id: number,
  signal: AbortSignal,
): Promise<StoredEvalReport> {
  if (!isPositiveInteger(kb) || !isPositiveInteger(id))
    throw new Error("invalid evaluation result request");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/eval-runs/${id}/result`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    data.source !== "executed" ||
    typeof data.digest !== "string" ||
    !/^[0-9a-f]{64}$/.test(data.digest) ||
    typeof data.created_at !== "string" ||
    !Number.isFinite(Date.parse(data.created_at)) ||
    !isEvalReport(data.report)
  )
    throw new Error("invalid executed evaluation result");
  return data as StoredEvalReport;
}

export type EvalRunPage = components["schemas"]["RunPage"] & {
  items: NonNullable<components["schemas"]["RunPage"]["items"]>;
};
export async function listEvalRuns(
  kb: number,
  cursor: number,
  signal: AbortSignal,
): Promise<EvalRunPage> {
  if (!isPositiveInteger(kb) || !Number.isSafeInteger(cursor) || cursor < 0)
    throw new Error("invalid evaluation history request");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/eval-runs?cursor=${cursor}&limit=10`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!isRecord(data) || !Array.isArray(data.items) || data.items.length > 10)
    throw new Error("invalid evaluation history");
  let previous = cursor;
  for (const item of data.items) {
    if (!isEvalRunStatus(item) || item.id <= previous)
      throw new Error("invalid evaluation history item");
    previous = item.id;
  }
  if (
    data.next_cursor !== null &&
    (data.items.length !== 10 || data.next_cursor !== previous)
  )
    throw new Error("invalid evaluation history cursor");
  return data as EvalRunPage;
}

export type EvalRunComparison = components["schemas"]["RunComparison"];
export async function compareEvalRuns(
  kb: number,
  baseline: number,
  current: number,
  offset: number,
  signal: AbortSignal,
): Promise<EvalRunComparison> {
  if (
    !isPositiveInteger(kb) ||
    !isPositiveInteger(baseline) ||
    !isPositiveInteger(current) ||
    !Number.isSafeInteger(offset) ||
    offset < 0 ||
    offset > 10000
  )
    throw new Error("invalid run comparison address");
  const response = await checked(
    await fetch(
      `/api/v1/kbs/${kb}/eval-run-comparison?${new URLSearchParams({ baseline: String(baseline), current: String(current), offset: String(offset), limit: "20" })}`,
      { credentials: "same-origin", signal },
    ),
  );
  const data: unknown = await response.json();
  if (
    !isRecord(data) ||
    data.source !== "executed" ||
    data.baseline_run_id !== baseline ||
    data.current_run_id !== current ||
    !isRecord(data.comparison) ||
    typeof data.comparison.baseline_digest !== "string" ||
    !evalDigest.test(data.comparison.baseline_digest) ||
    typeof data.comparison.current_digest !== "string" ||
    !evalDigest.test(data.comparison.current_digest)
  )
    throw new Error("invalid run comparison response");
  return {
    baseline_run_id: baseline,
    current_run_id: current,
    source: "executed",
    comparison: decodeComparison(
      data.comparison,
      data.comparison.baseline_digest,
      data.comparison.current_digest,
      offset,
    ),
  };
}

export type Rebuild = components["schemas"]["Rebuild"];
export type RebuildInput = components["schemas"]["RebuildRequest"];

function isRebuild(data: unknown): data is Rebuild {
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.job_id) ||
    !isPositiveInteger(data.source_space_id) ||
    !isPositiveInteger(data.target_space_id) ||
    data.source_space_id === data.target_space_id ||
    !isPositiveInteger(data.source_revision) ||
    data.source_revision > 2147483646 ||
    typeof data.status !== "string" ||
    !["queued", "running", "succeeded", "failed", "dead", "cancelled"].includes(
      data.status,
    ) ||
    typeof data.attempts !== "number" ||
    !Number.isSafeInteger(data.attempts) ||
    data.attempts < 0 ||
    !isPositiveInteger(data.max_attempts) ||
    data.attempts > data.max_attempts ||
    (data.failure_code !== null &&
      (typeof data.failure_code !== "string" ||
        data.failure_code.length === 0)) ||
    typeof data.created_at !== "string" ||
    !Number.isFinite(Date.parse(data.created_at)) ||
    (data.finished_at !== null &&
      (typeof data.finished_at !== "string" ||
        !Number.isFinite(Date.parse(data.finished_at))))
  )
    return false;
  const terminal = ["succeeded", "failed", "dead", "cancelled"].includes(
    data.status,
  );
  return (
    terminal === (data.finished_at !== null) &&
    (!(data.status === "failed" || data.status === "dead") ||
      data.failure_code !== null) &&
    (data.finished_at === null ||
      Date.parse(data.finished_at) >= Date.parse(data.created_at))
  );
}

export async function createRebuild(
  kb: number,
  input: RebuildInput,
  csrf: string,
  key: string,
): Promise<Rebuild> {
  if (
    !isPositiveInteger(kb) ||
    !isPositiveInteger(input.target_space_id) ||
    !isPositiveInteger(input.revision) ||
    input.revision > 2147483646 ||
    !/^[\x21-\x7e]{1,128}$/.test(key)
  )
    throw new Error("invalid rebuild request");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/rebuilds`, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
        "Idempotency-Key": key,
      },
      body: JSON.stringify({
        target_space_id: input.target_space_id,
        revision: input.revision,
      }),
    }),
  );
  const data: unknown = await response.json();
  if (
    !isRebuild(data) ||
    data.target_space_id !== input.target_space_id ||
    data.source_revision !== input.revision
  )
    throw new Error("invalid rebuild response");
  return data;
}

export async function getRebuild(
  kb: number,
  job: number,
  signal: AbortSignal,
): Promise<Rebuild> {
  if (!isPositiveInteger(kb) || !isPositiveInteger(job))
    throw new Error("invalid rebuild request");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/rebuilds/${job}`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!isRebuild(data) || data.job_id !== job)
    throw new Error("invalid rebuild response");
  return data;
}

export async function cancelRebuild(
  kb: number,
  job: number,
  csrf: string,
): Promise<void> {
  if (!isPositiveInteger(kb) || !isPositiveInteger(job))
    throw new Error("invalid rebuild cancellation");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/rebuilds/${job}/cancel`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
  const data: unknown = await response.json();
  if (!isRecord(data) || data.status !== "cancelled")
    throw new Error("invalid rebuild cancellation response");
}
