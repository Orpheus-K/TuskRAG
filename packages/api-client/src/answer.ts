import { ApiError } from "./index";
import type { components } from "./schema";

export type ChatModel = components["schemas"]["ChatModel"];
export type NewChatModel = components["schemas"]["NewChatModel"];
export type AnswerRequest = components["schemas"]["Request"];
export type AnswerEvidence = components["schemas"]["AnswerEvidence"];
export type AnswerDone = components["schemas"]["Done"];
export type Conversation = components["schemas"]["Conversation"];
export type ConversationDetail = components["schemas"]["ConversationDetail"];
export type ConversationPage = components["schemas"]["ConversationPage"];
export type SavedMessage = components["schemas"]["SavedMessage"];
export type AnswerFeedback = components["schemas"]["Feedback"];
export type AnswerStep = components["schemas"]["StepEvent"];
export type AnswerEvent =
  | { event: "step"; data: AnswerStep }
  | { event: "retrieval"; data: components["schemas"]["RetrievalEvent"] }
  | { event: "delta"; data: components["schemas"]["Delta"] }
  | { event: "citations"; data: components["schemas"]["Citations"] }
  | { event: "done"; data: AnswerDone }
  | { event: "error"; data: components["schemas"]["AnswerError"] };

const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const positive = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v > 0;
const nonnegative = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
function isModel(v: unknown): v is ChatModel {
  return (
    record(v) &&
    positive(v.id) &&
    typeof v.name === "string" &&
    typeof v.model === "string"
  );
}
async function checked(response: Response) {
  if (response.ok) return response;
  const value: unknown = await response.json();
  throw new ApiError(
    response.status,
    record(value) && record(value.error) && typeof value.error.code === "string"
      ? value.error.code
      : "invalid_response",
    "",
  );
}
export async function listChatModels(
  signal: AbortSignal,
): Promise<ChatModel[]> {
  const response = await checked(
    await fetch("/api/v1/chat-models", { signal, credentials: "same-origin" }),
  );
  const value: unknown = await response.json();
  if (
    !record(value) ||
    !Array.isArray(value.items) ||
    !value.items.every(isModel)
  )
    throw new Error("invalid chat model list");
  return value.items;
}
export async function createChatModel(
  input: NewChatModel,
  csrf: string,
): Promise<ChatModel> {
  const response = await checked(
    await fetch("/api/v1/admin/chat-models", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(input),
    }),
  );
  const value: unknown = await response.json();
  if (!isModel(value)) throw new Error("invalid chat model");
  return value;
}
function isEvidence(v: unknown): v is AnswerEvidence {
  return (
    record(v) &&
    positive(v.n) &&
    typeof v.id === "string" &&
    /^c_[1-9]\d*$/.test(v.id) &&
    [v.document_id, v.version_id, v.section_id].every(positive) &&
    typeof v.title === "string" &&
    typeof v.content === "string" &&
    Array.isArray(v.heading_path) &&
    v.heading_path.every((x) => typeof x === "string") &&
    (v.pages === null ||
      (Array.isArray(v.pages) &&
        v.pages.length === 2 &&
        v.pages.every(positive) &&
        Number(v.pages[0]) <= Number(v.pages[1])))
  );
}
export function decodeAnswerEvent(event: string, v: unknown): AnswerEvent {
  if (!record(v)) throw new Error("invalid answer event");
  if (
    event === "retrieval" &&
    Array.isArray(v.evidence) &&
    v.evidence.length <= 30 &&
    v.evidence.every(isEvidence) &&
    v.evidence.every((e, i) => e.n === i + 1) &&
    Array.isArray(v.degraded) &&
    v.degraded.every((x) => typeof x === "string") &&
    nonnegative(v.retrieval_revision)
  ) {
    return {
      event,
      data: {
        evidence: v.evidence,
        degraded: v.degraded,
        retrieval_revision: v.retrieval_revision,
      },
    };
  }
  if (
    event === "step" &&
    positive(v.step) &&
    typeof v.tool === "string" &&
    typeof v.args_summary === "string" &&
    nonnegative(v.result_count)
  ) {
    return {
      event,
      data: {
        step: v.step,
        tool: v.tool,
        args_summary: v.args_summary,
        result_count: v.result_count,
      },
    };
  }
  if (event === "delta" && typeof v.text === "string" && v.text.length <= 65536)
    return { event, data: { text: v.text } };
  if (
    event === "citations" &&
    nonnegative(v.invalid_count) &&
    Array.isArray(v.items) &&
    v.items.every(
      (x: unknown) =>
        record(x) && positive(x.n) && typeof x.evidence_id === "string",
    )
  ) {
    return {
      event,
      data: {
        invalid_count: v.invalid_count,
        items: v.items as components["schemas"]["Citation"][],
      },
    };
  }
  if (
    event === "done" &&
    typeof v.message_id === "string" &&
    v.message_id.length > 0 &&
    typeof v.prompt_version === "string" &&
    (v.outcome === "answered" ||
      v.outcome === "refused" ||
      v.outcome === "unsupported" ||
      v.outcome === "truncated")
  ) {
    if (
      v.usage !== undefined &&
      (!record(v.usage) ||
        !nonnegative(v.usage.prompt_tokens) ||
        !nonnegative(v.usage.completion_tokens) ||
        !nonnegative(v.usage.total_tokens) ||
        v.usage.total_tokens <
          v.usage.prompt_tokens + v.usage.completion_tokens)
    )
      throw new Error("invalid answer usage");
    return {
      event,
      data: {
        message_id: v.message_id,
        outcome: v.outcome,
        prompt_version: v.prompt_version,
        ...(v.usage === undefined
          ? {}
          : { usage: v.usage as components["schemas"]["ChatUsage"] }),
      },
    };
  }
  if (
    event === "error" &&
    typeof v.code === "string" &&
    typeof v.message === "string" &&
    typeof v.retryable === "boolean"
  )
    return {
      event,
      data: { code: v.code, message: v.message, retryable: v.retryable },
    };
  throw new Error("invalid answer event");
}

