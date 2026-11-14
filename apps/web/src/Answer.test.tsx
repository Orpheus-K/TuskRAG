import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { streamAnswer } from "@tuskrag/api-client";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { createAppRouter } from "./router";

// Browser tests cover chunk loading. Component tests wait for module setup
// before the interaction deadline, including when other suites occupy the CPU.
beforeAll(async () => {
  await import("./DocumentPreview");
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const evidence = {
  n: 1,
  id: "c_10",
  content: "申请期限为五天。",
  document_id: 2,
  version_id: 3,
  section_id: 4,
  title: "申请制度",
  heading_path: ["流程"],
  pages: [2, 3],
};
const event = (name: string, data: unknown) =>
  `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;
const retrieval = event("retrieval", {
  evidence: [evidence],
  degraded: [],
  retrieval_revision: 1,
});
const delta = event("delta", { text: "申请期限为五天 [1]。" });
const citations = event("citations", {
  items: [{ n: 1, evidence_id: "c_10" }],
  invalid_count: 0,
});
const done = event("done", {
  message_id: "message",
  outcome: "answered",
  prompt_version: "grounded-v1",
});
function response(text: string) {
  return new Response(text, {
    headers: { "Content-Type": "text/event-stream" },
  });
}
const conversation = {
  id: 7,
  kb_id: 5,
  model_id: 1,
  title: "申请咨询",
  created_at: "2026-11-07T00:00:00Z",
};
function setup(
  answer: (init?: RequestInit) => Response | Promise<Response>,
  options: { initial?: string; history?: () => unknown } = {},
) {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (url === "/api/v1/me")
      return Response.json({ tenant_id: 1, user_id: 2, csrf_token: "csrf" });
    if (url === "/api/health/ready") return Response.json({ status: "ok" });
    if (url === "/api/v1/chat-models")
      return Response.json({
        items: [{ id: 1, name: "知识助手", model: "chat" }],
      });
    if (url === "/api/v1/kbs/5")
      return Response.json({
        id: 5,
        name: "制度库",
        role: "viewer",
        status: "active",
        embedding_space_id: 1,
        config_revision: 1,
        egress_policy: "deny",
        created_at: "2026-11-07T00:00:00Z",
      });
    if (url.startsWith("/api/v1/kbs/5/conversations"))
      return Response.json({ items: [conversation], next_cursor: null });
    if (url === "/api/v1/conversations" && init?.method === "POST")
      return Response.json(conversation);
    if (url === "/api/v1/conversations/7") {
      const value = options.history?.() ?? { conversation, messages: [] };
      return value instanceof Response ? value : Response.json(value);
    }
    if (url.endsWith("/feedback")) return Response.json({ status: "pending" });
    if (url === "/api/v1/conversations/7/messages") return answer(init);
    if (url.startsWith("/api/v1/documents/2/toc"))
      return Response.json({
        document: { id: 2, title: "申请制度", version_id: 3 },
        sections: [
          {
            id: 4,
            parent_id: null,
            ordinal: 1,
            level: 1,
            heading: "流程",
            heading_path: ["流程"],
            pages: [2, 3],
          },
        ],
        next_cursor: null,
      });
    if (url === "/api/v1/sections/4")
      return Response.json({
        document: { id: 2, title: "申请制度", version_id: 3 },
        section: {
          id: 4,
          parent_id: null,
          ordinal: 1,
          level: 1,
          heading: "流程",
          heading_path: ["流程"],
          pages: [2, 3],
        },
        markdown: "申请期限为五天。",
        token_count: 10,
      });
    throw new Error(`unexpected request ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RouterProvider
        router={createAppRouter(
          createMemoryHistory({
            initialEntries: [options.initial ?? "/kb/5/answer"],
          }),
        )}
      />
    </QueryClientProvider>,
  );
  return fetchMock;
}
async function ask() {
  await screen.findByRole("heading", { name: "制度库 · 问答" });
  fireEvent.change(screen.getByLabelText("你的问题"), {
    target: { value: "申请期限？" },
  });
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "提问" })).toHaveProperty(
      "disabled",
      false,
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "提问" }));
}
it("answers and opens the cited original section", async () => {
  const calls = setup(() => response(retrieval + delta + citations + done));
  await ask();
  await screen.findByText("回答完成");
  fireEvent.click(screen.getByRole("button", { name: "引用 1：申请制度" }));
  await screen.findByRole("heading", { name: "流程" });
  expect(
    calls.mock.calls.find(
      ([url]) => url === "/api/v1/conversations/7/messages",
    )?.[1]?.headers,
  ).toEqual({ "Content-Type": "application/json", "X-CSRF-Token": "csrf" });
});
it("clears partial content and citations after permission changes", async () => {
  setup(() =>
    response(
      retrieval +
        delta +
        event("error", {
          code: "evidence_unavailable",
          message: "权限或原文版本已变化，请重新提问",
          retryable: false,
        }),
    ),
  );
  await ask();
  await screen.findByRole("alert");
  expect(screen.queryByText("申请期限为五天", { exact: false })).toBeNull();
  expect(screen.queryByRole("button", { name: "引用 1：申请制度" })).toBeNull();
});
it("cancels the stream and enables another question", async () => {
  let cancelled = false;
  setup(
    (init) =>
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(retrieval + delta));
            init?.signal?.addEventListener("abort", () => {
              cancelled = true;
              controller.error(new DOMException("aborted", "AbortError"));
            });
          },
        }),
        { headers: { "Content-Type": "text/event-stream" } },
      ),
  );
  await ask();
  await screen.findByText("申请期限为五天 [1]。");
  fireEvent.click(screen.getByRole("button", { name: "停止回答" }));
  await screen.findByText("已停止，未保存不完整回答。");
  expect(cancelled).toBe(true);
  expect(screen.getByRole("button", { name: "提问" })).toHaveProperty(
    "disabled",
    false,
  );
});
it("does not treat an interrupted stream as success", async () => {
  setup(() => response(retrieval + delta));
  await ask();
  await screen.findByRole("alert");
  expect(screen.queryByText("回答完成")).toBeNull();
});

