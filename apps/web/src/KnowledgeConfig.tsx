import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  getKnowledgeConfig,
  type Identity,
  type KnowledgeBase,
  type KnowledgeConfigSnapshot,
  type RetrievalConfig,
  saveKnowledgeConfig,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { Input } from "@tuskrag/ui/input";
import { Select } from "@tuskrag/ui/select";
import { type FormEvent, useState } from "react";

function accessDenied(error: unknown) {
  return (
    error instanceof ApiError &&
    [401, 403, 404].includes(error.status) &&
    error.code !== "provider_not_found"
  );
}

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === "provider_not_found")
      return "模型提供方编号不存在或不属于当前租户，请检查编号。";
    if (error.status === 401) return "登录已过期，请重新登录。";
    if (error.status === 403) return "当前账号已无配置权限。";
    if (error.status === 404) return "知识库或模型提供方不存在，或无权访问。";
    if (error.code === "revision_conflict")
      return "配置已被其他人修改。当前输入已保留；请载入最新配置后重新编辑。";
    if (error.code === "knowledge_base_archived")
      return "知识库已归档，无法保存配置。";
    if (error.code === "egress_denied")
      return "该模型提供方不符合当前数据出域策略。";
    if (error.status === 400 || error.status === 422)
      return "配置无效，请检查参数和模型提供方编号。";
  }
  return "操作未确认，请重新载入配置检查最新修订后再试。";
}

export function KnowledgeConfigPanel({
  identity,
  base,
}: {
  identity: Identity;
  base: KnowledgeBase;
}) {
  const [open, setOpen] = useState(false);
  const [hasOpened, setHasOpened] = useState(false);
  if (base.role !== "owner" && base.role !== "admin") return null;
  return (
    <section className="mt-6">
      <Button
        variant="outline"
        aria-expanded={open}
        aria-controls="knowledge-config"
        onClick={() => {
          setHasOpened(true);
          setOpen(!open);
        }}
      >
        检索默认配置
      </Button>
      {hasOpened && (
        <div hidden={!open}>
          <ConfigEditor
            key={`${identity.tenant_id}:${identity.user_id}:${base.id}`}
            identity={identity}
            base={base}
          />
        </div>
      )}
    </section>
  );
}

function ConfigEditor({
  identity,
  base,
}: {
  identity: Identity;
  base: KnowledgeBase;
}) {
  const client = useQueryClient();
  const key = [
    "knowledge-config",
    identity.tenant_id,
    identity.user_id,
    base.id,
  ];
  const [reset, setReset] = useState(0);
  const config = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => getKnowledgeConfig(base.id, signal),
    retry: false,
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const save = useMutation({
    mutationFn: ({
      snapshot,
      settings,
    }: {
      snapshot: KnowledgeConfigSnapshot;
      settings: RetrievalConfig;
    }) => saveKnowledgeConfig(base.id, snapshot, settings, identity.csrf_token),
    retry: false,
    onSuccess: async (result) => {
      client.setQueryData(key, result);
      const searches = [
        "search",
        identity.tenant_id,
        identity.user_id,
        base.id,
      ];
      await client.cancelQueries({ queryKey: searches });
      await client.invalidateQueries({ queryKey: searches });
    },
  });
  async function reload() {
    const result = await config.refetch();
    if (result.isSuccess) {
      save.reset();
      setReset((value) => value + 1);
    }
  }
  if (accessDenied(config.error) || accessDenied(save.error)) {
    return (
      <p className="mt-4 text-sm" role="alert">
        {errorMessage(accessDenied(save.error) ? save.error : config.error)}
      </p>
    );
  }
  if (config.isPending)
    return (
      <p className="mt-4 text-sm" role="status">
        正在加载检索配置
      </p>
    );
  if (!config.data)
    return (
      <div className="mt-4 space-y-3">
        <p role="alert">无法加载检索配置。</p>
        <Button
          variant="outline"
          onClick={() => void reload()}
          disabled={config.isFetching}
        >
          重新加载配置
        </Button>
      </div>
    );
  const conflict =
    save.error instanceof ApiError && save.error.code === "revision_conflict";
  const archived =
    base.status === "archived" ||
    (save.error instanceof ApiError &&
      save.error.code === "knowledge_base_archived");
  return (
    <div id="knowledge-config" className="mt-4 space-y-4 rounded-xl border p-5">
      <h2 className="text-lg font-medium">知识库检索默认值</h2>
      <p className="text-sm text-muted-foreground">
        {config.data.config.revision === 0
          ? "尚未保存配置，当前默认返回 8 条且不重排。"
          : `当前修订 ${config.data.config.revision}`}
      </p>
      <p className="text-sm text-muted-foreground">
        保存后用于后续检索；单次自定义参数仍优先。
      </p>
      {archived && (
        <p role="status" className="text-sm">
          知识库已归档，配置仅供查看。
        </p>
      )}
      {config.isError && <p role="alert">重新加载失败，当前输入已保留。</p>}
      {save.isError && (
        <p role="alert" className="text-sm">
          {errorMessage(save.error)}
        </p>
      )}
      {save.isSuccess && (
        <p role="status" className="text-sm">
          已保存配置，修订 {save.data.config.revision}。
        </p>
      )}
      <ConfigForm
        key={`${config.data.etag}:${reset}`}
        snapshot={config.data}
        disabled={save.isPending || config.isFetching || archived}
        preventSave={conflict}
        onSave={(settings) => save.mutate({ snapshot: config.data, settings })}
      />
      {!save.isPending && (
        <Button
          variant="outline"
          disabled={config.isFetching}
          onClick={() => void reload()}
        >
          载入最新配置（放弃当前修改）
        </Button>
      )}
    </div>
  );
}

