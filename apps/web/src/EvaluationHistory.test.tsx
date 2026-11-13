import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { listEvalRuns } from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";
import {
  historyItems,
  historyPage,
} from "../../../tests/eval/run-history-fixture";
import { createAppRouter } from "./router";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup({ role = "admin", archived = false, empty = false } = {}) {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  let error = 0;
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      calls.push(url);
      let body: unknown;
      if (url === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (url === "/api/health/ready") body = { status: "ok" };
      else if (url === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "历史验收",
          role,
          status: archived ? "archived" : "active",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: "2026-11-06T00:00:00Z",
        };
      else if (error)
        return new Response(
          JSON.stringify({ error: { code: "error", trace_id: "test" } }),
          { status: error },
        );
      else
        body = empty
          ? { items: [], next_cursor: null }
          : historyPage(
              Number(new URL(url, "http://local").searchParams.get("cursor")),
            );
      return new Response(JSON.stringify(body));
    }),
  );
  const history = createMemoryHistory({
    initialEntries: ["/kb/5/eval/history"],
  });
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={createAppRouter(history)} />
    </QueryClientProvider>,
  );
  return {
    calls,
    history,
    fail: (status: number) => {
      error = status;
    },
  };
}
it("paginates history with URL cursor and exact run links", async () => {
  const x = setup();
  await screen.findByRole("link", { name: "运行 1" });
  expect(screen.getByText("上次失败：permission_revoked")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "下一页运行" }));
  const link = await screen.findByRole("link", {
    name: "运行 11",
  });
  expect(x.history.location.search).toContain("cursor=10");
  expect(link.getAttribute("href")).toContain("run=11");
  expect(link.getAttribute("href")).toContain("revision=2");
  expect(screen.queryByRole("link", { name: "运行 1" })).toBeNull();
  expect(screen.queryByRole("button", { name: "下一页运行" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "返回历史首页" }));
  await screen.findByRole("link", { name: "运行 1" });
});
it.each([403, 503])(
  "hides cached rows after refresh error %s",
  async (status) => {
    const x = setup();
    await screen.findByRole("link", { name: "运行 1" });
    x.fail(status);
    fireEvent.click(screen.getByRole("button", { name: "刷新当前页" }));
    await screen.findByRole("alert");
    expect(screen.queryByRole("list", { name: "运行历史列表" })).toBeNull();
    expect(screen.queryByRole("button", { name: "下一页运行" })).toBeNull();
  },
);
it("allows archived history without creation controls", async () => {
  setup({ archived: true });
  await screen.findByText("知识库已归档，历史仅供查看。");
  await screen.findByRole("link", { name: "运行 1" });
  expect(screen.queryByRole("button", { name: "开始运行" })).toBeNull();
});
it("shows a genuine empty page", async () => {
  setup({ empty: true });
  await screen.findByText("当前页没有可见运行。");
  expect(screen.queryByRole("button", { name: "下一页运行" })).toBeNull();
});
it("does not fetch history for viewers", async () => {
  const x = setup({ role: "viewer" });
  await screen.findByText("当前账号无评测运行权限。");
  expect(x.calls.some((c) => c.includes("eval-runs"))).toBe(false);
});
it("rejects reordered rows, contradictory state and false next cursors", async () => {
  for (const data of [
    { items: [historyItems[1], historyItems[0]], next_cursor: null },
    {
      items: [{ ...historyItems[0], result_available: true }],
      next_cursor: null,
    },
    { items: [historyItems[0]], next_cursor: 1 },
  ]) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify(data))),
    );
    await expect(
      listEvalRuns(5, 0, new AbortController().signal),
    ).rejects.toThrow("invalid evaluation history");
  }
});