const savedMessage = {
  id: "saved-message-123456789",
  question: "申请材料？",
  content: "申请期限为五天 [1]。",
  status: "complete",
  evidence: [evidence],
  citations: { items: [{ n: 1, evidence_id: "c_10" }], invalid_count: 0 },
  degraded: [],
  done: {
    message_id: "saved-message-123456789",
    outcome: "answered",
    prompt_version: "grounded-v2",
  },
  created_at: "2026-11-07T00:00:00Z",
};
it("reopens saved citations, records feedback and continues the same conversation", async () => {
  const calls = setup(
    () =>
      response(
        retrieval +
          event("delta", { text: "申请可在线办理 [1]。" }) +
          citations +
          done,
      ),
    {
      initial: "/kb/5/answer?conversation=7",
      history: () => ({ conversation, messages: [savedMessage] }),
    },
  );
  await screen.findByText("申请材料？");
  expect(screen.getByLabelText("回答模型")).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "无帮助" }));
  fireEvent.change(screen.getByLabelText("纠错建议（选填）"), {
    target: { value: "请核对五天的依据" },
  });
  fireEvent.click(screen.getByRole("button", { name: "保存反馈" }));
  await screen.findByText("反馈已保存，等待审核。");
  expect(
    calls.mock.calls.find(([url]) => url.endsWith("/feedback"))?.[1]?.body,
  ).toBe(JSON.stringify({ rating: -1, correction: "请核对五天的依据" }));
  await ask();
  await screen.findByText("申请可在线办理", { exact: false });
  expect(
    calls.mock.calls.filter(([url]) => url === "/api/v1/conversations"),
  ).toHaveLength(0);
  expect(
    calls.mock.calls.find(([url]) => url.endsWith("/messages"))?.[1]?.body,
  ).toBe(JSON.stringify({ question: "申请期限？" }));
});
it("removes saved answers and source preview when history authorization fails", async () => {
  let revoked = false;
  setup(() => response(retrieval + delta + citations + done), {
    initial: "/kb/5/answer?conversation=7",
    history: () =>
      revoked
        ? Response.json(
            { error: { code: "evidence_unavailable" } },
            { status: 409 },
          )
        : { conversation, messages: [savedMessage] },
  });
  await screen.findByText("申请材料？");
  fireEvent.click(screen.getByRole("button", { name: "引用 1：申请制度" }));
  await screen.findByRole("heading", { name: "流程" });
  revoked = true;
  fireEvent.click(screen.getByRole("button", { name: "有帮助" }));
  fireEvent.click(screen.getByRole("button", { name: "保存反馈" }));
  await screen.findByText("会话不可用，权限或历史引用可能已变化。");
  expect(screen.queryByText("申请期限为五天", { exact: false })).toBeNull();
  expect(screen.queryByRole("heading", { name: "流程" })).toBeNull();
});
it("decodes UTF-8 and CRLF split at arbitrary byte boundaries", async () => {
  const bytes = new TextEncoder().encode(
    (retrieval + delta + citations + done).replaceAll("\n", "\r\n"),
  );
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            for (const byte of bytes) controller.enqueue(Uint8Array.of(byte));
            controller.close();
          },
        }),
        { headers: { "Content-Type": "text/event-stream" } },
      ),
  );
  const events: string[] = [];
  await streamAnswer(
    { kb_id: 5, model_id: 1, question: "问题" },
    "csrf",
    new AbortController().signal,
    (e) => events.push(e.event),
  );
  expect(events).toEqual(["retrieval", "delta", "citations", "done"]);
});
it("rejects unknown and mismatched citation events", async () => {
  vi.stubGlobal("fetch", async () =>
    response(
      retrieval +
        event("citations", {
          items: [{ n: 1, evidence_id: "c_99" }],
          invalid_count: 0,
        }) +
        done,
    ),
  );
  await expect(
    streamAnswer(
      { kb_id: 5, model_id: 1, question: "问题" },
      "csrf",
      new AbortController().signal,
      () => {},
    ),
  ).rejects.toThrow("invalid citations");
});

it("supports agentic mode and renders retrieval step timeline", async () => {
  let capturedMode: unknown;
  const stepEvent = event("step", {
    step: 1,
    tool: "search_kb",
    args_summary: "query=申请期限",
    result_count: 1,
  });
  setup(async (init) => {
    const body = JSON.parse(String(init?.body ?? "{}")) as { mode?: unknown };
    capturedMode = body.mode;
    return response(stepEvent + retrieval + delta + citations + done);
  });
  fireEvent.keyDown(await screen.findByLabelText("问答模式"), {
    key: "ArrowDown",
  });
  fireEvent.click(
    await screen.findByRole("option", { name: /Agentic 多步推理/ }),
  );
  fireEvent.change(screen.getByLabelText("你的问题"), {
    target: { value: "申请期限是几天？" },
  });
  fireEvent.click(screen.getByRole("button", { name: "提问" }));
  await screen.findByLabelText("Agentic 检索步骤");
  expect(capturedMode).toBe("agentic");
  expect(screen.getByText(/search_kb/)).toBeTruthy();
  expect(screen.getByText(/query=申请期限/)).toBeTruthy();
});
