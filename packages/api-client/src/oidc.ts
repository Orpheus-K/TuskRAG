import { ApiError } from "./index";
import type { components } from "./schema";

export type OIDCProvider = components["schemas"]["OIDCProviderBody"];
export type CreateOIDCProviderInput =
  components["schemas"]["CreateOIDCProviderInputBody"];
export type TenantAuthPolicy = components["schemas"]["TenantAuthPolicyBody"];

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

async function checked(response: Response) {
  if (response.ok) return response;
  const value: unknown = await response.json().catch(() => null);
  throw new ApiError(
    response.status,
    isRecord(value) &&
      isRecord(value.error) &&
      typeof value.error.code === "string"
      ? value.error.code
      : "http_error",
    isRecord(value) &&
      isRecord(value.error) &&
      typeof value.error.message === "string"
      ? value.error.message
      : response.statusText,
  );
}

export async function getPublicOIDCProviders(
  signal: AbortSignal,
  tenantId: number,
): Promise<TenantAuthPolicy> {
  const res = await checked(
    await fetch(`/api/v1/auth/oidc/providers?tenant_id=${tenantId}`, {
      signal,
    }),
  );
  return (await res.json()) as TenantAuthPolicy;
}

export async function startOIDCLogin(
  providerId: number,
  tenantId: number,
  redirectUri: string,
): Promise<string> {
  const params = new URLSearchParams({
    tenant_id: String(tenantId),
    redirect_uri: redirectUri,
  });
  const res = await checked(
    await fetch(`/api/v1/auth/oidc/${providerId}/start?${params.toString()}`),
  );
  const body = (await res.json()) as { authorize_url: string };
  return body.authorize_url;
}

export async function listOIDCProviders(
  signal: AbortSignal,
): Promise<OIDCProvider[]> {
  const res = await checked(
    await fetch("/api/v1/admin/oidc-providers", { signal }),
  );
  const body = (await res.json()) as { items?: OIDCProvider[] };
  return Array.isArray(body.items) ? body.items : [];
}

export async function createOIDCProvider(
  csrf: string,
  input: CreateOIDCProviderInput,
): Promise<OIDCProvider> {
  const res = await checked(
    await fetch("/api/v1/admin/oidc-providers", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify(input),
    }),
  );
  return (await res.json()) as OIDCProvider;
}

export async function deleteOIDCProvider(
  csrf: string,
  providerId: number,
): Promise<void> {
  await checked(
    await fetch(`/api/v1/admin/oidc-providers/${providerId}`, {
      method: "DELETE",
      headers: { "X-CSRF-Token": csrf },
    }),
  );
}

export async function updateAuthPolicy(
  csrf: string,
  disable_local_login: boolean,
): Promise<{ disable_local_login: boolean }> {
  const res = await checked(
    await fetch("/api/v1/admin/auth-policy", {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify({ disable_local_login }),
    }),
  );
  return (await res.json()) as { disable_local_login: boolean };
}
