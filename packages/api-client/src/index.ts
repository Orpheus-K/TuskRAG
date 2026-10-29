import type { components, paths } from "./schema";

export type Identity = components["schemas"]["IdentityBody"];
export type Login = components["schemas"]["LoginInputBody"];
export type KnowledgeBase = components["schemas"]["KnowledgeBaseBody"];
export type Document = components["schemas"]["DocumentBody"];
export type DocumentPage = components["schemas"]["DocumentListOutputBody"];
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
    top_k: 8,
    options: options ?? { rerank: false, expand_parent: false },
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