function ConfigForm({
  snapshot,
  disabled,
  preventSave,
  onSave,
}: {
  snapshot: KnowledgeConfigSnapshot;
  disabled: boolean;
  preventSave: boolean;
  onSave: (settings: RetrievalConfig) => void;
}) {
  const saved = snapshot.config.retrieval;
  const [rerank, setRerank] = useState(saved?.reranker !== undefined);
  const [error, setError] = useState("");
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (disabled || preventSave) return;
    const data = new FormData(event.currentTarget);
    const settings: RetrievalConfig = {
      top_k: Number(data.get("config-top-k")),
    };
    if (rerank) {
      const topN = Number(data.get("config-top-n"));
      if (topN !== 20 && topN !== 30 && topN !== 50) {
        setError("请选择有效的重排候选数。");
        return;
      }
      settings.reranker = {
        provider_id: Number(data.get("config-provider")),
        model: String(data.get("config-model") ?? "").trim(),
        top_n: topN,
        fallback: data.get("config-fallback") === "on",
      };
      const threshold = String(data.get("config-min-score") ?? "").trim();
      if (threshold !== "") settings.reranker.min_score = Number(threshold);
    }
    setError("");
    onSave(settings);
  }
  return (
    <form onSubmit={submit}>
      <fieldset disabled={disabled} className="space-y-4">
        <legend className="sr-only">检索默认参数</legend>
        <label className="block space-y-1 text-sm">
          <span>默认返回条数</span>
          <Input
            name="config-top-k"
            type="number"
            min={1}
            max={30}
            step={1}
            required
            defaultValue={saved?.top_k ?? 8}
          />
        </label>
        <label className="flex items-center gap-2 text-sm">
          <Input
            type="checkbox"
            checked={rerank}
            onChange={(event) => setRerank(event.currentTarget.checked)}
          />
          <span>默认启用重排</span>
        </label>
        <fieldset disabled={!rerank} className="grid gap-4 sm:grid-cols-2">
          <legend className="sr-only">默认重排设置</legend>
          <label className="block space-y-1 text-sm">
            <span>重排提供方编号</span>
            <Input
              name="config-provider"
              type="number"
              min={1}
              step={1}
              required={rerank}
              defaultValue={saved?.reranker?.provider_id ?? ""}
            />
            <span className="text-xs text-muted-foreground">
              填写平台管理员提供的编号。
            </span>
          </label>
          <label className="block space-y-1 text-sm">
            <span>默认重排模型</span>
            <Input
              name="config-model"
              maxLength={200}
              required={rerank}
              defaultValue={saved?.reranker?.model ?? ""}
            />
          </label>
          <label className="block space-y-1 text-sm">
            <span>重排候选数</span>
            <Select
              name="config-top-n"
              defaultValue={saved?.reranker?.top_n ?? 30}
            >
              <option value={20}>20</option>
              <option value={30}>30</option>
              <option value={50}>50</option>
            </Select>
          </label>
          <label className="block space-y-1 text-sm">
            <span>最低重排分数（可留空）</span>
            <Input
              name="config-min-score"
              type="number"
              step="any"
              defaultValue={saved?.reranker?.min_score ?? ""}
            />
          </label>
          <label className="flex items-center gap-2 text-sm sm:col-span-2">
            <Input
              name="config-fallback"
              type="checkbox"
              defaultChecked={saved?.reranker?.fallback ?? false}
            />
            <span>重排服务暂不可用时允许返回未重排结果</span>
          </label>
        </fieldset>
        {error && (
          <p role="alert" className="text-sm">
            {error}
          </p>
        )}
        <Button type="submit" disabled={preventSave}>
          保存检索配置
        </Button>
      </fieldset>
    </form>
  );
}
