import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
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
import { Alert } from "@tuskrag/ui/alert";
import { Badge } from "@tuskrag/ui/badge";
import { Button } from "@tuskrag/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@tuskrag/ui/card";
import { EmptyState } from "@tuskrag/ui/empty-state";
import { Field } from "@tuskrag/ui/field";
import { IdeShell, IdeSidebarGroup } from "@tuskrag/ui/ide-shell";
import { Input } from "@tuskrag/ui/input";
import { SectionHeader } from "@tuskrag/ui/section-header";
import { WorkspaceSplit } from "@tuskrag/ui/workspace-split";
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

function SidebarNavItem({
  to,
  params,
  search,
  icon,
  label,
  active,
}: {
  to: string;
  params?: Record<string, string>;
  search?: Record<string, unknown>;
  icon: string;
  label: string;
  active?: boolean;
}) {
  return (
    <Link
      className={`tusk-sidebar-item flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors ${
        active
          ? "bg-accent font-medium text-accent-foreground"
          : "text-muted-foreground hover:bg-muted hover:text-foreground"
      }`}
      params={params as never}
      search={search as never}
      title={label}
      to={to as never}
    >
      <span
        aria-hidden="true"
        className="flex h-5 w-5 shrink-0 items-center justify-center rounded border border-border/70 bg-surface text-[11px] font-mono"
      >
        {icon}
      </span>
      <span className="tusk-sidebar-label truncate">{label}</span>
    </Link>
  );
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

  return (
    <section className="mx-auto w-full max-w-md py-12 sm:py-20">
      <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        企业知识空间
      </p>
      <h1 className="mt-2 text-2xl font-semibold tracking-tight">
        登录 TuskRAG
      </h1>
      <p className="mt-2 text-sm leading-6 text-muted-foreground">
        使用管理员提供的租户编号和账号登录。
      </p>
      <Card className="mt-6">
        <CardContent className="pt-6">
          <form className="space-y-4" onSubmit={submit}>
            <Field htmlFor="tenant" label="租户编号">
              <Input
                autoComplete="off"
                id="tenant"
                inputMode="numeric"
                min="1"
                name="tenant"
                onChange={(e) => setTenantInput(e.target.value)}
                required
                step="1"
                type="number"
                value={tenantInput}
              />
            </Field>
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
                      disabled={busy}
                      key={p.id}
                      onClick={() => void onSSO(p.id)}
                      type="button"
                      variant="outline"
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
            <Field htmlFor="login" label="账号">
              <Input
                autoComplete="username"
                id="login"
                name="login"
                required
              />
            </Field>
            <Field htmlFor="password" label="密码">
              <Input
                autoComplete="current-password"
                id="password"
                name="password"
                required
                type="password"
              />
            </Field>
            {error && <Alert variant="danger">{error}</Alert>}
            <Button className="w-full" disabled={busy} type="submit">
              {busy ? "正在登录" : "登录"}
            </Button>
          </form>
        </CardContent>
      </Card>
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

  const kbListContent = (
    <div className="space-y-5">
      <SectionHeader
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button asChild size="sm" variant="outline">
              <Link to="/admin">平台治理</Link>
            </Button>
            <Button asChild size="sm" variant="outline">
              <Link to="/api-keys">API Key 管理</Link>
            </Button>
            <Button
              disabled={loggingOut}
              onClick={() => void signOut()}
              size="sm"
              variant="outline"
            >
              {loggingOut ? "正在退出" : "退出登录"}
            </Button>
          </div>
        }
        description="查看你有权访问的知识库，点击进入文档管理或直接提问。"
        eyebrow={`租户 ${identity.tenant_id}`}
        title="知识库"
      />
      {error && <Alert variant="danger">{error}</Alert>}
      {knowledgeBases.isPending ? (
        <Alert variant="info">正在加载知识库</Alert>
      ) : knowledgeBases.isError ? (
        <Card>
          <CardContent className="pt-5">
            {knowledgeBases.error instanceof ApiError &&
            knowledgeBases.error.status === 401 ? (
              <>
                <p className="text-sm" role="alert">
                  登录已过期，请重新登录。
                </p>
                <Button
                  className="mt-4"
                  onClick={() => void forgetSession()}
                  variant="outline"
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
                  onClick={() => void knowledgeBases.refetch()}
                  variant="outline"
                >
                  重试
                </Button>
              </>
            )}
          </CardContent>
        </Card>
      ) : knowledgeBases.data.length === 0 ? (
        <EmptyState
          description="配置嵌入模型后可创建知识库。"
          title="暂无知识库"
        />
      ) : (
        <ul className="grid gap-4 sm:grid-cols-2">
          {knowledgeBases.data.map((item) => (
            <li key={item.id}>
              <Card className="h-full transition-colors hover:border-foreground/25">
                <CardHeader className="pb-2">
                  <div className="flex items-start justify-between gap-3">
                    <CardTitle className="min-w-0 break-words text-base">
                      <Link
                        className="underline-offset-4 hover:underline focus-visible:underline"
                        params={{ kbId: String(item.id) }}
                        search={{ cursor: 0 }}
                        to="/kb/$kbId/documents"
                      >
                        {item.name}
                      </Link>
                    </CardTitle>
                    <Badge
                      variant={
                        item.status === "archived" ? "neutral" : "success"
                      }
                    >
                      {item.status === "archived" ? "已归档" : "使用中"}
                    </Badge>
                  </div>
                  <CardDescription>
                    编号 {item.id} · {roleLabels[item.role]}
                  </CardDescription>
                </CardHeader>
                <CardContent className="flex items-center gap-3 pt-2">
                  <Button asChild size="xs" variant="outline">
                    <Link
                      params={{ kbId: String(item.id) }}
                      search={{ cursor: 0 }}
                      to="/kb/$kbId/documents"
                    >
                      文档管理
                    </Link>
                  </Button>
                  {item.status === "active" && (
                    <Link
                      className="text-xs font-medium underline underline-offset-4"
                      params={{ kbId: String(item.id) }}
                      to="/kb/$kbId/answer"
                    >
                      提问
                    </Link>
                  )}
                </CardContent>
              </Card>
            </li>
          ))}
        </ul>
      )}
    </div>
  );

  const configPanelContent = (
    <div className="space-y-4">
      <ChatModels identity={identity} />
      <Setup identity={identity} />
    </div>
  );

  return (
    <WorkspaceSplit
      panes={[
        {
          id: "kb-main",
          title: "知识库工作区",
          content: kbListContent,
        },
        {
          id: "kb-setup",
          title: "模型与创建",
          defaultWidth: 420,
          minWidth: 300,
          maxWidth: 640,
          collapsible: true,
          content: configPanelContent,
        },
      ]}
      storageKey={`tuskrag.workspace.${identity.tenant_id}`}
    />
  );
}

