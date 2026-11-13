import { ApiError } from "./index";
import type { components } from "./schema";

export type UserGroup = components["schemas"]["UserGroupBody"];
export type GroupMember = components["schemas"]["GroupMemberBody"];
export type ACLSubject = components["schemas"]["ACLSubjectBody"];
export type DocumentACL = components["schemas"]["DocumentACLBody"];
export type ACLMode = "inherit_kb" | "restricted";

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

export async function listGroups(signal: AbortSignal): Promise<UserGroup[]> {
  const response = await checked(
    await fetch("/api/v1/groups", { credentials: "same-origin", signal }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !Array.isArray(data.items)) {
    throw new Error("invalid user groups response");
  }
  return data.items as UserGroup[];
}

export async function createGroup(
  name: string,
  csrf: string,
): Promise<UserGroup> {
  const response = await checked(
    await fetch("/api/v1/groups", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify({ name }),
    }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !positive(data.id)) {
    throw new Error("invalid user group response");
  }
  return data as UserGroup;
}

export async function deleteGroup(groupId: number, csrf: string): Promise<void> {
  if (!positive(groupId)) throw new Error("invalid group id");
  await checked(
    await fetch(`/api/v1/groups/${groupId}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
}

export async function listGroupMembers(
  groupId: number,
  signal: AbortSignal,
): Promise<GroupMember[]> {
  if (!positive(groupId)) throw new Error("invalid group id");
  const response = await checked(
    await fetch(`/api/v1/groups/${groupId}/members`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !Array.isArray(data.items)) {
    throw new Error("invalid group members response");
  }
  return data.items as GroupMember[];
}

export async function addGroupMember(
  groupId: number,
  userId: number,
  csrf: string,
): Promise<void> {
  if (!positive(groupId) || !positive(userId)) {
    throw new Error("invalid group or user id");
  }
  await checked(
    await fetch(`/api/v1/groups/${groupId}/members/${userId}`, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
}

export async function removeGroupMember(
  groupId: number,
  userId: number,
  csrf: string,
): Promise<void> {
  if (!positive(groupId) || !positive(userId)) {
    throw new Error("invalid group or user id");
  }
  await checked(
    await fetch(`/api/v1/groups/${groupId}/members/${userId}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
}

export async function getDocumentACL(
  kb: number,
  doc: number,
  signal: AbortSignal,
): Promise<DocumentACL> {
  if (!positive(kb) || !positive(doc)) {
    throw new Error("invalid kb or document id");
  }
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/documents/${doc}/acl`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !positive(data.document_id)) {
    throw new Error("invalid document acl response");
  }
  return data as DocumentACL;
}

export async function setDocumentACL(
  kb: number,
  doc: number,
  aclMode: ACLMode,
  subjects: ACLSubject[],
  csrf: string,
): Promise<DocumentACL> {
  if (!positive(kb) || !positive(doc)) {
    throw new Error("invalid kb or document id");
  }
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/documents/${doc}/acl`, {
      method: "PUT",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify({ acl_mode: aclMode, subjects }),
    }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !positive(data.document_id)) {
    throw new Error("invalid document acl response");
  }
  return data as DocumentACL;
}
