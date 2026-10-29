import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  createEmbeddingSpace,
  createKnowledgeBase,
  type EmbeddingSpace,
  type Identity,
  listEmbeddingSpaces,
  type NewKnowledgeBase,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { type FormEvent, useRef, useState } from "react";

const inputClass =
  "h-10 w-full rounded-md border bg-background px-3 text-sm outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring";
const labelClass = "block text-sm font-medium";

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) return "登录已过期，请重新登录。";
    if (error.status === 403) return "当前账号没有配置权限。";
    if (error.code === "secret_unavailable")
      return "服务器未配置密钥加密主密钥，无法保存 API 密钥。";
    if (error.code === "name_conflict") return "该名称已存在。";
    if (error.code === "idempotency_conflict")
      return "请求编号冲突，请修改输入后重试。";
    if (error.status === 400) return "配置无效，请检查输入。";
  }
  return "操作未完成，请检查服务后重试。";
}

function ModelForm({
  identity,
  onSaved,
}: {
  identity: Identity;
  onSaved: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const providerName = data.get("provider_name");
    const baseURL = data.get("base_url");
    const model = data.get("model");
    const locality = data.get("locality");
    const distance = data.get("distance");
    const apiKey = data.get("api_key");
    const dimensions = Number(data.get("dimensions"));
    if (
      typeof providerName !== "string" ||
      typeof baseURL !== "string" ||
      typeof model !== "string" ||
      (locality !== "internal" && locality !== "external") ||
      (distance !== "cosine" && distance !== "ip") ||
      typeof apiKey !== "string" ||
      !Number.isInteger(dimensions) ||
      dimensions < 64 ||
      dimensions > 4000
    ) {
      setError("请填写有效的模型配置。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await createEmbeddingSpace(
        {
          provider_name: providerName,
          base_url: baseURL,
          locality,
          model,
          dimensions,
          distance,
          normalized: data.get("normalized") === "on",
          ...(apiKey ? { api_key: apiKey } : {}),
        },
        identity.csrf_token,
      );
      form.reset();
      onSaved();
    } catch (cause) {
      setError(`${errorMessage(cause)} 如连接中断，请先查看已保存的配置。`);
      onSaved();
    } finally {
      const field = form.elements.namedItem("api_key");
      if (field instanceof HTMLInputElement) field.value = "";
      setBusy(false);
    }
  }

  return (
    <form className="mt-5 space-y-4" onSubmit={submit}>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <label className={labelClass} htmlFor="provider-name">
            提供方名称
          </label>
          <input
            className={inputClass}
            id="provider-name"
            name="provider_name"
            maxLength={200}
            required
          />
        </div>
        <div className="space-y-2">
          <label className={labelClass} htmlFor="provider-locality">
            网络位置
          </label>
          <select
            className={inputClass}
            id="provider-locality"
            name="locality"
            defaultValue="internal"
          >
            <option value="internal">内部网络</option>
            <option value="external">外部网络（HTTPS）</option>
          </select>
        </div>
        <div className="space-y-2 sm:col-span-2">
          <label className={labelClass} htmlFor="provider-url">
            提供方基础地址
          </label>
          <input
            className={inputClass}
            id="provider-url"
            name="base_url"
            type="url"
            placeholder="https://example.com"
            maxLength={2048}
            required
          />
        </div>
        <div className="space-y-2">
          <label className={labelClass} htmlFor="model-name">
            嵌入模型名称
          </label>
          <input
            className={inputClass}
            id="model-name"
            name="model"
            maxLength={200}
            required
          />
        </div>
        <div className="space-y-2">
          <label className={labelClass} htmlFor="model-dimensions">
            向量维度
          </label>
          <input
            className={inputClass}
            id="model-dimensions"
            name="dimensions"
            type="number"
            min="64"
            max="4000"
            step="1"
            required
          />
        </div>
        <div className="space-y-2">
          <label className={labelClass} htmlFor="model-distance">
            距离计算
          </label>
          <select
            className={inputClass}
            id="model-distance"
            name="distance"
            defaultValue="cosine"
          >
            <option value="cosine">余弦距离</option>
            <option value="ip">内积</option>
          </select>
        </div>
        <div className="space-y-2">
          <label className={labelClass} htmlFor="model-key">
            API 密钥（可选）
          </label>
          <input
            className={inputClass}
            id="model-key"
            name="api_key"
            type="password"
            autoComplete="off"
            maxLength={4096}
          />
        </div>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input name="normalized" type="checkbox" defaultChecked />
        向量归一化
      </label>
      <p className="text-xs leading-5 text-muted-foreground">
        保存配置不会验证模型连接。API 密钥需要服务器配置加密主密钥。
      </p>
      {error && (
        <p className="text-sm" role="alert">
          {error}
        </p>
      )}
      <Button type="submit" disabled={busy}>
        {busy ? "正在保存" : "保存模型配置"}
      </Button>
    </form>
  );
}

