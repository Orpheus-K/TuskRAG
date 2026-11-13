import { ApiError } from "./index";
import type { components } from "./schema";

export type AuditLogEntry = components["schemas"]["AuditLogEntryBody"];
export type TenantEgressPolicy = components["schemas"]["TenantEgressPolicyBody"];
export type ModelProvider = components["schemas"]["ModelProviderBody"];
export type ModelProviderInput = components["schemas"]["CreateModelProviderInputBody"];
export type ProviderConnectivity = components["schemas"]["ProviderConnectivityBody"];

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

async function checked(response: Response) {
  if (response.ok) return response;
  const value: unknown = await response.json().catch(() => null);
  throw new ApiError(
    response.status,
    isRecord(value) && isRecord(value.error) && typeof value.error.code === "string"
      ? value.error.code
      : "http_error",
    isRecord(value) &&
      isRecord(value.error) &&
      typeof value.error.message === "string"
      ? value.error.message
      : response.statusText,
  );
}

export interface AuditLogQuery {
  action?: string;
  actor_user_id?: number;
  object_type?: string;
  result?: string;
  from?: string;
  to?: string;
  before_id?: number;
  limit?: number;
}

function buildAuditParams(query: AuditLogQuery): string {
  const params = new URLSearchParams();
  if (query.action) params.set("action", query.action);
  if (query.actor_user_id) params.set("actor_user_id", String(query.actor_user_id));
  if (query.object_type) params.set("object_type", query.object_type);
  if (query.result) params.set("result", query.result);
  if (query.from) params.set("from", query.from);
  if (query.to) params.set("to", query.to);
  if (query.before_id) params.set("before_id", String(query.before_id));
  if (query.limit) params.set("limit", String(query.limit));
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

export async function listAuditLogs(
  signal: AbortSignal,
  query: AuditLogQuery = {},
): Promise<AuditLogEntry[]> {
  const res = await checked(
    await fetch(`/api/v1/admin/audit-logs${buildAuditParams(query)}`, { signal }),
  );
  const body = (await res.json()) as { items?: AuditLogEntry[] };
  return Array.isArray(body.items) ? body.items : [];
}

export async function exportAuditLogs(
  signal: AbortSignal,
  query: AuditLogQuery,
  format: "jsonl" | "csv",
): Promise<string> {
  const params = new URLSearchParams(buildAuditParams(query).replace(/^\?/, ""));
  params.set("format", format);
  const res = await checked(
    await fetch(`/api/v1/admin/audit-logs/export?${params.toString()}`, { signal }),
  );
  return res.text();
}

export async function getTenantEgressPolicy(
  signal: AbortSignal,
): Promise<TenantEgressPolicy> {
  const res = await checked(
    await fetch("/api/v1/admin/tenant-egress", { signal }),
  );
  return (await res.json()) as TenantEgressPolicy;
}

export async function updateTenantEgressPolicy(
  csrf: string,
  egress_policy_max: "deny" | "internal_only" | "allow",
): Promise<TenantEgressPolicy> {
  const res = await checked(
    await fetch("/api/v1/admin/tenant-egress", {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify({ egress_policy_max }),
    }),
  );
  return (await res.json()) as TenantEgressPolicy;
}

export async function listModelProviders(
  signal: AbortSignal,
): Promise<ModelProvider[]> {
  const res = await checked(
    await fetch("/api/v1/admin/model-providers", { signal }),
  );
  const body = (await res.json()) as { items?: ModelProvider[] };
  return Array.isArray(body.items) ? body.items : [];
}

export async function createModelProvider(
  csrf: string,
  input: ModelProviderInput,
): Promise<ModelProvider> {
  const res = await checked(
    await fetch("/api/v1/admin/model-providers", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify(input),
    }),
  );
  return (await res.json()) as ModelProvider;
}

export async function updateModelProvider(
  csrf: string,
  providerId: number,
  input: ModelProviderInput,
): Promise<ModelProvider> {
  const res = await checked(
    await fetch(`/api/v1/admin/model-providers/${providerId}`, {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify(input),
    }),
  );
  return (await res.json()) as ModelProvider;
}

export async function deleteModelProvider(
  csrf: string,
  providerId: number,
): Promise<void> {
  await checked(
    await fetch(`/api/v1/admin/model-providers/${providerId}`, {
      method: "DELETE",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
}

export async function testModelProvider(
  csrf: string,
  providerId: number,
): Promise<ProviderConnectivity> {
  const res = await checked(
    await fetch(`/api/v1/admin/model-providers/${providerId}/test`, {
      method: "POST",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
  return (await res.json()) as ProviderConnectivity;
}
