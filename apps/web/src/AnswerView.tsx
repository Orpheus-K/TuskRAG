import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  type AnswerDone,
  type AnswerEvidence,
  type AnswerFeedback,
  type AnswerStep,
  type Identity,
  putAnswerFeedback,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { useState } from "react";

export type AnswerState = {
  phase: "idle" | "loading" | "complete" | "cancelled" | "error";
  question?: string;
  text: string;
  steps?: AnswerStep[];
  evidence: AnswerEvidence[];
  cited: number[];
  degraded: string[];
  done?: AnswerDone | undefined;
  error?: string;
};
export const emptyAnswer: AnswerState = {
  phase: "idle",
  text: "",
  steps: [],
  evidence: [],
  cited: [],
  degraded: [],
};
export const answerInputClass =
  "w-full rounded-md border bg-background px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function AnswerView({
  state,
  onEvidence,
  feedback,
  identity,
  saved = false,
}: {
  state: AnswerState;
  onEvidence: (e: AnswerEvidence) => void;
  feedback?: AnswerFeedback | undefined;
  identity: Identity;
  saved?: boolean;
}) {
  if (state.phase === "idle") return null;
  if (state.phase === "error")
    return (
      <p role="alert" className="rounded-xl border p-5">
        {state.error}
      </p>
    );
  return (
    <article aria-label="回答" className="space-y-4 rounded-xl border p-5">
      {state.question && (
        <h2 className="break-words font-medium">{state.question}</h2>
      )}
      <p role="status" className="text-sm text-muted-foreground">
        {state.phase === "loading"
          ? state.text
            ? "正在生成回答"
            : state.steps && state.steps.length > 0
              ? `Agentic 检索中（第 ${state.steps.length} 步）`
              : "正在查找依据"
          : state.phase === "cancelled"
            ? "已停止，未保存不完整回答。"
            : state.done?.outcome === "refused"
              ? "证据不足，无法回答。"
              : state.done?.outcome === "unsupported"
                ? "回答未引用有效证据，请核对原文。"
                : state.done?.outcome === "truncated"
                  ? "回答达到长度限制，内容不完整。"
                  : "回答完成"}
      </p>
      {state.degraded.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {state.degraded.includes("rerank") && "检索结果未经重排。"}
          {state.degraded.includes("context_budget") &&
            "部分依据超出上下文预算。"}
          {state.degraded.includes("rewrite") &&
            "追问改写失败，已使用原问题检索。"}
        </p>
      )}
      {state.steps && state.steps.length > 0 && (
        <ol
          aria-label="Agentic 检索步骤"
          className="space-y-1 rounded-md bg-muted/40 px-3 py-2 text-xs text-muted-foreground"
        >
          {state.steps.map((s) => (
            <li key={s.step}>
              #{s.step} <code>{s.tool}</code>（{s.args_summary}）→ {s.result_count} 条结果
            </li>
          ))}
        </ol>
      )}
      <div className="whitespace-pre-wrap break-words leading-7">
        {[...state.text.matchAll(/(\[\d+\])|([^[]+|\[)/g)].map((match) => {
          const part = match[0],
            n = /^\[(\d+)\]$/.exec(part)?.[1],
            item = n
              ? state.evidence.find((e) => e.n === Number(n))
              : undefined;
          return item && state.cited.includes(item.n) ? (
            <button
              key={`${match.index}-${part}`}
              type="button"
              className="mx-0.5 rounded border px-1 text-sm underline"
              aria-label={`引用 ${item.n}：${item.title}`}
              onClick={() => onEvidence(item)}
            >
              {part}
            </button>
          ) : (
            part
          );
        })}
      </div>
      {state.cited.length > 0 && (
        <ul aria-label="引用来源" className="space-y-2 border-t pt-4">
          {state.evidence
            .filter((e) => state.cited.includes(e.n))
            .map((item) => (
              <li key={item.id}>
                <button
                  type="button"
                  className="text-left text-sm underline"
                  onClick={() => onEvidence(item)}
                >
                  [{item.n}] {item.title} · {item.heading_path.join(" › ")}
                  {item.pages && ` · 第 ${item.pages[0]}—${item.pages[1]} 页`}
                </button>
              </li>
            ))}
        </ul>
      )}
      {saved && state.done && (
        <FeedbackForm
          key={state.done.message_id}
          id={state.done.message_id}
          initial={feedback}
          identity={identity}
        />
      )}
    </article>
  );
}

function FeedbackForm({
  id,
  initial,
  identity,
}: {
  id: string;
  initial?: AnswerFeedback | undefined;
  identity: Identity;
}) {
  const [rating, setRating] = useState<-1 | 1 | 0>(initial?.rating ?? 0),
    [correction, setCorrection] = useState(initial?.correction ?? "");
  const cache = useQueryClient();
  const mutation = useMutation({
    mutationFn: async () => {
      if (rating === 0 || new TextEncoder().encode(correction).length > 4000)
        throw new Error("invalid feedback");
      await putAnswerFeedback(id, { rating, correction }, identity.csrf_token);
    },
    onSuccess: () =>
      cache.invalidateQueries({
        queryKey: ["conversation", identity.tenant_id, identity.user_id],
      }),
  });
  const tooLong = new TextEncoder().encode(correction).length > 4000;
  return (
    <form
      className="space-y-3 border-t pt-4"
      onSubmit={(event) => {
        event.preventDefault();
        mutation.mutate();
      }}
    >
      <p className="text-sm font-medium">这条回答有帮助吗？</p>
      <div className="flex gap-2">
        <Button
          type="button"
          size="sm"
          variant={rating === 1 ? "default" : "outline"}
          aria-pressed={rating === 1}
          onClick={() => setRating(1)}
        >
          有帮助
        </Button>
        <Button
          type="button"
          size="sm"
          variant={rating === -1 ? "default" : "outline"}
          aria-pressed={rating === -1}
          onClick={() => setRating(-1)}
        >
          无帮助
        </Button>
      </div>
      <label className="block space-y-2 text-sm">
        <span>纠错建议（选填）</span>
        <textarea
          className={answerInputClass}
          value={correction}
          maxLength={4000}
          onChange={(event) => setCorrection(event.target.value)}
        />
      </label>
      {tooLong && (
        <p role="alert" className="text-sm">
          纠错建议过长，请缩短。
        </p>
      )}
      <Button
        size="sm"
        variant="outline"
        disabled={rating === 0 || tooLong || mutation.isPending}
        type="submit"
      >
        {mutation.isPending ? "正在保存…" : "保存反馈"}
      </Button>
      {mutation.isError && (
        <p role="alert" className="text-sm">
          反馈未保存，请刷新会话后重试。
        </p>
      )}
      {(mutation.isSuccess || initial) && !mutation.isError && (
        <p role="status" className="text-sm text-muted-foreground">
          反馈已保存，等待审核。
        </p>
      )}
    </form>
  );
}