export function App() {
  const queryClient = useQueryClient();
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  });
  const kbMatch = /^\/kb\/([1-9]\d*)(?:\/|$)/.exec(pathname);
  const activeKbId = kbMatch ? kbMatch[1] : null;

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

  const readinessBadge = (
    <Badge
      role="status"
      variant={
        readiness.isPending
          ? "neutral"
          : readiness.isError
            ? "danger"
            : "success"
      }
    >
      {status}
    </Badge>
  );

  if (currentIdentity.data) {
    const identity = currentIdentity.data;
    return (
      <IdentityContext.Provider value={identity}>
        <IdeShell
          brand={
            <Link
              className="flex items-center gap-2 text-sm font-semibold tracking-tight"
              to="/"
            >
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-primary text-xs font-bold text-primary-foreground">
                T
              </span>
              <span className="tusk-sidebar-label">TuskRAG</span>
            </Link>
          }
          sidebarFooter={
            <div className="space-y-0.5 px-1">
              <p className="tusk-sidebar-label truncate text-xs font-medium text-foreground">
                用户 #{identity.user_id}
              </p>
              <p className="tusk-sidebar-label truncate text-[11px] text-muted-foreground">
                租户 {identity.tenant_id}
              </p>
            </div>
          }
          sidebarNav={
            <div className="space-y-3">
              <IdeSidebarGroup title="工作台">
                <SidebarNavItem
                  active={pathname === "/"}
                  icon="KB"
                  label="知识库总览"
                  to="/"
                />
                <SidebarNavItem
                  active={pathname === "/admin"}
                  icon="AD"
                  label="平台治理"
                  to="/admin"
                />
                <SidebarNavItem
                  active={pathname === "/api-keys"}
                  icon="AK"
                  label="API Key 管理"
                  to="/api-keys"
                />
              </IdeSidebarGroup>

              {activeKbId ? (
                <IdeSidebarGroup title={`知识库 #${activeKbId}`}>
                  <SidebarNavItem
                    active={pathname.endsWith("/documents")}
                    icon="DOC"
                    label="文档与入库"
                    params={{ kbId: activeKbId }}
                    search={{ cursor: 0 }}
                    to="/kb/$kbId/documents"
                  />
                  <SidebarNavItem
                    active={pathname.endsWith("/answer")}
                    icon="QA"
                    label="知识问答"
                    params={{ kbId: activeKbId }}
                    to="/kb/$kbId/answer"
                  />
                  <SidebarNavItem
                    active={pathname.endsWith("/config")}
                    icon="CFG"
                    label="配置与重建"
                    params={{ kbId: activeKbId }}
                    to="/kb/$kbId/config"
                  />
                  <SidebarNavItem
                    active={pathname.endsWith("/members")}
                    icon="USR"
                    label="成员权限"
                    params={{ kbId: activeKbId }}
                    to="/kb/$kbId/members"
                  />
                  <SidebarNavItem
                    active={pathname.includes("/eval")}
                    icon="EVL"
                    label="评测中心"
                    params={{ kbId: activeKbId }}
                    search={{ cursor: "", digest: "", row: 0 }}
                    to="/kb/$kbId/eval"
                  />
                  <SidebarNavItem
                    active={pathname.endsWith("/trace")}
                    icon="TRC"
                    label="检索追踪"
                    params={{ kbId: activeKbId }}
                    to="/kb/$kbId/trace"
                  />
                </IdeSidebarGroup>
              ) : null}
            </div>
          }
          topbarLeft={
            <span className="truncate text-xs text-muted-foreground">
              {activeKbId ? `知识库 #${activeKbId}` : "控制台"}
            </span>
          }
          topbarRight={
            <div className="flex items-center gap-2">
              {readiness.isError ? (
                <Button
                  onClick={() => void readiness.refetch()}
                  size="xs"
                  variant="outline"
                >
                  重新检查
                </Button>
              ) : null}
              {readinessBadge}
            </div>
          }
        >
          {readiness.isError && (
            <Alert className="mb-4 flex items-center justify-between gap-3" variant="warning">
              <span>基础服务暂不可用。</span>
              <Button
                onClick={() => void readiness.refetch()}
                size="xs"
                variant="outline"
              >
                重新检查
              </Button>
            </Alert>
          )}
          <Outlet />
        </IdeShell>
      </IdentityContext.Provider>
    );
  }

  return (
    <main className="mx-auto flex min-h-screen max-w-5xl flex-col px-6 py-8 sm:px-10">
      <header className="flex items-center justify-between border-b pb-6">
        <span className="text-lg font-semibold tracking-tight">TuskRAG</span>
        {readinessBadge}
      </header>
      {readiness.isError && (
        <div className="mt-5 flex items-center gap-3 text-sm text-muted-foreground">
          <span>基础服务暂不可用。</span>
          <Button
            onClick={() => void readiness.refetch()}
            size="sm"
            variant="outline"
          >
            重新检查
          </Button>
        </div>
      )}
      {currentIdentity.isPending ? (
        <p className="py-16 text-sm text-muted-foreground" role="status">
          正在加载
        </p>
      ) : currentIdentity.data === null ? (
        <LoginForm
          onLogin={(value) => queryClient.setQueryData(["identity"], value)}
        />
      ) : (
        <section className="py-16">
          <p role="alert">暂时无法连接，请稍后重试。</p>
          <Button
            className="mt-4"
            onClick={() => void currentIdentity.refetch()}
            variant="outline"
          >
            重试
          </Button>
        </section>
      )}
    </main>
  );
}
