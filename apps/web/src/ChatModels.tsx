import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  createChatModel,
  type Identity,
  listChatModels,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { type FormEvent, useState } from "react";

export function ChatModels({ identity }: { identity: Identity }) {
  const client = useQueryClient();
  const models = useQuery({
    queryKey: ["chat-models", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listChatModels(signal),
    retry: false,
  });
  const [show, setShow] = useState(false),
    [saving, setSaving] = useState(false),
    [error, setError] = useState("");
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget,
      data = new FormData(form);
    setSaving(true);
    setError("");
    try {
      await createChatModel(
        {
          name: String(data.get("name")),
          base_url: String(data.get("base_url")),
          model: String(data.get("model")),
          locality:
            data.get("locality") === "external" ? "external" : "internal",
          api_key: String(data.get("api_key")),
        },
        identity.csrf_token,
      );
      form.reset();
      setShow(false);
      await client.invalidateQueries({ queryKey: ["chat-models"] });
    } catch (cause) {
      setError(
        cause instanceof ApiError && cause.status === 403
          ? "仅本地管理员可配置回答模型。"
          : cause instanceof ApiError && cause.code === "secret_unavailable"
            ? "服务器尚未配置密钥加密主密钥。"
            : "配置未保存，请检查名称、地址与服务器状态。",
      );
    } finally {
      const key = form.elements.namedItem("api_key");
      if (key instanceof HTMLInputElement) key.value = "";
      setSaving(false);
    }
  }
  return (
    <section className="mt-6 rounded-xl border p-5 sm:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-medium">回答模型</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            配置用于生成知识库回答的模型。
          </p>
        </div>
        <Button type="button" variant="outline" onClick={() => setShow(!show)}>
          {show ? "收起回答模型配置" : "配置回答模型"}
        </Button>
      </div>
      {models.isPending && (
        <p role="status" className="mt-4 text-sm">
          正在加载回答模型
        </p>
      )}
      {models.isError && (
        <p role="alert" className="mt-4 text-sm">
          回答模型加载失败。
          <button
            type="button"
            className="ml-2 underline"
            onClick={() => void models.refetch()}
          >
            重试
          </button>
        </p>
      )}
      {models.data?.length === 0 && (
        <p className="mt-4 text-sm text-muted-foreground">尚未配置回答模型。</p>
      )}
      {models.data && (
        <ul className="mt-4 space-y-2 text-sm">
          {models.data.map((model) => (
            <li key={model.id}>
              {model.name} · {model.model}
            </li>
          ))}
        </ul>
      )}
      {show && (
        <form
          onSubmit={(event) => void save(event)}
          className="mt-5 grid gap-4 sm:grid-cols-2"
        >
          {[
            ["name", "显示名称", "例如：知识助手"],
            ["base_url", "服务根地址", "https://model.example.com"],
            ["model", "模型名称", "服务提供的模型标识"],
            ["api_key", "API 密钥", "可选；仅发送至本服务器加密保存"],
          ].map(([name, label, placeholder]) => (
            <label key={name} className="space-y-2 text-sm">
              <span>{label}</span>
              <input
                className="h-10 w-full rounded-md border bg-background px-3"
                name={name}
                type={
                  name === "api_key"
                    ? "password"
                    : name === "base_url"
                      ? "url"
                      : "text"
                }
                placeholder={placeholder}
                required={name !== "api_key"}
                maxLength={
                  name === "api_key" ? 4096 : name === "base_url" ? 2048 : 200
                }
                autoComplete={name === "api_key" ? "new-password" : "off"}
                disabled={saving}
              />
            </label>
          ))}
          <label className="space-y-2 text-sm">
            <span>服务位置</span>
            <select
              name="locality"
              className="h-10 w-full rounded-md border bg-background px-3"
              defaultValue="external"
              disabled={saving}
            >
              <option value="external">组织外部（需要允许出域）</option>
              <option value="internal">组织内部</option>
            </select>
          </label>
          <div className="flex items-end">
            <Button type="submit" disabled={saving}>
              {saving ? "正在保存…" : "保存回答模型"}
            </Button>
          </div>
          {error && (
            <p role="alert" className="text-sm sm:col-span-2">
              {error}
            </p>
          )}
        </form>
      )}
    </section>
  );
}
