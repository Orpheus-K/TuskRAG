import { Label } from "@tuskrag/ui/label";
import { Checkbox } from "@tuskrag/ui/checkbox";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  type EvalSetCase,
  type EvalSetDefinition,
  type EvalSetSnapshot,
  getEvalSet,
  getKnowledgeBase,
  type Identity,
  isEvalSetDefinition,
  listEvalSets,
  saveEvalSet,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { Input } from "@tuskrag/ui/input";
import { Textarea } from "@tuskrag/ui/textarea";
import { useState } from "react";

const denied = (e: unknown) =>
  e instanceof ApiError && [401, 403, 404].includes(e.status);
type DraftCase = {
  id: string;
  question: string;
  answerable: boolean;
  reference_answer: string;
  tags: string;
  originalTags: string[];
  expected: { key: string; document: string; section: string; page: string }[];
};
function draft(c?: EvalSetCase): DraftCase {
  return {
    id: c?.id ?? "",
    question: c?.question ?? "",
    answerable: c?.answerable ?? false,
    reference_answer: c?.reference_answer ?? "",
    tags: c?.tags?.join(", ") ?? "",
    originalTags: c?.tags ?? [],
    expected: (c?.expected ?? []).map((t) => ({
      key: crypto.randomUUID(),
      document: String(t.document_id),
      section: t.section_id ? String(t.section_id) : "",
      page: t.page ? String(t.page) : "",
    })),
  };
}
function SetForm({
  snapshot,
  disabled,
  preventSave,
  onSave,
}: {
  snapshot: EvalSetSnapshot | null;
  disabled: boolean;
  preventSave: boolean;
  onSave: (d: EvalSetDefinition, key: string) => void;
}) {
  const [name, setName] = useState(snapshot?.value.definition.name ?? "");
  const [cases, setCases] = useState(
    () => snapshot?.value.definition.cases?.map(draft) ?? [draft()],
  );
  const [index, setIndex] = useState(0);
  const [key] = useState(() => crypto.randomUUID());
  const [error, setError] = useState("");
  const current = cases[index];
  function update(change: Partial<DraftCase>) {
    setCases((values) =>
      values.map((c, i) => (i === index ? { ...c, ...change } : c)),
    );
  }
  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (disabled || preventSave) return;
        const definition: EvalSetDefinition = {
          name,
          cases: cases.map((c) => ({
            id: c.id,
            question: c.question,
            answerable: c.answerable,
            reference_answer: c.reference_answer,
            tags:
              c.tags === c.originalTags.join(", ")
                ? c.originalTags
                : c.tags
                    .split(/[,，]/)
                    .map((t) => t.trim())
                    .filter(Boolean),
            expected: c.answerable
              ? c.expected.map((t) => ({
                  document_id: Number(t.document),
                  section_id: Number(t.section),
                  page: Number(t.page),
                }))
              : [],
          })),
        };
        if (!isEvalSetDefinition(definition)) {
          setError(
            "请检查题集名称、唯一题号、问题及期望位置；有答案题至少需要一个有效文档编号，标签和位置不能重复。",
          );
          return;
        }
        setError("");
        onSave(definition, key);
      }}
    >
      <fieldset disabled={disabled} className="min-w-0 space-y-4">
        <Label className="block space-y-1 text-sm">
          <span>题集名称</span>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
          />
        </Label>
      </fieldset>
      <div className="flex flex-wrap items-center gap-2">
        <span>
          第 {index + 1} / {cases.length} 题
        </span>
        <Button
          type="button"
          variant="outline"
          disabled={index === 0}
          onClick={() => setIndex(index - 1)}
        >
          上一题
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={index >= cases.length - 1}
          onClick={() => setIndex(index + 1)}
        >
          下一题
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={disabled || cases.length >= 10000}
          onClick={() => {
            setCases([...cases, draft()]);
            setIndex(cases.length);
          }}
        >
          添加题目
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={disabled || cases.length === 1}
          onClick={() => {
            setCases(cases.filter((_, i) => i !== index));
            setIndex(Math.max(0, index - 1));
          }}
        >
          移除当前题
        </Button>
      </div>
      <fieldset disabled={disabled} className="min-w-0 space-y-4">
        {current && (
          <div className="space-y-4 rounded-lg border p-4">
            <Label className="block space-y-1 text-sm">
              <span>题号</span>
              <Input
                value={current.id}
                onChange={(e) => update({ id: e.target.value })}
                required
              />
            </Label>
            <Label className="block space-y-1 text-sm">
              <span>问题</span>
              <Textarea
                rows={3}
                value={current.question}
                onChange={(e) => update({ question: e.target.value })}
                required
              />
            </Label>
            <Label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={current.answerable}
                onCheckedChange={(checked) => update({ answerable: checked })}
              />
              应有答案
            </Label>
            {!current.answerable && (
              <p className="text-sm text-muted-foreground">
                无答案题不保存期望位置。
              </p>
            )}
            {current.answerable && (
              <section className="space-y-3" aria-label="期望位置">
                <p className="text-sm">
                  文档编号必填；节编号和页码留空表示不限。保存时会核验当前位置与权限。
                </p>
                {current.expected.map((target) => (
                  <div
                    key={target.key}
                    className="grid gap-2 rounded-md border p-3 sm:grid-cols-3"
                  >
                    {(
                      [
                        ["document", "文档编号"],
                        ["section", "节编号"],
                        ["page", "页码"],
                      ] as const
                    ).map(([field, label]) => (
                      <Label key={field} className="space-y-1 text-sm">
                        <span>{label}</span>
                        <Input
                          type="number"
                          min="1"
                          step="1"
                          required={field === "document"}
                          value={target[field]}
                          onChange={(e) =>
                            update({
                              expected: current.expected.map((t) =>
                                t.key === target.key
                                  ? { ...t, [field]: e.target.value }
                                  : t,
                              ),
                            })
                          }
                        />
                      </Label>
                    ))}
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() =>
                        update({
                          expected: current.expected.filter(
                            (t) => t.key !== target.key,
                          ),
                        })
                      }
                    >
                      移除位置
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  disabled={current.expected.length >= 100}
                  onClick={() =>
                    update({
                      expected: [
                        ...current.expected,
                        {
                          key: crypto.randomUUID(),
                          document: "",
                          section: "",
                          page: "",
                        },
                      ],
                    })
                  }
                >
                  添加期望位置
                </Button>
              </section>
            )}
            <Label className="block space-y-1 text-sm">
              <span>参考答案（可选）</span>
              <Textarea
                rows={3}
                value={current.reference_answer}
                onChange={(e) => update({ reference_answer: e.target.value })}
              />
            </Label>
            <Label className="block space-y-1 text-sm">
              <span>标签（逗号分隔）</span>
              <Input
                value={current.tags}
                onChange={(e) => update({ tags: e.target.value })}
              />
            </Label>
          </div>
        )}
      </fieldset>
      {error && <p role="alert">{error}</p>}
      <Button type="submit" disabled={disabled || preventSave}>
        {snapshot ? "保存新修订" : "创建题集"}
      </Button>
    </form>
  );
}

