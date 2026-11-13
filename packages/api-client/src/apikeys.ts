import { ApiError } from "./index";
import type { components } from "./schema";

export type APIKey = components["schemas"]["APIKeyBody"];
export type CreateAPIKeyInput = components["schemas"]["CreateAPIKeyInputBody"];
export type CreateAPIKeyResult =
  components["schemas"]["CreateAPIKeyOutputBody"];

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

export async function listAPIKeys(signal: AbortSignal): Promise<APIKey[]> {
  const response = await checked(
    await fetch("/api/v1/api-keys", { credentials: "same-origin", signal }),
  );
  const data: unknown = await response.json();
  if (!record(data) || !Array.isArray(data.items)) {
    throw new Error("invalid api keys response");
  }
  return data.items as APIKey[];
}

export async function createAPIKey(
  input: CreateAPIKeyInput,
  csrf: string,
): Promise<CreateAPIKeyResult> {
  const response = await checked(
    await fetch("/api/v1/api-keys", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify(input),
    }),
  );
  const data: unknown = await response.json();
  if (
    !record(data) ||
    !record(data.key) ||
    !positive(data.key.id) ||
    typeof data.plaintext !== "string"
  ) {
    throw new Error("invalid api key creation response");
  }
  return data as CreateAPIKeyResult;
}

export async function revokeAPIKey(keyId: number, csrf: string): Promise<void> {
  if (!positive(keyId)) throw new Error("invalid api key id");
  await checked(
    await fetch(`/api/v1/api-keys/${keyId}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
}
