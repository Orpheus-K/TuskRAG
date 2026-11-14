import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet } from "@tanstack/react-router";
import {
  ApiError,
  getIdentity,
  getPublicOIDCProviders,
  getReadiness,
  type Identity,
  listKnowledgeBases,
  login,
  logout,
  startOIDCLogin,
  type TenantAuthPolicy,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { createContext, type FormEvent, useEffect, useState } from "react";
import { ChatModels } from "./ChatModels";
import { Setup } from "./Setup";

export const IdentityContext = createContext<Identity | null>(null);

function messageFor(error: unknown): string {
  if (error instanceof ApiError && error.status === 401) {
    return "租户编号、账号或密码不正确，或账号已锁定。";
  }
  if (error instanceof ApiError && error.status === 429) {
    return "请求过多，请稍后再试。";
  }
  return "暂时无法完成操作，请检查服务状态后重试。";
}

function LoginForm({ onLogin }: { onLogin: (identity: Identity) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [tenantInput, setTenantInput] = useState("");
  const [authPolicy, setAuthPolicy] = useState<TenantAuthPolicy | null>(null);

  useEffect(() => {
    const tid = Number(tenantInput);
    if (!Number.isSafeInteger(tid) || tid <= 0) {
      setAuthPolicy(null);
      return;
    }
    const controller = new AbortController();
    void getPublicOIDCProviders(controller.signal, tid)
      .then((res) => setAuthPolicy(res))
      .catch(() => setAuthPolicy(null));
    return () => controller.abort();
  }, [tenantInput]);

  async function onSSO(providerId: number) {
    const tid = Number(tenantInput);
    if (!Number.isSafeInteger(tid) || tid <= 0) return;
    setBusy(true);
    setError("");
    try {
      const redirectUri = `${window.location.origin}/api/v1/auth/oidc/callback`;
      const authorizeUrl = await startOIDCLogin(providerId, tid, redirectUri);
      window.location.assign(authorizeUrl);
    } catch (cause) {
      setError(messageFor(cause));
      setBusy(false);
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const tenant = data.get("tenant");
    const account = data.get("login");
    const password = data.get("password");
    const tenantId = typeof tenant === "string" ? Number(tenant) : 0;
    if (
      !Number.isSafeInteger(tenantId) ||
      tenantId <= 0 ||
      typeof account !== "string" ||
      typeof password !== "string"
    ) {
      setError("请填写有效的租户编号、账号和密码。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      onLogin(await login({ tenant_id: tenantId, login: account, password }));
    } catch (cause) {
      setError(messageFor(cause));
    } finally {
      const field = form.elements.namedItem("password");
      if (field instanceof HTMLInputElement) field.value = "";
      setBusy(false);
    }
  }

  const inputClass =
    "h-10 w-full rounded-md border bg-background px-3 text-sm outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring";

  return (
    <section className="mx-auto w-full max-w-md py-16 sm:py-24">
      <p className="text-sm font-medium text-muted-foreground">企业知识空间</p>
      <h1 className="mt-3 text-3xl font-semibold tracking-tight">
        登录 TuskRAG
      </h1>
      <p className="mt-3 text-sm leading-6 text-muted-foreground">
        使用管理员提供的租户编号和账号登录。
      </p>
      <form className="mt-8 space-y-5 rounded-xl border p-6" onSubmit={submit}>
        <div className="space-y-2">
          <label className="text-sm font-medium" htmlFor="tenant">
            租户编号
          </label>
          <input
            className={inputClass}
            id="tenant"
            name="tenant"
            type="number"
            min="1"
            step="1"
            inputMode="numeric"
            autoComplete="off"
            value={tenantInput}
            onChange={(e) => setTenantInput(e.target.value)}
            required
          />
        </div>
        {authPolicy &&
        Array.isArray(authPolicy.providers) &&
        authPolicy.providers.length > 0 ? (
          <div className="space-y-2 border-b pb-4">
            <p className="text-xs text-muted-foreground">
              企业单点登录（OIDC）
            </p>
            <div className="flex flex-wrap gap-2">
              {authPolicy.providers.map((p) => (
                <Button
                  key={p.id}
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => void onSSO(p.id)}
                >
                  使用 {p.name} 登录
                </Button>
              ))}
            </div>
          </div>
        ) : null}
        {authPolicy?.disable_local_login ? (
          <p className="text-sm text-muted-foreground">
            该租户已禁用本地密码登录，请使用上方企业 SSO 登录。
          </p>
        ) : null}
        <div className="space-y-2">
          <label className="text-sm font-medium" htmlFor="login">
            账号
          </label>
          <input
            className={inputClass}
            id="login"
            name="login"
            autoComplete="username"
            required
          />
        </div>
        <div className="space-y-2">
          <label className="text-sm font-medium" htmlFor="password">
            密码
          </label>
          <input
            className={inputClass}
            id="password"
            name="password"
            type="password"
            autoComplete="current-password"
            required
          />
        </div>
        {error && (
          <p className="text-sm text-muted-foreground" role="alert">
            {error}
          </p>
        )}
        <Button className="w-full" type="submit" disabled={busy}>
          {busy ? "正在登录" : "登录"}
        </Button>
      </form>
    </section>
  );
}

export function Workspace({ identity }: { identity: Identity }) {
  const queryClient = useQueryClient();
  const [loggingOut, setLoggingOut] = useState(false);
  const [error, setError] = useState("");
  const knowledgeBases = useQuery({
    queryKey: ["knowledge-bases", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listKnowledgeBases(signal),
    retry: false,
  });
  const roleLabels: Record<string, string> = {
    owner: "所有者",
    admin: "管理员",
    editor: "编辑者",
    viewer: "查看者",
  };

  async function forgetSession() {
    await queryClient.cancelQueries({ queryKey: ["identity"] });
    queryClient.removeQueries({ queryKey: ["knowledge-bases"] });
    queryClient.removeQueries({ queryKey: ["knowledge-base"] });
    queryClient.removeQueries({ queryKey: ["documents"] });
    queryClient.removeQueries({ queryKey: ["search"] });
    queryClient.removeQueries({ queryKey: ["embedding-spaces"] });
    queryClient.setQueryData(["identity"], null);
  }

  async function signOut() {
    setLoggingOut(true);
    setError("");
    try {
      await logout(identity.csrf_token);
      await forgetSession();
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 401) {
        await forgetSession();
      } else {
        setError(messageFor(cause));
      }
    } finally {
      setLoggingOut(false);
    }
  }

  return (
    <section className="py-12 sm:py-16">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="text-sm text-muted-foreground">
            租户 {identity.tenant_id}
          </p>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight">知识库</h1>
          <p className="mt-3 text-sm text-muted-foreground">
            查看你有权访问的知识库。
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Link
            className="inline-flex h-9 items-center justify-center rounded-md border border-border bg-background px-4 py-2 text-sm font-medium shadow-sm hover:bg-muted"
            to="/admin"
          >
            平台治理
          </Link>
          <Link
            className="inline-flex h-9 items-center justify-center rounded-md border border-border bg-background px-4 py-2 text-sm font-medium shadow-sm hover:bg-muted"
            to="/api-keys"
          >
            API Key 管理
          </Link>
          <Button
            variant="outline"
            onClick={() => void signOut()}
            disabled={loggingOut}
          >
            {loggingOut ? "正在退出" : "退出登录"}
          </Button>
        </div>
      </div>
      {error && (
        <p className="mt-5 text-sm text-muted-foreground" role="alert">
          {error}
        </p>
      )}
      {knowledgeBases.isPending ? (
        <p className="mt-10 text-sm text-muted-foreground" role="status">
          正在加载知识库
        </p>
      ) : knowledgeBases.isError ? (
        <div className="mt-10 rounded-xl border p-6">
          {knowledgeBases.error instanceof ApiError &&
          knowledgeBases.error.status === 401 ? (
            <>
              <p className="text-sm" role="alert">
                登录已过期，请重新登录。
              </p>
              <Button
                className="mt-4"
                variant="outline"
                onClick={() => void forgetSession()}
              >
                重新登录
              </Button>
            </>
          ) : (
            <>
              <p className="text-sm" role="alert">
                无法加载知识库。
              </p>
              <Button
                className="mt-4"
                variant="outline"
                onClick={() => void knowledgeBases.refetch()}
              >
                重试
              </Button>
            </>
          )}
        </div>
      ) : knowledgeBases.data.length === 0 ? (
        <div className="mt-10 rounded-xl border border-dashed p-8">
          <h2 className="font-medium">暂无知识库</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            配置嵌入模型后可创建知识库。
          </p>
        </div>
      ) : (
        <ul className="mt-10 grid gap-4 sm:grid-cols-2">
          {knowledgeBases.data.map((item) => (
            <li className="rounded-xl border p-5" key={item.id}>
              <div className="flex items-start justify-between gap-3">
                <h2 className="min-w-0 break-words font-medium">
                  <Link
                    className="underline-offset-4 hover:underline focus-visible:underline"
                    to="/kb/$kbId/documents"
                    params={{ kbId: String(item.id) }}
                    search={{ cursor: 0 }}
                  >
                    {item.name}
                  </Link>
                </h2>
                <span className="shrink-0 whitespace-nowrap rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
                  {item.status === "archived" ? "已归档" : "使用中"}
                </span>
              </div>
              <p className="mt-5 text-xs text-muted-foreground">
                编号 {item.id} · {roleLabels[item.role]}
              </p>
              {item.status === "active" && (
                <Link
                  className="mt-3 inline-block text-sm underline"
                  to="/kb/$kbId/answer"
                  params={{ kbId: String(item.id) }}
                >
                  提问
                </Link>
              )}
            </li>
          ))}
        </ul>
      )}
      <ChatModels identity={identity} />
      <Setup identity={identity} />
    </section>
  );
}

export function App() {
  const queryClient = useQueryClient();
  const readiness = useQuery({
    queryKey: ["readiness"],
    queryFn: ({ signal }) => getReadiness(signal),
    retry: false,
  });
  const currentIdentity = useQuery({
    queryKey: ["identity"],
    queryFn: async ({ signal }): Promise<Identity | null> => {
      try {
        return await getIdentity(signal);
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) return null;
        throw error;
      }
    },
    retry: false,
  });
  const status = readiness.isPending
    ? "正在检查"
    : readiness.isError
      ? "服务不可用"
      : "服务已就绪";
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl flex-col px-6 py-8 sm:px-10">
      <header className="flex items-center justify-between border-b pb-6">
        <span className="text-lg font-semibold tracking-tight">TuskRAG</span>
        <span className="text-sm text-muted-foreground" role="status">
          {status}
        </span>
      </header>
      {readiness.isError && (
        <div className="mt-5 flex items-center gap-3 text-sm text-muted-foreground">
          <span>基础服务暂不可用。</span>
          <Button
            size="sm"
            variant="outline"
            onClick={() => void readiness.refetch()}
          >
            重新检查
          </Button>
        </div>
      )}
      {currentIdentity.isPending ? (
        <p className="py-16 text-sm text-muted-foreground" role="status">
          正在加载
        </p>
      ) : currentIdentity.data ? (
        <IdentityContext.Provider value={currentIdentity.data}>
          <Outlet />
        </IdentityContext.Provider>
      ) : currentIdentity.data === null ? (
        <LoginForm
          onLogin={(value) => queryClient.setQueryData(["identity"], value)}
        />
      ) : (
        <section className="py-16">
          <p role="alert">暂时无法连接，请稍后重试。</p>
          <Button
            className="mt-4"
            variant="outline"
            onClick={() => void currentIdentity.refetch()}
          >
            重试
          </Button>
        </section>
      )}
    </main>
  );
}