export async function streamAnswer(
  input: AnswerRequest,
  csrf: string,
  signal: AbortSignal,
  onEvent: (event: AnswerEvent) => void,
): Promise<void> {
  return streamAnswerAt("/api/v1/answers", input, csrf, signal, onEvent);
}
export async function streamConversation(
  id: number,
  question: string,
  csrf: string,
  signal: AbortSignal,
  onEvent: (event: AnswerEvent) => void,
  mode?: "standard" | "agentic" | "auto",
): Promise<void> {
  if (!positive(id)) throw new Error("invalid conversation");
  return streamAnswerAt(
    `/api/v1/conversations/${id}/messages`,
    mode ? { question, mode } : { question },
    csrf,
    signal,
    onEvent,
  );
}
async function streamAnswerAt(
  path: string,
  input: unknown,
  csrf: string,
  signal: AbortSignal,
  onEvent: (event: AnswerEvent) => void,
): Promise<void> {
  const response = await checked(
    await fetch(path, {
      method: "POST",
      credentials: "same-origin",
      signal,
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(input),
    }),
  );
  if (
    !response.headers.get("Content-Type")?.startsWith("text/event-stream") ||
    !response.body
  )
    throw new Error("invalid answer stream");
  const reader = response.body.getReader(),
    decoder = new TextDecoder("utf-8", { fatal: true });
  let buffer = "",
    eventName = "",
    data = "",
    phase = "start",
    textSize = 0;
  let evidence: AnswerEvidence[] = [];
  const dispatch = () => {
    if (!data) return false;
    const parsed: unknown = JSON.parse(data);
    const event = decodeAnswerEvent(eventName, parsed);
    if (event.event === "step") {
      if (phase !== "start") throw new Error("unexpected step");
    } else if (event.event === "retrieval") {
      if (phase !== "start") throw new Error("unexpected retrieval");
      evidence = event.data.evidence;
      phase = "stream";
    } else if (event.event === "delta") {
      if (phase !== "stream") throw new Error("unexpected delta");
      textSize += event.data.text.length;
      if (textSize > 65536) throw new Error("answer too large");
    } else if (event.event === "citations") {
      if (
        phase !== "stream" ||
        new Set(event.data.items.map((x) => x.n)).size !==
          event.data.items.length ||
        event.data.items.some((x) => evidence[x.n - 1]?.id !== x.evidence_id)
      )
        throw new Error("invalid citations");
      phase = "cited";
    } else if (event.event === "done" && phase !== "cited")
      throw new Error("unexpected done");
    onEvent(event);
    data = "";
    eventName = "";
    return event.event === "done" || event.event === "error";
  };
  try {
    for (;;) {
      const chunk = await reader.read();
      buffer += decoder.decode(chunk.value, { stream: !chunk.done });
      if (buffer.length > 131072) throw new Error("answer event too large");
      for (;;) {
        const end = buffer.indexOf("\n");
        if (end < 0) break;
        const line = buffer.slice(0, end).replace(/\r$/, "");
        buffer = buffer.slice(end + 1);
        if (line === "") {
          if (dispatch()) return;
        } else if (line.startsWith("event:")) eventName = line.slice(6).trim();
        else if (line.startsWith("data:")) {
          data += `${line.slice(5).replace(/^ /, "")}\n`;
          if (data.length > 131072) throw new Error("answer event too large");
        }
      }
      if (chunk.done)
        throw new Error("answer stream ended without terminal event");
    }
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
}

function isConversation(value: unknown): value is Conversation {
  return (
    record(value) &&
    positive(value.id) &&
    positive(value.kb_id) &&
    positive(value.model_id) &&
    typeof value.title === "string" &&
    typeof value.created_at === "string" &&
    Number.isFinite(Date.parse(value.created_at))
  );
}
function isFeedback(value: unknown): value is AnswerFeedback {
  return (
    record(value) &&
    (value.rating === 1 || value.rating === -1) &&
    typeof value.correction === "string"
  );
}
function isSavedMessage(value: unknown): value is SavedMessage {
  if (
    !record(value) ||
    typeof value.id !== "string" ||
    typeof value.question !== "string" ||
    typeof value.content !== "string" ||
    !["running", "complete", "failed", "cancelled"].includes(
      String(value.status),
    ) ||
    !Array.isArray(value.evidence) ||
    !value.evidence.every(isEvidence) ||
    !Array.isArray(value.degraded) ||
    !value.degraded.every((v) => typeof v === "string") ||
    typeof value.created_at !== "string" ||
    !Number.isFinite(Date.parse(value.created_at)) ||
    (value.feedback !== undefined && !isFeedback(value.feedback))
  )
    return false;
  try {
    const cites = decodeAnswerEvent("citations", value.citations);
    if (cites.event !== "citations") return false;
    const evidence = value.evidence;
    if (cites.data.items.some((c) => evidence[c.n - 1]?.id !== c.evidence_id))
      return false;
    if (value.status === "complete") {
      const done = decodeAnswerEvent("done", value.done);
      return done.event === "done" && done.data.message_id === value.id;
    }
    return (
      value.content === "" && evidence.length === 0 && value.done === undefined
    );
  } catch {
    return false;
  }
}
export async function createConversation(
  input: components["schemas"]["NewConversation"],
  csrf: string,
  signal: AbortSignal,
): Promise<Conversation> {
  const response = await checked(
    await fetch("/api/v1/conversations", {
      method: "POST",
      credentials: "same-origin",
      signal,
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(input),
    }),
  );
  const value: unknown = await response.json();
  if (!isConversation(value)) throw new Error("invalid conversation");
  return value;
}
export async function listConversations(
  kb: number,
  cursor: number,
  signal: AbortSignal,
): Promise<ConversationPage> {
  const response = await checked(
    await fetch(`/api/v1/kbs/${kb}/conversations?cursor=${cursor}`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const value: unknown = await response.json();
  if (
    !record(value) ||
    !Array.isArray(value.items) ||
    value.items.length > 20 ||
    !value.items.every(isConversation) ||
    value.items.some((c) => c.kb_id !== kb) ||
    (value.next_cursor !== null && !positive(value.next_cursor))
  )
    throw new Error("invalid conversation list");
  return { items: value.items, next_cursor: value.next_cursor };
}
export async function getConversation(
  id: number,
  signal: AbortSignal,
): Promise<ConversationDetail> {
  const response = await checked(
    await fetch(`/api/v1/conversations/${id}`, {
      credentials: "same-origin",
      signal,
    }),
  );
  const value: unknown = await response.json();
  if (
    !record(value) ||
    !isConversation(value.conversation) ||
    value.conversation.id !== id ||
    !Array.isArray(value.messages) ||
    value.messages.length > 50 ||
    !value.messages.every(isSavedMessage)
  )
    throw new Error("invalid saved conversation");
  return { conversation: value.conversation, messages: value.messages };
}
export async function putAnswerFeedback(
  id: string,
  feedback: AnswerFeedback,
  csrf: string,
): Promise<void> {
  const response = await checked(
    await fetch(`/api/v1/messages/${encodeURIComponent(id)}/feedback`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(feedback),
    }),
  );
  const value: unknown = await response.json();
  if (!record(value) || value.status !== "pending")
    throw new Error("invalid feedback response");
}
