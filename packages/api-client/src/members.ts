import { ApiError } from "./index";
import type { components } from "./schema";

export type KnowledgeBaseMember =
  components["schemas"]["KnowledgeBaseMemberBody"];
export type TenantUser = components["schemas"]["TenantUserBody"];
export type KnowledgeBaseRole = "owner" | "admin" | "editor" | "viewer";
export type EgressPolicy = "deny" | "internal_only" | "allow";

const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const positive = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v > 0;

async function checked(response: Response) {
  if (response.ok) return response;
  const value: unknown = await response.json().catch(() => null);
  throw new ApiError(
    response.status,
    record(value) && record(value.error) && typeof value.error.code === "string"
      ? value.error.code
      : "http_error",
    record(value) &&
      record(value.error) &&
      typeof value.error.message === "string"
      ? value.error.message
      : response.statusText,
  );
}

export async function listTenantUsers(
  signal: AbortSignal,
): Promise<TenantUser[]> {
  const response = await checked(
    await fetch("/api/v1/tenant/users", {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !Array.isArray(data.items)) {
    throw new Error("invalid tenant users response");
  }
  return data.items as TenantUser[];
}

export async function listKnowledgeBaseMembers(
  kb: number,
  signal: AbortSignal,
): Promise<KnowledgeBaseMember[]> {
  if (!positive(kb)) throw new Error("invalid knowledge base id");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/members`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !Array.isArray(data.items)) {
    throw new Error("invalid knowledge base members response");
  }
  return data.items as KnowledgeBaseMember[];
}

export async function setKnowledgeBaseMember(
  kb: number,
  userId: number,
  role: KnowledgeBaseRole,
  csrf: string,
): Promise<void> {
  if (!positive(kb) || !positive(userId)) {
    throw new Error("invalid knowledge base or user id");
  }
  await checked(
    await fetch(`/api/v1/kbs/${kb}/members/${userId}`, {
      method: "PUT",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify({ role }),
    }),
  );
}

export async function removeKnowledgeBaseMember(
  kb: number,
  userId: number,
  csrf: string,
): Promise<void> {
  if (!positive(kb) || !positive(userId)) {
    throw new Error("invalid knowledge base or user id");
  }
  await checked(
    await fetch(`/api/v1/kbs/${kb}/members/${userId}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: {
        "X-CSRF-Token": csrf,
      },
    }),
  );
}

export async function setKnowledgeBaseEgressPolicy(
  kb: number,
  egressPolicy: EgressPolicy,
  csrf: string,
): Promise<components["schemas"]["KnowledgeBaseBody"]> {
  if (!positive(kb)) throw new Error("invalid knowledge base id");
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/egress-policy`, {
      method: "PUT",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify({ egress_policy: egressPolicy }),
    }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !positive(data.id)) {
    throw new Error("invalid knowledge base response");
  }
  return data as components["schemas"]["KnowledgeBaseBody"];
}
