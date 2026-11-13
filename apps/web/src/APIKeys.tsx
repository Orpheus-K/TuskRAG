import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  createAPIKey,
  type Identity,
  listAPIKeys,
  listKnowledgeBases,
  revokeAPIKey,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { type FormEvent, useState } from "react";

const allActions = [
  { id: "search", label: "检索 (search)" },
  { id: "read", label: "读原文 (read)" },
  { id: "answer", label: "问答 (answer)" },
  { id: "mcp", label: "MCP (mcp)" },
  { id: "ingest", label: "入库/删除 (ingest)" },
];

export function APIKeysPage({ identity }: { identity: Identity }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [actions, setActions] = useState<string[]>(["search", "read", "answer"]);
  const [selectedKBs, setSelectedKBs] = useState<number[]>([]);
  const [rpm, setRpm] = useState("60");
  const [quota, setQuota] = useState("0");
  const [ipAllowlist, setIpAllowlist] = useState("");
  const [createdSecret, setCreatedSecret] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const keysQueryKey = ["api-keys", identity.tenant_id, identity.user_id];
  const keys = useQuery({
    queryKey: keysQueryKey,
    queryFn: ({ signal }) => listAPIKeys(signal),
    retry: false,
  });

  const kbs = useQuery({
    queryKey: ["knowledge-bases", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listKnowledgeBases(signal),
    retry: false,
  });

  const create = useMutation({
    mutationFn: () =>
      createAPIKey(
        {
          name: name.trim(),
          actions,
          kb_ids: selectedKBs,
          rate_limit_rpm: Number(rpm) || 60,
          monthly_token_quota: Number(quota) || 0,
          ip_allowlist: ipAllowlist
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean),
        },
        identity.csrf_token,
      ),
    onSuccess: async (res) => {
      setError(null);
      setCreatedSecret(res.plaintext);
      setName("");
      await queryClient.invalidateQueries({ queryKey: keysQueryKey });
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 400) {
        setError("API Key 参数无效，请检查名称、动作范围或 IP 白名单。");
        return;
      }
      setError("创建 API Key 失败，请重试。");
    },
  });

  const revoke = useMutation({
    mutationFn: (keyId: number) => revokeAPIKey(keyId, identity.csrf_token),
    onSuccess: async () => {
      setError(null);
      await queryClient.invalidateQueries({ queryKey: keysQueryKey });
    },
    onError: () => setError("吊销 API Key 失败，请重试。"),
  });

  function toggleAction(id: string) {
    setActions((prev) =>
      prev.includes(id) ? prev.filter((a) => a !== id) : [...prev, id],
    );
  }

  function toggleKB(id: number) {
    setSelectedKBs((prev) =>
      prev.includes(id) ? prev.filter((k) => k !== id) : [...prev, id],
    );
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!name.trim() || actions.length === 0) {
      setError("请填写名称并至少选择一个动作权限。");
      return;
    }
    create.mutate();
  }

  return (
    <section className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">API Key 管理</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            为外部 Agent 与系统集成签发范围受限的访问密钥，创建后仅显示一次明文。
          </p>
        </div>
        <Link className="text-sm underline" to="/">
          返回工作区
        </Link>
      </div>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      {createdSecret && (
        <div
          role="status"
          className="rounded-md border border-emerald-500/40 bg-emerald-50/40 p-4 text-sm"
        >
          <p className="font-medium">新 API Key 已生成（请立即复制保存）：</p>
          <code className="mt-2 block break-all rounded bg-background px-3 py-2 font-mono text-xs">
            {createdSecret}
          </code>
        </div>
      )}

      <form
        onSubmit={handleSubmit}
        className="space-y-4 rounded-xl border p-5 text-sm"
      >
        <h2 className="font-semibold">签发新密钥</h2>
        <div className="grid gap-4 sm:grid-cols-3">
          <label className="block">
            密钥名称
            <input
              aria-label="密钥名称"
              className="mt-1 block w-full rounded-md border bg-background px-3 py-2"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如：agent-prod"
            />
          </label>
          <label className="block">
            每分钟限速 (RPM)
            <input
              aria-label="每分钟限速"
              type="number"
              min={1}
              className="mt-1 block w-full rounded-md border bg-background px-3 py-2"
              value={rpm}
              onChange={(e) => setRpm(e.target.value)}
            />
          </label>
          <label className="block">
            月度 Token 配额（0 不限）
            <input
              aria-label="月度 Token 配额"
              type="number"
              min={0}
              className="mt-1 block w-full rounded-md border bg-background px-3 py-2"
              value={quota}
              onChange={(e) => setQuota(e.target.value)}
            />
          </label>
        </div>

        <label className="block">
          IP 白名单（可选，逗号分隔 IP 或 CIDR）
          <input
            aria-label="IP 白名单"
            className="mt-1 block w-full rounded-md border bg-background px-3 py-2"
            value={ipAllowlist}
            onChange={(e) => setIpAllowlist(e.target.value)}
            placeholder="例如：10.0.0.0/8, 192.168.1.10"
          />
        </label>

        <div>
          <p className="font-medium">允许的动作范围</p>
          <div className="mt-2 flex flex-wrap gap-3">
            {allActions.map((act) => (
              <label key={act.id} className="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={actions.includes(act.id)}
                  onChange={() => toggleAction(act.id)}
                />
                <span>{act.label}</span>
              </label>
            ))}
          </div>
        </div>

        {kbs.data && kbs.data.length > 0 && (
          <div>
            <p className="font-medium">限定知识库（不选表示本人有权访问的全部知识库）</p>
            <div className="mt-2 flex flex-wrap gap-3">
              {kbs.data.map((kb) => (
                <label key={kb.id} className="flex items-center gap-1.5">
                  <input
                    type="checkbox"
                    checked={selectedKBs.includes(kb.id)}
                    onChange={() => toggleKB(kb.id)}
                  />
                  <span>{kb.name}</span>
                </label>
              ))}
            </div>
          </div>
        )}

        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? "生成中..." : "生成 API Key"}
        </Button>
      </form>

      <div className="space-y-3">
        <h2 className="text-base font-semibold">已有密钥</h2>
        {keys.isPending && (
          <p className="text-sm text-muted-foreground">正在加载...</p>
        )}
        {keys.data && keys.data.length === 0 && (
          <p className="text-sm text-muted-foreground">暂无 API Key。</p>
        )}
        {keys.data && keys.data.length > 0 && (
          <ul className="divide-y rounded-xl border">
            {keys.data.map((k) => (
              <li
                key={k.id}
                className="flex flex-wrap items-center justify-between gap-3 px-4 py-3 text-sm"
              >
                <div className="space-y-1">
                  <p className="font-medium">
                    {k.name}{" "}
                    <span className="font-mono text-xs text-muted-foreground">
                      tsk_{k.prefix}_...
                    </span>
                    {k.revoked_at && (
                      <span className="ml-2 text-xs text-destructive">
                        已吊销
                      </span>
                    )}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    动作：{k.actions.join(", ")} · 限速：{k.rate_limit_rpm} RPM ·
                    本月用量：{k.month_tokens_used}
                    {k.monthly_token_quota > 0
                      ? ` / ${k.monthly_token_quota} tokens`
                      : " tokens（不限额）"}
                  </p>
                </div>
                {!k.revoked_at && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={revoke.isPending}
                    onClick={() => revoke.mutate(k.id)}
                  >
                    吊销
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
