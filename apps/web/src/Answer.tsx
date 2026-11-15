import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type AnswerEvidence,
  ApiError,
  createConversation,
  getConversation,
  getKnowledgeBase,
  type Identity,
  listChatModels,
  listConversations,
  streamConversation,
} from "@tuskrag/api-client";
import { Alert } from "@tuskrag/ui/alert";
import { Button } from "@tuskrag/ui/button";
import { Card } from "@tuskrag/ui/card";
import { Label } from "@tuskrag/ui/label";
import { SectionHeader } from "@tuskrag/ui/section-header";
import { Select, SelectItem } from "@tuskrag/ui/select";
import { Textarea } from "@tuskrag/ui/textarea";
import { WorkspaceSplit } from "@tuskrag/ui/workspace-split";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { type AnswerState, AnswerView, emptyAnswer } from "./AnswerView";
import type { PreviewTarget } from "./DocumentPreview";
import { Preview } from "./Preview";

export function AnswerPage({
  identity,
  id,
  conversation = 0,
  onSelect,
}: {
  identity: Identity;
  id: number;
  conversation?: number | undefined;
  onSelect: (id: number) => void;
}) {
  const cache = useQueryClient();
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
    gcTime: 0,
  });
  const models = useQuery({
    queryKey: ["chat-models", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listChatModels(signal),
    retry: false,
  });
  const [cursor, setCursor] = useState(0),
    [question, setQuestion] = useState(""),
    [model, setModel] = useState(0),
    [mode, setMode] = useState<"standard" | "agentic" | "auto">("standard"),
    [state, setState] = useState<AnswerState>(emptyAnswer),
    [preview, setPreview] = useState<PreviewTarget | null>(null);
  const active = useRef<AbortController | null>(null),
    activeConversation = useRef(0),
    questionRef = useRef<HTMLTextAreaElement>(null),
    formRef = useRef<HTMLFormElement>(null);
  const conversations = useQuery({
    queryKey: [
      "conversations",
      identity.tenant_id,
      identity.user_id,
      id,
      cursor,
    ],
    queryFn: ({ signal }) => listConversations(id, cursor, signal),
    enabled: base.data?.status === "active",
    retry: false,
    gcTime: 0,
  });
  const history = useQuery({
    queryKey: [
      "conversation",
      identity.tenant_id,
      identity.user_id,
      conversation,
    ],
    queryFn: async ({ signal }) => {
      const value = await getConversation(conversation, signal);
      if (value.conversation.kb_id !== id)
        throw new Error("conversation belongs to another knowledge base");
      return value;
    },
    enabled: conversation > 0,
    retry: false,
    gcTime: 0,
    refetchInterval: (query) =>
      query.state.data?.messages.some((m) => m.status === "running")
        ? 2000
        : false,
  });
  useEffect(() => () => active.current?.abort(), []);
  useEffect(() => {
    if (activeConversation.current === conversation) return;
    activeConversation.current = conversation;
    active.current?.abort();
    setState(emptyAnswer);
    setPreview(null);
    setQuestion("");
  }, [conversation]);
  useEffect(() => {
    if (base.isError || history.isError) {
      active.current?.abort();
      setState(emptyAnswer);
      setPreview(null);
    }
  }, [base.isError, history.isError]);
  const modelID =
    history.data?.conversation.model_id || model || models.data?.[0]?.id || 0;
  const historyBlocked =
    conversation > 0 && (history.isPending || history.isError);
  const running =
    state.phase === "loading" ||
    Boolean(history.data?.messages.some((m) => m.status === "running"));
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (
      active.current ||
      !question.trim() ||
      !modelID ||
      historyBlocked ||
      running
    )
      return;
    const controller = new AbortController(),
      asked = question.trim();
    active.current = controller;
    activeConversation.current = conversation;
    setPreview(null);
    setState({ ...emptyAnswer, phase: "loading", question: asked });
    let selected = conversation;
    try {
      if (!selected) {
        const created = await createConversation(
          {
            kb_id: id,
            model_id: modelID,
            title: [...asked].slice(0, 120).join(""),
          },
          identity.csrf_token,
          controller.signal,
        );
        selected = created.id;
        activeConversation.current = selected;
        onSelect(selected);
        setCursor(0);
        void cache.invalidateQueries({
          queryKey: ["conversations", identity.tenant_id, identity.user_id, id],
        });
      }
      await streamConversation(
        selected,
        asked,
        identity.csrf_token,
        controller.signal,
        (event) => {
          if (controller.signal.aborted) return;
          if (event.event === "error") {
            setPreview(null);
            setState({
              ...emptyAnswer,
              phase: "error",
              error: event.data.message,
            });
            return;
          }
          setState((previous) => {
            if (event.event === "step")
              return {
                ...previous,
                steps: [...(previous.steps ?? []), event.data],
              };
            if (event.event === "retrieval")
              return {
                ...previous,
                evidence: event.data.evidence,
                degraded: event.data.degraded,
              };
            if (event.event === "delta")
              return { ...previous, text: previous.text + event.data.text };
            if (event.event === "citations")
              return {
                ...previous,
                cited: event.data.items.map((item) => item.n),
              };
            return { ...previous, phase: "complete", done: event.data };
          });
        },
        mode === "standard" ? undefined : mode,
      );
      if (activeConversation.current === selected) setQuestion("");
    } catch (error) {
      if (activeConversation.current !== selected) return;
      setPreview(null);
      if (controller.signal.aborted)
        setState({ ...emptyAnswer, phase: "cancelled" });
      else
        setState({
          ...emptyAnswer,
          phase: "error",
          error:
            error instanceof ApiError && error.status === 401
              ? "登录已过期，请重新登录。"
              : "回答未完成，请刷新会话后重试。",
        });
    } finally {
      if (active.current === controller) active.current = null;
      if (selected)
        void cache.invalidateQueries({
          queryKey: [
            "conversation",
            identity.tenant_id,
            identity.user_id,
            selected,
          ],
        });
    }
  }
  function select(id: number) {
    active.current?.abort();
    setState(emptyAnswer);
    setPreview(null);
    onSelect(id);
  }
  function openEvidence(item: AnswerEvidence) {
    setPreview({
      id: item.document_id,
      versionId: item.version_id,
      title: item.title,
      initialSection: item.section_id,
      includePrevious: true,
    });
  }
  if (base.isPending)
    return (
      <p role="status" className="py-6 text-sm text-muted-foreground">
        正在加载知识库
      </p>
    );
  if (base.isError)
    return (
      <p role="alert" className="py-6">
        知识库不可用或访问权限已变化。
      </p>
    );
  const historyPane = (
    <aside className="space-y-3" aria-label="会话历史">
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-full"
        disabled={Boolean(active.current)}
        onClick={() => select(0)}
      >
        新建会话
      </Button>
      {conversations.isPending && base.data.status === "active" && (
        <p role="status" className="text-sm text-muted-foreground">
          正在加载会话
        </p>
      )}
      {conversations.isError && (
        <Alert
          className="flex items-center justify-between gap-2"
          variant="danger"
        >
          <span>会话列表不可用。</span>
          <Button
            type="button"
            size="xs"
            variant="outline"
            onClick={() => void conversations.refetch()}
          >
            重试
          </Button>
        </Alert>
      )}
      {conversations.data?.items.length === 0 && (
        <p className="text-sm text-muted-foreground">
          暂无会话，提问后自动保存。
        </p>
      )}
      <ul className="space-y-1.5">
        {conversations.data?.items.map((item) => (
          <li key={item.id}>
            <Button
              type="button"
              size="sm"
              variant={conversation === item.id ? "secondary" : "ghost"}
              className="h-auto w-full justify-start whitespace-normal break-words px-2.5 py-2 text-left font-normal"
              aria-current={conversation === item.id ? "page" : undefined}
              disabled={Boolean(active.current)}
              onClick={() => select(item.id)}
            >
              {item.title}
            </Button>
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-2 text-sm">
        {cursor > 0 && (
          <Button
            type="button"
            size="xs"
            variant="ghost"
            className="underline"
            onClick={() => setCursor(0)}
          >
            最新会话
          </Button>
        )}
        {conversations.data?.next_cursor && (
          <Button
            type="button"
            size="xs"
            variant="ghost"
            className="underline"
            onClick={() => {
              const next = conversations.data?.next_cursor;
              if (next) setCursor(next);
            }}
          >
            更早会话
          </Button>
        )}
      </div>
    </aside>
  );

  const mainPane = (
    <div className="min-w-0 space-y-5">
      {conversation > 0 && history.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          正在读取会话
        </p>
      )}
      {conversation > 0 && history.isError && (
        <Alert className="space-y-2" variant="danger">
          <p>会话不可用，权限或历史引用可能已变化。</p>
          <Button
            type="button"
            size="xs"
            variant="outline"
            onClick={() => void history.refetch()}
          >
            重新读取
          </Button>
        </Alert>
      )}
      {!history.isError &&
        state.phase !== "error" &&
        history.data?.messages
          .filter((m) => m.id !== state.done?.message_id)
          .map((message) =>
            message.status === "complete" ? (
              <AnswerView
                key={message.id}
                state={{
                  phase: "complete",
                  question: message.question,
                  text: message.content,
                  evidence: message.evidence,
                  cited: message.citations.items.map((c) => c.n),
                  degraded: message.degraded,
                  done: message.done,
                }}
                onEvidence={openEvidence}
                identity={identity}
                feedback={message.feedback}
                saved
              />
            ) : message.status === "running" ? (
              !active.current && (
                <p
                  key={message.id}
                  role="status"
                  className="text-sm text-muted-foreground"
                >
                  另一窗口正在回答，请稍候。
                </p>
              )
            ) : (
              <Card key={message.id} className="p-4 text-sm">
                <p className="break-words font-medium">{message.question}</p>
                <p className="mt-2 text-muted-foreground">
                  {message.status === "cancelled"
                    ? "已停止回答。"
                    : "回答未完成，可重新提问。"}
                </p>
              </Card>
            ),
          )}
      {!history.isError && (
        <AnswerView
          state={state}
          onEvidence={openEvidence}
          identity={identity}
          saved={Boolean(state.done)}
          feedback={
            history.data?.messages.find((m) => m.id === state.done?.message_id)
              ?.feedback
          }
        />
      )}
      {base.data.status === "archived" ? (
        <Alert variant="warning">知识库已归档，不能发起问答。</Alert>
      ) : (
        <div className="sticky bottom-0 -mx-4 -mb-4 rounded-b-xl border-t bg-surface px-4 pb-4 pt-4">
          <form
            ref={formRef}
            onSubmit={(event) => void submit(event)}
            className="space-y-3"
          >
            <div className="grid gap-3 sm:grid-cols-2">
              <Label className="block space-y-1.5 text-xs font-medium">
                <span>回答模型</span>
                <Select
                  value={modelID}
                  onValueChange={(value) => setModel(Number(value))}
                  disabled={conversation > 0 || running || !models.data?.length}
                >
                  {!models.data?.length && (
                    <SelectItem value={0}>暂无可用模型</SelectItem>
                  )}
                  {models.data?.map((item) => (
                    <SelectItem key={item.id} value={item.id}>
                      {item.name} · {item.model}
                    </SelectItem>
                  ))}
                </Select>
              </Label>
              <Label className="block space-y-1.5 text-xs font-medium">
                <span>问答模式</span>
                <Select
                  aria-label="问答模式"
                  value={mode}
                  disabled={running}
                  onValueChange={(value) =>
                    setMode(value as "standard" | "agentic" | "auto")
                  }
                >
                  <SelectItem value="standard">标准模式 (Standard)</SelectItem>
                  <SelectItem value="auto">智能路由 (Auto)</SelectItem>
                  <SelectItem value="agentic">Agentic 多步推理</SelectItem>
                </Select>
              </Label>
            </div>
            {models.isError && (
              <Alert
                className="flex items-center justify-between gap-2"
                variant="danger"
              >
                <span>无法加载回答模型。</span>
                <Button
                  type="button"
                  size="xs"
                  variant="outline"
                  onClick={() => void models.refetch()}
                >
                  重试
                </Button>
              </Alert>
            )}
            {models.data?.length === 0 && (
              <p className="text-sm text-muted-foreground">
                请管理员在知识库总览的“模型配置”中添加回答模型。
              </p>
            )}
            <Label className="block space-y-1.5 text-xs font-medium">
              <span>你的问题</span>
              <Textarea
                ref={questionRef}
                className="min-h-24 resize-y"
                value={question}
                maxLength={4096}
                onChange={(event) => setQuestion(event.target.value)}
                onKeyDown={(event) => {
                  if (
                    event.key === "Enter" &&
                    (event.ctrlKey || event.metaKey) &&
                    !event.nativeEvent.isComposing
                  ) {
                    event.preventDefault();
                    formRef.current?.requestSubmit();
                  }
                }}
                placeholder={
                  conversation ? "继续追问…" : "例如：申请需要准备哪些材料？"
                }
                required
              />
            </Label>
            <div className="flex items-center gap-3">
              <Button
                disabled={
                  !modelID || !question.trim() || running || historyBlocked
                }
                type="submit"
              >
                {state.phase === "loading" ? "正在回答…" : "提问"}
              </Button>
              {state.phase === "loading" && (
                <Button
                  variant="outline"
                  type="button"
                  onClick={() => {
                    active.current?.abort();
                    questionRef.current?.focus();
                  }}
                >
                  停止回答
                </Button>
              )}
              <span className="ml-auto hidden text-xs text-muted-foreground sm:inline">
                Ctrl + Enter 发送
              </span>
            </div>
          </form>
        </div>
      )}
      {!history.isError && preview && (
        <Preview
          key={`${preview.id}:${preview.versionId}:${preview.initialSection}`}
          identity={identity}
          target={preview}
          onClose={() => setPreview(null)}
        />
      )}
    </div>
  );

  return (
    <section className="space-y-4">
      <SectionHeader title={`${base.data.name} · 问答`} />
      <WorkspaceSplit
        className="lg:min-h-[calc(100dvh-9rem)]"
        panes={[
          {
            id: "answer-history",
            title: "会话历史",
            defaultWidth: 230,
            minWidth: 180,
            maxWidth: 360,
            collapsible: true,
            content: historyPane,
          },
          {
            id: "answer-main",
            title: "对话与证据",
            content: mainPane,
          },
        ]}
        storageKey={`tuskrag.answer.${id}`}
      />
    </section>
  );
}
