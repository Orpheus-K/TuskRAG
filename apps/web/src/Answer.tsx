import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
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
import { Button } from "@tuskrag/ui/button";
import { type FormEvent, useEffect, useRef, useState } from "react";
import {
  type AnswerState,
  AnswerView,
  answerInputClass,
  emptyAnswer,
} from "./AnswerView";
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
    [state, setState] = useState<AnswerState>(emptyAnswer),
    [preview, setPreview] = useState<PreviewTarget | null>(null);
  const active = useRef<AbortController | null>(null),
    activeConversation = useRef(0),
    questionRef = useRef<HTMLTextAreaElement>(null);
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
      <p role="status" className="py-12">
        正在加载知识库
      </p>
    );
  if (base.isError)
    return (
      <p role="alert" className="py-12">
        知识库不可用或访问权限已变化。
      </p>
    );
  return (
    <section className="mx-auto max-w-5xl space-y-6 py-8 sm:py-12">
      <nav className="flex flex-wrap gap-4 text-sm">
        <Link className="underline" to="/">
          选择知识库
        </Link>
        <Link
          className="underline"
          to="/kb/$kbId/documents"
          params={{ kbId: String(id) }}
          search={{ cursor: 0 }}
        >
          查看文档
        </Link>
      </nav>
      <div>
        <h1 className="text-2xl font-semibold">{base.data.name} · 问答</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          依据知识库回答，点击引用查看原文。会话仅自己可见。
        </p>
      </div>
      <div className="grid items-start gap-6 md:grid-cols-[200px_minmax(0,1fr)]">
        <aside
          className="max-h-72 space-y-3 overflow-y-auto rounded-xl border p-4 md:max-h-[calc(100vh-5rem)]"
          aria-label="会话历史"
        >
          <Button
            type="button"
            variant="outline"
            className="w-full"
            disabled={Boolean(active.current)}
            onClick={() => select(0)}
          >
            新建会话
          </Button>
          {conversations.isPending && base.data.status === "active" && (
            <p role="status" className="text-sm">
              正在加载会话
            </p>
          )}
          {conversations.isError && (
            <p role="alert" className="text-sm">
              会话列表不可用。
              <button
                type="button"
                className="underline"
                onClick={() => void conversations.refetch()}
              >
                重试
              </button>
            </p>
          )}
          {conversations.data?.items.length === 0 && (
            <p className="text-sm text-muted-foreground">
              暂无会话，提问后自动保存。
            </p>
          )}
          <ul className="space-y-2">
            {conversations.data?.items.map((item) => (
              <li key={item.id}>
                <button
                  type="button"
                  className={`w-full break-words rounded p-2 text-left text-sm ${conversation === item.id ? "bg-muted font-medium" : "hover:bg-muted"}`}
                  aria-current={conversation === item.id ? "page" : undefined}
                  disabled={Boolean(active.current)}
                  onClick={() => select(item.id)}
                >
                  {item.title}
                </button>
              </li>
            ))}
          </ul>
          <div className="flex flex-wrap gap-3 text-sm">
            {cursor > 0 && (
              <button
                type="button"
                className="underline"
                onClick={() => setCursor(0)}
              >
                最新会话
              </button>
            )}
            {conversations.data?.next_cursor && (
              <button
                type="button"
                className="underline"
                onClick={() => {
                  const next = conversations.data?.next_cursor;
                  if (next) setCursor(next);
                }}
              >
                更早会话
              </button>
            )}
          </div>
        </aside>
        <div className="min-w-0 space-y-5">
          {conversation > 0 && history.isPending && (
            <p role="status">正在读取会话</p>
          )}
          {conversation > 0 && history.isError && (
            <div role="alert" className="rounded-xl border p-5">
              <p>会话不可用，权限或历史引用可能已变化。</p>
              <button
                type="button"
                className="mt-2 underline"
                onClick={() => void history.refetch()}
              >
                重新读取
              </button>
            </div>
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
                    <p key={message.id} role="status">
                      另一窗口正在回答，请稍候。
                    </p>
                  )
                ) : (
                  <div
                    key={message.id}
                    className="rounded-xl border p-4 text-sm"
                  >
                    <p className="break-words font-medium">
                      {message.question}
                    </p>
                    <p className="mt-2 text-muted-foreground">
                      {message.status === "cancelled"
                        ? "已停止回答。"
                        : "回答未完成，可重新提问。"}
                    </p>
                  </div>
                ),
              )}
          {!history.isError && (
            <AnswerView
              state={state}
              onEvidence={openEvidence}
              identity={identity}
              saved={Boolean(state.done)}
              feedback={
                history.data?.messages.find(
                  (m) => m.id === state.done?.message_id,
                )?.feedback
              }
            />
          )}
          {base.data.status === "archived" ? (
            <p role="status">知识库已归档，不能发起问答。</p>
          ) : (
            <form
              onSubmit={(event) => void submit(event)}
              className="space-y-4 rounded-xl border p-5"
            >
              <label className="block space-y-2 text-sm font-medium">
                <span>回答模型</span>
                <select
                  className={answerInputClass}
                  value={modelID}
                  onChange={(event) => setModel(Number(event.target.value))}
                  disabled={conversation > 0 || running || !models.data?.length}
                >
                  {!models.data?.length && (
                    <option value={0}>暂无可用模型</option>
                  )}
                  {models.data?.map((item) => (
                    <option key={item.id} value={item.id}>
                      {item.name} · {item.model}
                    </option>
                  ))}
                </select>
              </label>
              {models.isError && (
                <p role="alert">
                  无法加载回答模型。
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
                <p className="text-sm text-muted-foreground">
                  请管理员在知识库首页配置回答模型。
                </p>
              )}
              <label className="block space-y-2 text-sm font-medium">
                <span>你的问题</span>
                <textarea
                  ref={questionRef}
                  className={`${answerInputClass} min-h-24 resize-y`}
                  value={question}
                  maxLength={4096}
                  onChange={(event) => setQuestion(event.target.value)}
                  placeholder={
                    conversation ? "继续追问…" : "例如：申请需要准备哪些材料？"
                  }
                  required
                />
              </label>
              <div className="flex gap-3">
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
              </div>
            </form>
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
      </div>
    </section>
  );
}
