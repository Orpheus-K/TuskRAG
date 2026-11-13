import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import {
  getEvalSet,
  isEvalSetDefinition,
  listEvalSets,
  type StoredEvalSet,
} from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";
import { savedSet, setPage } from "../../../tests/eval/set-ui-fixture";
import { createAppRouter } from "./router";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup({
  selected = 9,
  role = "admin",
  status = "active",
  failure = "",
} = {}) {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  let saved = structuredClone(savedSet),
    denied = false,
    error = failure;
  const writes: {
    method: string;
    headers: Record<string, string>;
    body: string;
  }[] = [];
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      calls.push(url);
      let body: unknown;
      if (url === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (url === "/api/health/ready") body = { status: "ok" };
      else if (url === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "知识库",
          role,
          status,
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: savedSet.created_at,
        };
      else {
        if (denied)
          return new Response(
            JSON.stringify({ error: { code: "forbidden", trace_id: "test" } }),
            { status: 403 },
          );
        if (options?.method === "POST" || options?.method === "PUT") {
          writes.push({
            method: options.method,
            headers: options.headers as Record<string, string>,
            body: String(options.body),
          });
          if (error === "network") throw new Error("connection lost");
          if (error)
            return new Response(
              JSON.stringify({ error: { code: error, trace_id: "test" } }),
              { status: 409 },
            );
          saved = {
            ...saved,
            revision: options.method === "POST" ? 1 : saved.revision + 1,
            definition: JSON.parse(
              String(options.body),
            ) as StoredEvalSet["definition"],
          };
          body = saved;
        } else if (url.includes("eval-sets?")) body = setPage;
        else body = saved;
      }
      return new Response(JSON.stringify(body), {
        headers: { ETag: `"${saved.revision}"` },
      });
    }),
  );
  const client = new QueryClient();
  render(
    <QueryClientProvider client={client}>
      <RouterProvider
        router={createAppRouter(
          createMemoryHistory({
            initialEntries: [`/kb/5/eval/sets?set=${selected}`],
          }),
        )}
      />
    </QueryClientProvider>,
  );
  return {
    writes,
    calls,
    setError: (v: string) => {
      error = v;
    },
    revoke: () => {
      denied = true;
      void client.invalidateQueries({ queryKey: ["eval-sets"] });
    },
  };
}
it("edits a case, preserves its target, sends revision and CSRF", async () => {
  const state = setup();
  await screen.findByDisplayValue("申请条件？");
  fireEvent.change(screen.getByLabelText("问题"), {
    target: { value: "修改后的问题？" },
  });
  fireEvent.click(screen.getByRole("button", { name: "保存新修订" }));
  await screen.findByText("已保存题集，修订 2。");
  expect(state.writes[0]?.headers).toMatchObject({
    "If-Match": '"1"',
    "X-CSRF-Token": "csrf",
  });
  expect(JSON.parse(state.writes[0]?.body ?? "{}").cases[0]).toMatchObject({
    question: "修改后的问题？",
    expected: [{ document_id: 3, section_id: 4, page: 2 }],
  });
});
it("retains a conflicted draft until explicit reload", async () => {
  const state = setup({ failure: "revision_conflict" });
  await screen.findByDisplayValue("申请条件？");
  fireEvent.change(screen.getByLabelText("问题"), {
    target: { value: "未保存草稿" },
  });
  fireEvent.click(screen.getByRole("button", { name: "保存新修订" }));
  await screen.findByText(/题集已被修改/);
  expect(screen.getByDisplayValue("未保存草稿")).toBeTruthy();
  expect(
    (screen.getByRole("button", { name: "保存新修订" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  state.setError("");
  fireEvent.click(
    screen.getByRole("button", { name: "载入最新题集（放弃当前草稿）" }),
  );
  await screen.findByDisplayValue("申请条件？");
  expect(screen.queryByDisplayValue("未保存草稿")).toBeNull();
});
it("locks uncertain creation and retries the same content and key", async () => {
  const state = setup({ selected: 0, failure: "network" });
  await screen.findByLabelText("题集名称");
  fireEvent.change(screen.getByLabelText("题集名称"), {
    target: { value: "新题集" },
  });
  fireEvent.change(screen.getByLabelText("题号"), { target: { value: "n1" } });
  fireEvent.change(screen.getByLabelText("问题"), {
    target: { value: "未知问题" },
  });
  fireEvent.click(screen.getByRole("button", { name: "创建题集" }));
  await screen.findByText(/保存结果未确认/);
  state.setError("");
  fireEvent.click(screen.getByRole("button", { name: "原样重试保存" }));
  await screen.findByText("已保存题集，修订 1。");
  expect(state.writes).toHaveLength(2);
  expect(state.writes[0]).toEqual(state.writes[1]);
  expect(state.writes[0]?.headers["Idempotency-Key"]).toBeTruthy();
});
it("allows archived question navigation without edits", async () => {
  setup({ status: "archived" });
  await screen.findByDisplayValue("申请条件？");
  fireEvent.click(screen.getByRole("button", { name: "下一题" }));
  await screen.findByDisplayValue("未记载内容？");
  expect(
    (screen.getByRole("button", { name: "保存新修订" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
});
it("removes definitions and list names when access is revoked", async () => {
  const state = setup();
  await screen.findByDisplayValue("申请条件？");
  state.revoke();
  await screen.findByText("知识库、题集或权限已不可用，内容已隐藏。");
  expect(screen.queryByDisplayValue("申请条件？")).toBeNull();
  expect(screen.queryByText("验收题集")).toBeNull();
});
it.each(["viewer", "editor"])("does not fetch sets for %s", async (role) => {
  const state = setup({ role });
  await screen.findByText("当前账号无题集管理权限。");
  expect(state.calls.some((v) => v.includes("eval-sets"))).toBe(false);
});
it("rejects duplicates and mismatched response identifiers, ETags and cursors", async () => {
  expect(
    isEvalSetDefinition({
      ...savedSet.definition,
      cases: [savedSet.definition.cases?.[0], savedSet.definition.cases?.[0]],
    }),
  ).toBe(false);
  for (const [body, etag] of [
    [{ ...savedSet, id: 10 }, '"1"'],
    [savedSet, '"2"'],
  ] as const) {
    vi.stubGlobal(
      "fetch",
      async () =>
        new Response(JSON.stringify(body), { headers: { ETag: etag } }),
    );
    await expect(
      getEvalSet(5, 9, new AbortController().signal),
    ).rejects.toThrow("invalid evaluation set response");
  }
  vi.stubGlobal(
    "fetch",
    async () => new Response(JSON.stringify({ ...setPage, next_cursor: 1 })),
  );
  await expect(
    listEvalSets(5, 0, new AbortController().signal),
  ).rejects.toThrow("invalid evaluation set cursor");
});
