import { Label } from "@tuskrag/ui/label";
import { Checkbox } from "@tuskrag/ui/checkbox";
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
import { Card, CardContent, CardHeader, CardTitle } from "@tuskrag/ui/card";
import { Dialog } from "@tuskrag/ui/dialog";
import { Field } from "@tuskrag/ui/field";
import { Input } from "@tuskrag/ui/input";
import { Select, SelectItem } from "@tuskrag/ui/select";
import { Plus } from "lucide-react";
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
  onRefresh,
}: {
  identity: Identity;
  onSaved: () => void;
  onRefresh: () => void;
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
      onRefresh();
    } finally {
      const field = form.elements.namedItem("api_key");
      if (field instanceof HTMLInputElement) field.value = "";
      setBusy(false);
    }
  }

  return (
    <form className="space-y-3" onSubmit={submit}>
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
            <SelectItem value="internal">内部网络</SelectItem>
            <SelectItem value="external">外部网络（HTTPS）</SelectItem>
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
            <SelectItem value="cosine">余弦距离</SelectItem>
            <SelectItem value="ip">内积</SelectItem>
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
      <Label className="flex items-center gap-2 text-sm">
        <Checkbox defaultChecked name="normalized" />
        <span>向量归一化</span>
      </Label>
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
            placeholder="选择嵌入模型"
            name="embedding_space_id"
            required
          >
            {spaces.map((space) => (
              <SelectItem key={space.id} value={space.id}>
                {space.provider_name} · {space.model} · {space.dimensions} 维
              </SelectItem>
            ))}
          </Select>
        </Field>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">
        新知识库默认仅允许使用内部模型。
      </p>
      {error && <Alert variant="danger">{error}</Alert>}
      <Button disabled={busy || spaces.length === 0} size="sm" type="submit">
        {busy ? "正在创建" : "创建知识库"}
      </Button>
    </form>
  );
}

function useEmbeddingSpaces(identity: Identity) {
  return useQuery({
    queryKey: ["embedding-spaces", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listEmbeddingSpaces(signal),
    retry: false,
  });
}

export function EmbeddingModels({ identity }: { identity: Identity }) {
  const queryClient = useQueryClient();
  const [showModelForm, setShowModelForm] = useState(false);
  const spaces = useEmbeddingSpaces(identity);
  function refreshModels() {
    void queryClient.invalidateQueries({ queryKey: ["embedding-spaces"] });
  }
  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between space-y-0 gap-3">
        <CardTitle>嵌入模型</CardTitle>
        <Dialog
          onOpenChange={setShowModelForm}
          open={showModelForm}
          title="添加嵌入模型"
          trigger={
            <Button size="xs" variant="outline">
              <Plus aria-hidden="true" className="h-3.5 w-3.5" />
              配置模型
            </Button>
          }
        >
          <ModelForm
            identity={identity}
            onRefresh={refreshModels}
            onSaved={() => {
              refreshModels();
              setShowModelForm(false);
            }}
          />
        </Dialog>
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
      </CardContent>
    </Card>
  );
}

export function CreateKnowledgeBase({
  identity,
  onCreated,
}: {
  identity: Identity;
  onCreated: () => void;
}) {
  const queryClient = useQueryClient();
  const spaces = useEmbeddingSpaces(identity);
  if (spaces.isError) {
    return (
      <div className="space-y-3">
        <Alert variant="danger">{errorMessage(spaces.error)}</Alert>
        <Button
          onClick={() => void spaces.refetch()}
          size="xs"
          variant="outline"
        >
          重试加载模型
        </Button>
      </div>
    );
  }
  if (!spaces.data) return <Alert variant="info">正在加载模型</Alert>;
  return (
    <KnowledgeBaseForm
      identity={identity}
      spaces={spaces.data}
      onSaved={() => {
        void queryClient.invalidateQueries({ queryKey: ["knowledge-bases"] });
        onCreated();
      }}
    />
  );
}
