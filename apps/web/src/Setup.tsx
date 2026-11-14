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
import { type FormEvent, useRef, useState } from "react";

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
    <form className="mt-4 space-y-3 border-t pt-4" onSubmit={submit}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field htmlFor="provider-name" label="提供方名称">
          <Input
            id="provider-name"
            maxLength={200}
            name="provider_name"
            required
          />
        </Field>
        <Field htmlFor="provider-locality" label="网络位置">
          <Select
            defaultValue="internal"
            id="provider-locality"
            name="locality"
          >
            <option value="internal">内部网络</option>
            <option value="external">外部网络（HTTPS）</option>
          </Select>
        </Field>
        <Field
          className="sm:col-span-2"
          htmlFor="provider-url"
          label="提供方基础地址"
        >
          <Input
            id="provider-url"
            maxLength={2048}
            name="base_url"
            placeholder="https://example.com"
            required
            type="url"
          />
        </Field>
        <Field htmlFor="model-name" label="嵌入模型名称">
          <Input id="model-name" maxLength={200} name="model" required />
        </Field>
        <Field htmlFor="model-dimensions" label="向量维度">
          <Input
            id="model-dimensions"
            max="4000"
            min="64"
            name="dimensions"
            required
            step="1"
            type="number"
          />
        </Field>
        <Field htmlFor="model-distance" label="距离计算">
          <Select defaultValue="cosine" id="model-distance" name="distance">
            <option value="cosine">余弦距离</option>
            <option value="ip">内积</option>
          </Select>
        </Field>
        <Field htmlFor="model-key" label="API 密钥（可选）">
          <Input
            autoComplete="off"
            id="model-key"
            maxLength={4096}
            name="api_key"
            type="password"
          />
        </Field>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Input defaultChecked name="normalized" type="checkbox" />
        <span>向量归一化</span>
      </label>
      <p className="text-xs leading-5 text-muted-foreground">
        保存配置不会验证模型连接。API 密钥需要服务器配置加密主密钥。
      </p>
      {error && <Alert variant="danger">{error}</Alert>}
      <Button disabled={busy} size="sm" type="submit">
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
    <form className="space-y-3" onSubmit={submit}>
      <div className="grid gap-3">
        <Field htmlFor="kb-name" label="知识库名称">
          <Input id="kb-name" maxLength={200} name="name" required />
        </Field>
        <Field htmlFor="kb-space" label="嵌入模型">
          <Select
            defaultValue=""
            id="kb-space"
            name="embedding_space_id"
            required
          >
            <option disabled value="">
              选择嵌入模型
            </option>
            {spaces.map((space) => (
              <option key={space.id} value={space.id}>
                {space.provider_name} · {space.model} · {space.dimensions} 维
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">
        新知识库默认仅允许内部语言、嵌入和重排模型；VLM
        不可用。出域策略管理尚未开放。
      </p>
      {error && <Alert variant="danger">{error}</Alert>}
      <Button disabled={busy || spaces.length === 0} size="sm" type="submit">
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
    <div className="grid gap-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between space-y-0 gap-3">
          <div>
            <CardTitle>嵌入模型</CardTitle>
            <CardDescription>配置用于知识库的模型和向量空间。</CardDescription>
          </div>
          <Button
            onClick={() => setShowModelForm(!showModelForm)}
            size="xs"
            type="button"
            variant="outline"
          >
            {showModelForm ? "收起配置" : "配置模型"}
          </Button>
        </CardHeader>
        <CardContent className="space-y-3">
          {spaces.isPending ? (
            <p className="text-sm text-muted-foreground" role="status">
              正在加载模型
            </p>
          ) : spaces.isError ? (
            <div className="space-y-2">
              <Alert variant="danger">{errorMessage(spaces.error)}</Alert>
              <Button
                onClick={() => void spaces.refetch()}
                size="xs"
                variant="outline"
              >
                重试加载模型
              </Button>
            </div>
          ) : spaces.data.length === 0 ? (
            <p className="text-sm text-muted-foreground">尚未配置嵌入模型。</p>
          ) : (
            <ul className="space-y-1.5 text-sm">
              {spaces.data.map((space) => (
                <li
                  className="rounded-md border bg-surface-subtle px-3 py-1.5"
                  key={space.id}
                >
                  {space.provider_name} · {space.model} · {space.dimensions} 维
                  {space.has_credential ? " · 已保存密钥" : ""}
                </li>
              ))}
            </ul>
          )}
          {showModelForm && (
            <ModelForm identity={identity} onSaved={refreshModels} />
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>新建知识库</CardTitle>
        </CardHeader>
        <CardContent>
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
            <p className="text-sm text-muted-foreground">
              加载模型配置后可创建知识库。
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
