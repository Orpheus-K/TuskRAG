import {
  type QueryClient,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
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
import { Dialog } from "@tuskrag/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@tuskrag/ui/dropdown-menu";
import { EmptyState } from "@tuskrag/ui/empty-state";
import { Field } from "@tuskrag/ui/field";
import { IdeShell, IdeSidebarGroup } from "@tuskrag/ui/ide-shell";
import { Input } from "@tuskrag/ui/input";
import { SectionHeader } from "@tuskrag/ui/section-header";
import { WorkspaceSplit } from "@tuskrag/ui/workspace-split";
import {
  ChevronsUpDown,
  CircleUser,
  FileText,
  FlaskConical,
  KeyRound,
  LibraryBig,
  LogOut,
  type LucideIcon,
  MessagesSquare,
  Plus,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import { createContext, type FormEvent, useEffect, useState } from "react";
import { ChatModels } from "./ChatModels";
import { CreateKnowledgeBase, EmbeddingModels } from "./Setup";

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

async function forgetSession(queryClient: QueryClient) {
  await queryClient.cancelQueries({ queryKey: ["identity"] });
  queryClient.removeQueries({ queryKey: ["knowledge-bases"] });
  queryClient.removeQueries({ queryKey: ["knowledge-base"] });
  queryClient.removeQueries({ queryKey: ["documents"] });
  queryClient.removeQueries({ queryKey: ["search"] });
  queryClient.removeQueries({ queryKey: ["embedding-spaces"] });
  queryClient.setQueryData(["identity"], null);
}

function SidebarNavItem({
  to,
  params,
  search,
  icon: Icon,
  label,
  active,
}: {
  to: string;
  params?: Record<string, string>;
  search?: Record<string, unknown>;
  icon: LucideIcon;
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
      aria-label={label}
      aria-current={active ? "page" : undefined}
      params={params as never}
      search={search as never}
      title={label}
      to={to as never}
    >
      <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
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
      <h1 className="mt-2 text-2xl font-semibold tracking-tight">
        登录 TuskRAG
      </h1>
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
              <Input autoComplete="username" id="login" name="login" required />
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
  const [creating, setCreating] = useState(false);
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

  const kbListContent = (
    <div className="space-y-5">
      <SectionHeader
        actions={
          <Dialog
            onOpenChange={setCreating}
            open={creating}
            title="新建知识库"
            trigger={
              <Button size="sm">
                <Plus aria-hidden="true" className="h-4 w-4" />
                新建知识库
              </Button>
            }
          >
            <CreateKnowledgeBase
              identity={identity}
              onCreated={() => setCreating(false)}
            />
          </Dialog>
        }
        title="知识库"
      />
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
                  onClick={() => void forgetSession(queryClient)}
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
        <ul className="grid gap-4 [grid-template-columns:repeat(auto-fit,minmax(min(100%,16rem),1fr))]">
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
      <EmbeddingModels identity={identity} />
    </div>
  );

  return (
    <WorkspaceSplit
      className="lg:min-h-[calc(100dvh-5.5rem)]"
      panes={[
        {
          id: "kb-main",
          title: "知识库工作区",
          content: kbListContent,
        },
        {
          id: "kb-models",
          title: "模型配置",
          defaultWidth: 320,
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
  const [loggingOut, setLoggingOut] = useState(false);
  const [logoutError, setLogoutError] = useState("");

  async function signOut(identity: Identity) {
    setLoggingOut(true);
    setLogoutError("");
    try {
      await logout(identity.csrf_token);
      await forgetSession(queryClient);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 401) {
        await forgetSession(queryClient);
      } else {
        setLogoutError(messageFor(cause));
      }
    } finally {
      setLoggingOut(false);
    }
  }

  const unavailableBadge = readiness.isError ? (
    <Badge role="status" variant="danger">
      服务不可用
    </Badge>
  ) : null;

  if (currentIdentity.data) {
    const identity = currentIdentity.data;
    const globalNav = [
      { to: "/", icon: LibraryBig, label: "知识库", active: pathname === "/" },
      {
        to: "/admin",
        icon: ShieldCheck,
        label: "平台治理",
        active: pathname === "/admin",
      },
      {
        to: "/api-keys",
        icon: KeyRound,
        label: "API Key",
        active: pathname === "/api-keys",
      },
    ];
    const kbNav = activeKbId
      ? [
          {
            to: "/kb/$kbId/documents",
            search: { cursor: 0 },
            icon: FileText,
            label: "文档与检索",
            active: pathname.endsWith("/documents"),
          },
          {
            to: "/kb/$kbId/answer",
            icon: MessagesSquare,
            label: "知识问答",
            active: pathname.endsWith("/answer"),
          },
          {
            to: "/kb/$kbId/eval",
            search: { cursor: "", digest: "", row: 0 },
            icon: FlaskConical,
            label: "评测中心",
            active:
              pathname.includes("/eval") ||
              pathname.includes("/answer-evaluation"),
          },
          {
            to: "/kb/$kbId/rebuild",
            search: { job: 0 },
            icon: RefreshCw,
            label: "嵌入重建",
            active: pathname.endsWith("/rebuild"),
          },
        ]
      : [];
    const currentPage = [...globalNav, ...kbNav].find((item) => item.active);
    return (
      <IdentityContext.Provider value={identity}>
        <IdeShell
          brand={
            <Link
              aria-label="TuskRAG"
              className="flex items-center gap-2 text-sm font-semibold tracking-tight"
              to="/"
            >
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-primary text-xs font-bold text-primary-foreground">
                <LibraryBig aria-hidden="true" className="size-4" />
              </span>
              <span className="tusk-sidebar-label">TuskRAG</span>
            </Link>
          }
          sidebarFooter={
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  aria-label="账号菜单"
                  className="tusk-sidebar-item h-auto w-full justify-start gap-2.5 px-2.5 py-1.5"
                  title="账号菜单"
                  variant="ghost"
                >
                  <CircleUser aria-hidden="true" className="h-5 w-5 shrink-0" />
                  <span className="tusk-sidebar-label min-w-0 flex-1 text-left">
                    <span className="block truncate text-xs font-medium">
                      用户 #{identity.user_id}
                    </span>
                    <span className="block truncate text-[11px] font-normal text-muted-foreground">
                      租户 {identity.tenant_id}
                    </span>
                  </span>
                  <ChevronsUpDown
                    aria-hidden="true"
                    className="tusk-sidebar-label h-3.5 w-3.5 shrink-0 text-muted-foreground"
                  />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" side="top">
                <DropdownMenuLabel>
                  用户 #{identity.user_id} · 租户 {identity.tenant_id}
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  disabled={loggingOut}
                  onSelect={() => void signOut(identity)}
                >
                  <LogOut aria-hidden="true" className="h-4 w-4" />
                  退出登录
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          }
          sidebarNav={
            <div className="space-y-3">
              <IdeSidebarGroup title="工作台">
                {globalNav.map((item) => (
                  <SidebarNavItem key={item.to} {...item} />
                ))}
              </IdeSidebarGroup>
              {activeKbId ? (
                <IdeSidebarGroup title={`知识库 #${activeKbId}`}>
                  {kbNav.map((item) => (
                    <SidebarNavItem
                      key={item.to}
                      params={{ kbId: activeKbId }}
                      {...item}
                    />
                  ))}
                </IdeSidebarGroup>
              ) : null}
            </div>
          }
          topbarLeft={
            <span className="truncate text-xs text-muted-foreground">
              {[activeKbId ? `知识库 #${activeKbId}` : null, currentPage?.label]
                .filter(Boolean)
                .join(" / ")}
            </span>
          }
          topbarRight={unavailableBadge}
        >
          {readiness.isError && (
            <Alert
              className="mb-4 flex items-center justify-between gap-3"
              variant="warning"
            >
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
          {logoutError && (
            <Alert className="mb-4" variant="danger">
              {logoutError}
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
        {unavailableBadge}
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
