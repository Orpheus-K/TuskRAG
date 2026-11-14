import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  createChatModel,
  type Identity,
  listChatModels,
} from "@tuskrag/api-client";
import { Alert } from "@tuskrag/ui/alert";
import { Button } from "@tuskrag/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@tuskrag/ui/card";
import { Field } from "@tuskrag/ui/field";
import { Input } from "@tuskrag/ui/input";
import { Select } from "@tuskrag/ui/select";
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
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between space-y-0 gap-3">
        <div>
          <CardTitle>回答模型</CardTitle>
          <CardDescription>配置用于生成知识库回答的模型。</CardDescription>
        </div>
        <Button
          onClick={() => setShow(!show)}
          size="xs"
          type="button"
          variant="outline"
        >
          {show ? "收起回答模型配置" : "配置回答模型"}
        </Button>
      </CardHeader>
      <CardContent className="space-y-3">
        {models.isPending && (
          <p className="text-sm text-muted-foreground" role="status">
            正在加载回答模型
          </p>
        )}
        {models.isError && (
          <Alert className="flex items-center justify-between gap-2" variant="danger">
            <span>回答模型加载失败。</span>
            <Button
              onClick={() => void models.refetch()}
              size="xs"
              variant="outline"
            >
              重试
            </Button>
          </Alert>
        )}
        {models.data?.length === 0 && (
          <p className="text-sm text-muted-foreground">尚未配置回答模型。</p>
        )}
        {models.data && models.data.length > 0 && (
          <ul className="space-y-1.5 text-sm">
            {models.data.map((model) => (
              <li
                className="rounded-md border bg-surface-subtle px-3 py-1.5"
                key={model.id}
              >
                {model.name} · {model.model}
              </li>
            ))}
          </ul>
        )}
        {show && (
          <form
            className="grid gap-3 border-t pt-4"
            onSubmit={(event) => void save(event)}
          >
            {(
              [
                ["name", "显示名称", "例如：知识助手"],
                ["base_url", "服务根地址", "https://model.example.com"],
                ["model", "模型名称", "服务提供的模型标识"],
                ["api_key", "API 密钥", "可选；仅发送至本服务器加密保存"],
              ] as const
            ).map(([name, label, placeholder]) => (
              <Field htmlFor={`chat-${name}`} key={name} label={label}>
                <Input
                  autoComplete={name === "api_key" ? "new-password" : "off"}
                  disabled={saving}
                  id={`chat-${name}`}
                  maxLength={
                    name === "api_key" ? 4096 : name === "base_url" ? 2048 : 200
                  }
                  name={name}
                  placeholder={placeholder}
                  required={name !== "api_key"}
                  type={
                    name === "api_key"
                      ? "password"
                      : name === "base_url"
                        ? "url"
                        : "text"
                  }
                />
              </Field>
            ))}
            <Field htmlFor="chat-locality" label="服务位置">
              <Select
                defaultValue="external"
                disabled={saving}
                id="chat-locality"
                name="locality"
              >
                <option value="external">组织外部（需要允许出域）</option>
                <option value="internal">组织内部</option>
              </Select>
            </Field>
            <Button disabled={saving} size="sm" type="submit">
              {saving ? "正在保存…" : "保存回答模型"}
            </Button>
            {error && <Alert variant="danger">{error}</Alert>}
          </form>
        )}
      </CardContent>
    </Card>
  );
}
