import { Link } from "@tanstack/react-router";
import {
  ApiError,
  type AuditLogEntry,
  createModelProvider,
  createOIDCProvider,
  deleteModelProvider,
  deleteOIDCProvider,
  exportAuditLogs,
  getPublicOIDCProviders,
  getTenantEgressPolicy,
  type Identity,
  listAuditLogs,
  listModelProviders,
  listOIDCProviders,
  type ModelProvider,
  type OIDCProvider,
  type ProviderConnectivity,
  testModelProvider,
  updateAuthPolicy,
  updateModelProvider,
  updateTenantEgressPolicy,
} from "@tuskrag/api-client";
import { useCallback, useEffect, useState } from "react";

export function AdminGovernancePage({ identity }: { identity: Identity }) {
  const [tab, setTab] = useState<"audit" | "providers" | "egress" | "sso">(
    "audit",
  );
  const [error, setError] = useState("");
  const [forbidden, setForbidden] = useState(false);

  // Audit logs
  const [logs, setLogs] = useState<AuditLogEntry[]>([]);
  const [actionFilter, setActionFilter] = useState("");
  const [resultFilter, setResultFilter] = useState("");
  const [loadingLogs, setLoadingLogs] = useState(false);

  // Model providers
  const [providers, setProviders] = useState<ModelProvider[]>([]);
  const [providerName, setProviderName] = useState("");
  const [providerBaseURL, setProviderBaseURL] = useState("");
  const [providerLocality, setProviderLocality] = useState<
    "internal" | "external"
  >("internal");
  const [providerAPIKey, setProviderAPIKey] = useState("");
  const [editingProviderId, setEditingProviderId] = useState<number | null>(
    null,
  );
  const [probeResults, setProbeResults] = useState<
    Record<number, ProviderConnectivity>
  >({});

  // Tenant egress policy
  const [egressMax, setEgressMax] = useState<
    "deny" | "internal_only" | "allow"
  >("deny");
  const [egressSaved, setEgressSaved] = useState(false);

  // OIDC SSO
  const [oidcProviders, setOidcProviders] = useState<OIDCProvider[]>([]);
  const [disableLocalLogin, setDisableLocalLogin] = useState(false);
  const [oidcName, setOidcName] = useState("");
  const [oidcIssuer, setOidcIssuer] = useState("");
  const [oidcClientId, setOidcClientId] = useState("");
  const [oidcClientSecret, setOidcClientSecret] = useState("");
  const [oidcGroupClaim, setOidcGroupClaim] = useState("groups");

  const handleErr = useCallback((cause: unknown) => {
    if (cause instanceof ApiError && cause.status === 403) {
      setForbidden(true);
      setError("仅租户管理员可访问平台治理控制台。");
      return;
    }
    setError(cause instanceof Error ? cause.message : "操作失败");
  }, []);

  const loadLogs = useCallback(async () => {
    setLoadingLogs(true);
    setError("");
    const controller = new AbortController();
    try {
      const items = await listAuditLogs(controller.signal, {
        ...(actionFilter.trim() ? { action: actionFilter.trim() } : {}),
        ...(resultFilter ? { result: resultFilter } : {}),
        limit: 100,
      });
      setLogs(items);
    } catch (cause) {
      handleErr(cause);
    } finally {
      setLoadingLogs(false);
    }
  }, [actionFilter, resultFilter, handleErr]);

  const loadProviders = useCallback(async () => {
    setError("");
    const controller = new AbortController();
    try {
      setProviders(await listModelProviders(controller.signal));
    } catch (cause) {
      handleErr(cause);
    }
  }, [handleErr]);

  const loadEgress = useCallback(async () => {
    setError("");
    const controller = new AbortController();
    try {
      const data = await getTenantEgressPolicy(controller.signal);
      setEgressMax(data.egress_policy_max);
    } catch (cause) {
      handleErr(cause);
    }
  }, [handleErr]);

  const loadSSO = useCallback(async () => {
    setError("");
    const controller = new AbortController();
    try {
      const [items, pub] = await Promise.all([
        listOIDCProviders(controller.signal),
        getPublicOIDCProviders(controller.signal, identity.tenant_id),
      ]);
      setOidcProviders(items);
      setDisableLocalLogin(pub.disable_local_login);
    } catch (cause) {
      handleErr(cause);
    }
  }, [identity.tenant_id, handleErr]);

  useEffect(() => {
    if (tab === "audit") void loadLogs();
    if (tab === "providers") void loadProviders();
    if (tab === "egress") void loadEgress();
    if (tab === "sso") void loadSSO();
  }, [tab, loadLogs, loadProviders, loadEgress, loadSSO]);

  async function onExport(format: "jsonl" | "csv") {
    setError("");
    const controller = new AbortController();
    try {
      const text = await exportAuditLogs(
        controller.signal,
        {
          ...(actionFilter.trim() ? { action: actionFilter.trim() } : {}),
          ...(resultFilter ? { result: resultFilter } : {}),
        },
        format,
      );
      const blob = new Blob([text], {
        type:
          format === "csv"
            ? "text/csv;charset=utf-8"
            : "application/x-ndjson;charset=utf-8",
      });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `audit-logs.${format}`;
      a.click();
      URL.revokeObjectURL(url);
      void loadLogs();
    } catch (cause) {
      handleErr(cause);
    }
  }

  async function onSaveProvider(event: React.FormEvent) {
    event.preventDefault();
    setError("");
    try {
      const payload = {
        name: providerName.trim(),
        base_url: providerBaseURL.trim(),
        locality: providerLocality,
        ...(providerAPIKey.trim() ? { api_key: providerAPIKey.trim() } : {}),
      };
      if (editingProviderId) {
        await updateModelProvider(
          identity.csrf_token,
          editingProviderId,
          payload,
        );
      } else {
        await createModelProvider(identity.csrf_token, payload);
      }
      setProviderName("");
      setProviderBaseURL("");
      setProviderAPIKey("");
      setEditingProviderId(null);
      await loadProviders();
    } catch (cause) {
      handleErr(cause);
    }
  }

  async function onDeleteProvider(id: number) {
    setError("");
    try {
      await deleteModelProvider(identity.csrf_token, id);
      await loadProviders();
    } catch (cause) {
      handleErr(cause);
    }
  }

  async function onTestProvider(id: number) {
    setError("");
    try {
      const res = await testModelProvider(identity.csrf_token, id);
      setProbeResults((prev) => ({ ...prev, [id]: res }));
    } catch (cause) {
      handleErr(cause);
    }
  }

  async function onSaveEgress(event: React.FormEvent) {
    event.preventDefault();
    setError("");
    setEgressSaved(false);
    try {
      const updated = await updateTenantEgressPolicy(
        identity.csrf_token,
        egressMax,
      );
      setEgressMax(updated.egress_policy_max);
      setEgressSaved(true);
    } catch (cause) {
      handleErr(cause);
    }
  }

  return (
    <section className="py-10">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <p className="text-sm text-muted-foreground">
            租户 {identity.tenant_id}
          </p>
          <h1 className="mt-1 text-3xl font-semibold tracking-tight">
            平台治理中心
          </h1>
        </div>
        <Link
          className="inline-flex h-9 items-center justify-center rounded-md border border-border bg-background px-4 py-2 text-sm font-medium shadow-sm hover:bg-muted"
          to="/"
        >
          返回知识库
        </Link>
      </div>

      <div className="mt-6 flex gap-2 border-b border-border pb-3">
        <button
          type="button"
          className={`rounded-md px-3 py-1.5 text-sm font-medium ${tab === "audit" ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}
          onClick={() => setTab("audit")}
        >
          审计日志
        </button>
        <button
          type="button"
          className={`rounded-md px-3 py-1.5 text-sm font-medium ${tab === "providers" ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}
          onClick={() => setTab("providers")}
        >
          模型供应商
        </button>
        <button
          type="button"
          className={`rounded-md px-3 py-1.5 text-sm font-medium ${tab === "egress" ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}
          onClick={() => setTab("egress")}
        >
          租户出域上限
        </button>
        <button
          type="button"
          className={`rounded-md px-3 py-1.5 text-sm font-medium ${tab === "sso" ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}
          onClick={() => setTab("sso")}
        >
          身份源（OIDC）
        </button>
      </div>

      {error ? (
        <p className="mt-4 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-destructive">
          {error}
        </p>
      ) : null}

      {!forbidden && tab === "audit" ? (
        <div className="mt-6 space-y-4">
          <div className="flex flex-wrap items-end gap-3">
            <div>
              <label
                className="block text-xs font-medium text-muted-foreground"
                htmlFor="audit-action"
              >
                动作过滤
              </label>
              <input
                id="audit-action"
                className="mt-1 h-9 rounded-md border border-input bg-background px-3 text-sm"
                placeholder="例如 audit.export"
                value={actionFilter}
                onChange={(e) => setActionFilter(e.target.value)}
              />
            </div>
            <div>
              <label
                className="block text-xs font-medium text-muted-foreground"
                htmlFor="audit-result"
              >
                结果
              </label>
              <select
                id="audit-result"
                className="mt-1 h-9 rounded-md border border-input bg-background px-3 text-sm"
                value={resultFilter}
                onChange={(e) => setResultFilter(e.target.value)}
              >
                <option value="">全部</option>
                <option value="allow">allow</option>
                <option value="deny">deny</option>
                <option value="error">error</option>
              </select>
            </div>
            <button
              type="button"
              className="h-9 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground"
              onClick={() => void loadLogs()}
              disabled={loadingLogs}
            >
              查询
            </button>
            <button
              type="button"
              className="h-9 rounded-md border border-border px-3 text-sm"
              onClick={() => void onExport("jsonl")}
            >
              导出 JSONL
            </button>
            <button
              type="button"
              className="h-9 rounded-md border border-border px-3 text-sm"
              onClick={() => void onExport("csv")}
            >
              导出 CSV
            </button>
          </div>

          <div className="overflow-x-auto rounded-md border border-border">
            <table className="w-full text-left text-sm">
              <thead className="bg-muted/50 text-xs uppercase text-muted-foreground">
                <tr>
                  <th className="px-3 py-2">ID</th>
                  <th className="px-3 py-2">时间</th>
                  <th className="px-3 py-2">操作者</th>
                  <th className="px-3 py-2">动作</th>
                  <th className="px-3 py-2">对象</th>
                  <th className="px-3 py-2">结果</th>
                </tr>
              </thead>
              <tbody>
                {logs.length === 0 ? (
                  <tr>
                    <td
                      className="px-3 py-4 text-center text-muted-foreground"
                      colSpan={6}
                    >
                      暂无审计记录
                    </td>
                  </tr>
                ) : (
                  logs.map((entry) => (
                    <tr key={entry.id} className="border-t border-border">
                      <td className="px-3 py-2 font-mono text-xs">
                        {entry.id}
                      </td>
                      <td className="px-3 py-2 text-xs">{entry.created_at}</td>
                      <td className="px-3 py-2">
                        {entry.actor_name || `User #${entry.actor_user_id}`}
                      </td>
                      <td className="px-3 py-2 font-mono text-xs">
                        {entry.action}
                      </td>
                      <td className="px-3 py-2 font-mono text-xs">
                        {entry.object_type}#{entry.object_id}
                      </td>
                      <td className="px-3 py-2">{entry.result}</td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      ) : null}

      {!forbidden && tab === "providers" ? (
        <div className="mt-6 space-y-6">
          <form
            className="grid gap-3 rounded-md border border-border p-4 sm:grid-cols-2"
            onSubmit={(e) => void onSaveProvider(e)}
          >
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="供应商名称"
              value={providerName}
              onChange={(e) => setProviderName(e.target.value)}
              required
            />
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="Base URL (例如 https://api.example.com/v1)"
              value={providerBaseURL}
              onChange={(e) => setProviderBaseURL(e.target.value)}
              required
            />
            <select
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              value={providerLocality}
              onChange={(e) =>
                setProviderLocality(e.target.value as "internal" | "external")
              }
            >
              <option value="internal">internal</option>
              <option value="external">external</option>
            </select>
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              type="password"
              placeholder={
                editingProviderId
                  ? "新 API Key（留空保持不变）"
                  : "API Key（可选）"
              }
              value={providerAPIKey}
              onChange={(e) => setProviderAPIKey(e.target.value)}
            />
            <div className="flex gap-2 sm:col-span-2">
              <button
                type="submit"
                className="h-9 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground"
              >
                {editingProviderId ? "保存修改" : "添加供应商"}
              </button>
              {editingProviderId ? (
                <button
                  type="button"
                  className="h-9 rounded-md border border-border px-3 text-sm"
                  onClick={() => {
                    setEditingProviderId(null);
                    setProviderName("");
                    setProviderBaseURL("");
                    setProviderAPIKey("");
                  }}
                >
                  取消编辑
                </button>
              ) : null}
            </div>
          </form>

          <div className="space-y-3">
            {providers.map((p) => {
              const probe = probeResults[p.id];
              return (
                <div
                  key={p.id}
                  className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border p-4"
                >
                  <div>
                    <div className="font-medium">
                      {p.name}{" "}
                      <span className="ml-2 rounded bg-muted px-2 py-0.5 text-xs">
                        {p.locality}
                      </span>
                      {p.has_credential ? (
                        <span className="ml-2 rounded bg-emerald-500/15 px-2 py-0.5 text-xs text-emerald-700">
                          已配置密钥
                        </span>
                      ) : null}
                    </div>
                    <div className="mt-1 font-mono text-xs text-muted-foreground">
                      {p.base_url}
                    </div>
                    {probe ? (
                      <div
                        className={`mt-1 text-xs ${probe.reachable ? "text-emerald-700" : "text-destructive"}`}
                      >
                        连通性测试: {probe.message} ({probe.latency_ms} ms)
                      </div>
                    ) : null}
                  </div>
                  <div className="flex gap-2">
                    <button
                      type="button"
                      className="h-8 rounded-md border border-border px-3 text-xs"
                      onClick={() => void onTestProvider(p.id)}
                    >
                      测试连通性
                    </button>
                    <button
                      type="button"
                      className="h-8 rounded-md border border-border px-3 text-xs"
                      onClick={() => {
                        setEditingProviderId(p.id);
                        setProviderName(p.name);
                        setProviderBaseURL(p.base_url);
                        setProviderLocality(
                          p.locality as "internal" | "external",
                        );
                        setProviderAPIKey("");
                      }}
                    >
                      编辑
                    </button>
                    <button
                      type="button"
                      className="h-8 rounded-md border border-destructive/40 px-3 text-xs text-destructive"
                      onClick={() => void onDeleteProvider(p.id)}
                    >
                      删除
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ) : null}

      {!forbidden && tab === "egress" ? (
        <form
          className="mt-6 max-w-md space-y-4 rounded-md border border-border p-4"
          onSubmit={(e) => void onSaveEgress(e)}
        >
          <div>
            <label
              className="block text-sm font-medium"
              htmlFor="tenant-egress-max"
            >
              租户最高出域策略（egress_policy_max）
            </label>
            <select
              id="tenant-egress-max"
              className="mt-2 h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              value={egressMax}
              onChange={(e) =>
                setEgressMax(
                  e.target.value as "deny" | "internal_only" | "allow",
                )
              }
            >
              <option value="deny">deny（禁止任何模型调用）</option>
              <option value="internal_only">
                internal_only（仅允许内网模型）
              </option>
              <option value="allow">allow（允许内网与外部模型）</option>
            </select>
          </div>
          <button
            type="submit"
            className="h-9 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground"
          >
            保存租户出域上限
          </button>
          {egressSaved ? (
            <p className="text-xs text-emerald-700">已更新租户出域上限。</p>
          ) : null}
        </form>
      ) : null}

      {!forbidden && tab === "sso" ? (
        <div className="mt-6 space-y-6">
          <div className="flex items-center justify-between rounded-md border border-border p-4">
            <div>
              <div className="font-medium">禁用本地密码登录</div>
              <p className="text-xs text-muted-foreground">
                启用后仅允许通过已启用的 OIDC 身份源登录（需至少保留一个已启用的
                OIDC 提供方）。
              </p>
            </div>
            <button
              type="button"
              className="h-9 rounded-md border border-border px-4 text-sm font-medium"
              onClick={() => {
                void (async () => {
                  setError("");
                  try {
                    const res = await updateAuthPolicy(
                      identity.csrf_token,
                      !disableLocalLogin,
                    );
                    setDisableLocalLogin(res.disable_local_login);
                  } catch (cause) {
                    handleErr(cause);
                  }
                })();
              }}
            >
              {disableLocalLogin
                ? "已禁用本地登录（点击恢复）"
                : "已允许本地登录（点击禁用）"}
            </button>
          </div>

          <form
            className="grid gap-3 rounded-md border border-border p-4 sm:grid-cols-2"
            onSubmit={(e) => {
              e.preventDefault();
              void (async () => {
                setError("");
                try {
                  await createOIDCProvider(identity.csrf_token, {
                    name: oidcName.trim(),
                    issuer_url: oidcIssuer.trim(),
                    client_id: oidcClientId.trim(),
                    ...(oidcClientSecret.trim()
                      ? { client_secret: oidcClientSecret.trim() }
                      : {}),
                    group_claim: oidcGroupClaim.trim() || "groups",
                    enabled: true,
                  });
                  setOidcName("");
                  setOidcIssuer("");
                  setOidcClientId("");
                  setOidcClientSecret("");
                  await loadSSO();
                } catch (cause) {
                  handleErr(cause);
                }
              })();
            }}
          >
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="身份源名称（如 Corp SSO）"
              value={oidcName}
              onChange={(e) => setOidcName(e.target.value)}
              required
            />
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="Issuer URL（https://idp.example.com）"
              value={oidcIssuer}
              onChange={(e) => setOidcIssuer(e.target.value)}
              required
            />
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="Client ID"
              value={oidcClientId}
              onChange={(e) => setOidcClientId(e.target.value)}
              required
            />
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              type="password"
              placeholder="Client Secret（PKCE 公开客户端可留空）"
              value={oidcClientSecret}
              onChange={(e) => setOidcClientSecret(e.target.value)}
            />
            <input
              className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              placeholder="组声明字段（默认 groups）"
              value={oidcGroupClaim}
              onChange={(e) => setOidcGroupClaim(e.target.value)}
            />
            <button
              type="submit"
              className="h-9 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground"
            >
              添加 OIDC 身份源
            </button>
          </form>

          <div className="space-y-2">
            {oidcProviders.map((p) => (
              <div
                key={p.id}
                className="flex items-center justify-between rounded-md border border-border p-4"
              >
                <div>
                  <div className="font-medium">
                    {p.name}{" "}
                    <span className="ml-2 rounded bg-muted px-2 py-0.5 text-xs">
                      claim: {p.group_claim}
                    </span>
                  </div>
                  <div className="mt-1 font-mono text-xs text-muted-foreground">
                    {p.issuer_url} · client: {p.client_id}
                  </div>
                </div>
                <button
                  type="button"
                  className="h-8 rounded-md border border-destructive/40 px-3 text-xs text-destructive"
                  onClick={() => {
                    void (async () => {
                      setError("");
                      try {
                        await deleteOIDCProvider(identity.csrf_token, p.id);
                        await loadSSO();
                      } catch (cause) {
                        handleErr(cause);
                      }
                    })();
                  }}
                >
                  删除
                </button>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </section>
  );
}