function KnowledgeBaseForm({
  identity,
  spaces,
  onSaved,
}: {
  identity: Identity;
  spaces: EmbeddingSpace[];
  onSaved: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const pending = useRef<{ payload: string; key: string } | null>(null);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const name = data.get("name");
    const spaceID = Number(data.get("embedding_space_id"));
    if (
      typeof name !== "string" ||
      !name.trim() ||
      !Number.isSafeInteger(spaceID) ||
      spaceID <= 0
    ) {
      setError("请填写名称并选择嵌入模型。");
      return;
    }
    const input: NewKnowledgeBase = {
      name: name.trim(),
      embedding_space_id: spaceID,
      egress_policy: "deny",
    };
    const payload = JSON.stringify(input);
    if (pending.current?.payload !== payload)
      pending.current = { payload, key: crypto.randomUUID() };
    setBusy(true);
    setError("");
    try {
      await createKnowledgeBase(
        input,
        identity.csrf_token,
        pending.current.key,
      );
      pending.current = null;
      form.reset();
      onSaved();
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="mt-5 space-y-4" onSubmit={submit}>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <label className={labelClass} htmlFor="kb-name">
            知识库名称
          </label>
          <input
            className={inputClass}
            id="kb-name"
            name="name"
            maxLength={200}
            required
          />
        </div>
        <div className="space-y-2">
          <label className={labelClass} htmlFor="kb-space">
            嵌入模型
          </label>
          <select
            className={inputClass}
            id="kb-space"
            name="embedding_space_id"
            defaultValue=""
            required
          >
            <option value="" disabled>
              选择嵌入模型
            </option>
            {spaces.map((space) => (
              <option key={space.id} value={space.id}>
                {space.provider_name} · {space.model} · {space.dimensions} 维
              </option>
            ))}
          </select>
        </div>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">
        新知识库默认仅允许内部语言、嵌入和重排模型；VLM
        不可用。出域策略管理尚未开放。
      </p>
      {error && (
        <p className="text-sm" role="alert">
          {error}
        </p>
      )}
      <Button type="submit" disabled={busy || spaces.length === 0}>
        {busy ? "正在创建" : "创建知识库"}
      </Button>
    </form>
  );
}

export function Setup({ identity }: { identity: Identity }) {
  const queryClient = useQueryClient();
  const [showModelForm, setShowModelForm] = useState(false);
  const spaces = useQuery({
    queryKey: ["embedding-spaces", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listEmbeddingSpaces(signal),
    retry: false,
  });
  function refreshModels() {
    void queryClient.invalidateQueries({ queryKey: ["embedding-spaces"] });
  }
  return (
    <div className="mt-8 grid gap-4">
      <section className="rounded-xl border p-5 sm:p-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="font-medium">嵌入模型</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              配置用于知识库的模型和向量空间。
            </p>
          </div>
          <Button
            type="button"
            variant="outline"
            onClick={() => setShowModelForm(!showModelForm)}
          >
            {showModelForm ? "收起配置" : "配置模型"}
          </Button>
        </div>
        {spaces.isPending ? (
          <p className="mt-4 text-sm text-muted-foreground" role="status">
            正在加载模型
          </p>
        ) : spaces.isError ? (
          <div className="mt-4">
            <p className="text-sm" role="alert">
              {errorMessage(spaces.error)}
            </p>
            <Button
              className="mt-3"
              variant="outline"
              onClick={() => void spaces.refetch()}
            >
              重试加载模型
            </Button>
          </div>
        ) : spaces.data.length === 0 ? (
          <p className="mt-4 text-sm text-muted-foreground">
            尚未配置嵌入模型。
          </p>
        ) : (
          <ul className="mt-4 space-y-2 text-sm">
            {spaces.data.map((space) => (
              <li key={space.id}>
                {space.provider_name} · {space.model} · {space.dimensions} 维
                {space.has_credential ? " · 已保存密钥" : ""}
              </li>
            ))}
          </ul>
        )}
        {showModelForm && (
          <ModelForm identity={identity} onSaved={refreshModels} />
        )}
      </section>
      <section className="rounded-xl border p-5 sm:p-6">
        <h2 className="font-medium">新建知识库</h2>
        {spaces.data ? (
          <KnowledgeBaseForm
            identity={identity}
            spaces={spaces.data}
            onSaved={() =>
              void queryClient.invalidateQueries({
                queryKey: ["knowledge-bases"],
              })
            }
          />
        ) : (
          <p className="mt-4 text-sm text-muted-foreground">
            加载模型配置后可创建知识库。
          </p>
        )}
      </section>
    </div>
  );
}