export function EvaluationSets({
  identity,
  id,
  cursor,
  selected,
  onChange,
}: {
  identity: Identity;
  id: number;
  cursor: number;
  selected: number;
  onChange: (v: { cursor?: number; set?: number }) => void;
}) {
  const client = useQueryClient();
  const scope = ["eval-sets", identity.tenant_id, identity.user_id, id];
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
  });
  const admin = base.data?.role === "owner" || base.data?.role === "admin";
  const list = useQuery({
    queryKey: [...scope, "list", cursor],
    queryFn: ({ signal }) => listEvalSets(id, cursor, signal),
    enabled: admin,
    retry: false,
    gcTime: 0,
  });
  const detailKey = [...scope, "detail", selected];
  const detail = useQuery({
    queryKey: detailKey,
    queryFn: ({ signal }) => getEvalSet(id, selected, signal),
    enabled: admin && selected > 0,
    retry: false,
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const [reset, setReset] = useState(0);
  const save = useMutation({
    mutationFn: ({
      definition,
      key,
    }: {
      definition: EvalSetDefinition;
      key: string;
    }) =>
      saveEvalSet(
        id,
        selected ? (detail.data ?? null) : null,
        definition,
        identity.csrf_token,
        key,
      ),
    retry: false,
    onSuccess: (result) => {
      client.setQueryData([...scope, "detail", result.value.id], result);
      void client.invalidateQueries({ queryKey: [...scope, "list"] });
      onChange({ set: result.value.id, cursor: 0 });
    },
  });
  if (base.isPending) return <p role="status">正在加载知识库</p>;
  if (
    base.isError ||
    denied(list.error) ||
    denied(detail.error) ||
    denied(save.error)
  )
    return (
      <section className="py-10 space-y-4">
        <p role="alert">知识库、题集或权限已不可用，内容已隐藏。</p>
        <Button onClick={() => window.location.reload()}>重新加载</Button>
      </section>
    );
  if (!admin) return <p role="alert">当前账号无题集管理权限。</p>;
  const conflict =
    save.error instanceof ApiError && save.error.code === "revision_conflict";
  const archived =
    base.data?.status === "archived" ||
    (save.error instanceof ApiError &&
      save.error.code === "knowledge_base_archived");
  const uncertain =
    save.isError &&
    !conflict &&
    !(save.error instanceof ApiError && [400, 422].includes(save.error.status));
  return (
    <section className="space-y-6 py-10">
      <Link
        to="/kb/$kbId/eval"
        params={{ kbId: String(id) }}
        search={{ cursor: "", digest: "", row: 0 }}
        className="text-sm underline"
      >
        返回评测报告
      </Link>
      <Link
        className="inline-block text-sm underline"
        to="/kb/$kbId/eval/history"
        params={{ kbId: String(id) }}
        search={{ cursor: 0 }}
      >
        运行历史
      </Link>
      <h1 className="text-3xl font-semibold break-words">
        {base.data?.name} · 评测集
      </h1>
      <p className="text-sm text-muted-foreground">
        评测使用已保存的题集修订。
      </p>
      {archived && <p role="status">知识库已归档，题集仅供查看。</p>}
      <section aria-label="题集列表" className="space-y-3">
        <h2 className="text-xl font-medium">已有题集</h2>
        {list.isPending && <p role="status">正在加载题集</p>}
        {list.isError ? (
          <p role="alert">题集列表加载失败，请重试。</p>
        ) : (
          <ul className="space-y-2">
            {list.data?.items?.map((item) => (
              <li key={item.id} className="rounded-lg border p-4">
                <Button
                  variant="outline"
                  disabled={save.isPending}
                  onClick={() => {
                    if (selected !== item.id) save.reset();
                    onChange({ set: item.id });
                  }}
                >
                  {item.name}
                </Button>
                <p className="mt-2 text-sm">
                  {item.case_count} 题 · 修订 {item.revision}
                </p>
              </li>
            ))}
          </ul>
        )}
        {list.data?.items?.length === 0 && !list.isError && (
          <p>当前页没有可见题集。</p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={list.isFetching || save.isPending}
            onClick={() => void list.refetch()}
          >
            刷新列表
          </Button>
          {cursor > 0 && (
            <Button
              variant="outline"
              disabled={save.isPending}
              onClick={() => onChange({ cursor: 0 })}
            >
              列表首页
            </Button>
          )}
          {list.data?.next_cursor != null && (
            <Button
              variant="outline"
              disabled={save.isPending}
              onClick={() => onChange({ cursor: list.data?.next_cursor ?? 0 })}
            >
              下一页题集
            </Button>
          )}
          {!archived && (
            <Button
              variant="outline"
              disabled={save.isPending}
              onClick={() => {
                save.reset();
                setReset((v) => v + 1);
                onChange({ set: 0 });
              }}
            >
              新建题集（放弃当前草稿）
            </Button>
          )}
        </div>
      </section>
      <section
        aria-label="题集编辑"
        className="rounded-xl border p-5 space-y-4"
      >
        <h2 className="text-xl font-medium">
          {selected ? `题集 ${selected}` : "新建题集"}
        </h2>
        {selected > 0 && detail.isPending ? (
          <p role="status">正在加载定义</p>
        ) : selected > 0 && detail.isError ? (
          <p role="alert">读取题集失败，请重新加载。</p>
        ) : (
          (!archived || selected > 0) && (
            <>
              {detail.data && selected > 0 && (
                <div className="space-y-2">
                  <p>当前修订 {detail.data.value.revision}</p>
                  {!archived && (
                    <Link
                      className="underline text-sm"
                      to="/kb/$kbId/eval/runs"
                      params={{ kbId: String(id) }}
                      search={{
                        set: selected,
                        revision: detail.data.value.revision,
                        run: 0,
                        row: 0,
                      }}
                    >
                      运行已保存修订
                    </Link>
                  )}
                </div>
              )}
              {save.isSuccess && (
                <p role="status">
                  已保存题集，修订 {save.data.value.revision}。
                </p>
              )}
              {save.isError && (
                <p role="alert">
                  {conflict
                    ? "题集已被修改，当前草稿已保留。请载入最新版本后重新编辑。"
                    : archived
                      ? "知识库已归档，无法保存。"
                      : uncertain
                        ? "保存结果未确认。输入已锁定，可原样重试或刷新列表核对。"
                        : "定义无效，请检查输入内容。"}
                </p>
              )}
              <SetForm
                key={`${selected}:${detail.data?.etag ?? "new"}:${reset}`}
                snapshot={selected ? (detail.data ?? null) : null}
                disabled={
                  archived || save.isPending || detail.isFetching || uncertain
                }
                preventSave={conflict}
                onSave={(definition, key) => save.mutate({ definition, key })}
              />
              {uncertain && !archived && save.variables && (
                <Button
                  disabled={save.isPending}
                  onClick={() => {
                    if (save.variables) save.mutate(save.variables);
                  }}
                >
                  原样重试保存
                </Button>
              )}
            </>
          )
        )}
        {selected > 0 && (
          <Button
            variant="outline"
            disabled={save.isPending || detail.isFetching}
            onClick={async () => {
              const result = await detail.refetch();
              if (result.isSuccess) {
                save.reset();
                setReset((v) => v + 1);
              }
            }}
          >
            载入最新题集（放弃当前草稿）
          </Button>
        )}
      </section>
    </section>
  );
}
