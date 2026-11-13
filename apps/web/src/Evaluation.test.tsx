import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { getEvalReport, listEvalReports } from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";
import { digest, page, stored } from "../../../tests/eval/ui-fixture";
import { createAppRouter } from "./router";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup(role = "admin", status = "active") {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const calls: string[] = [];
  let denied = false;
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
          name: "测试知识库",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          status,
          role,
          created_at: stored.created_at,
        };
      else if (denied)
        return new Response(
          JSON.stringify({ error: { code: "forbidden", trace_id: "test" } }),
          { status: 403 },
        );
      else if (url.includes("eval-reports?")) body = page;
      else if (url.endsWith(digest)) body = stored;
      else throw new Error(url);
      return new Response(JSON.stringify(body));
    }),
  );
  const client = new QueryClient();
  const history = createMemoryHistory({
    initialEntries: [`/kb/5/eval?digest=${digest}`],
  });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={createAppRouter(history)} />
    </QueryClientProvider>,
  );
  return {
    calls,
    client,
    revoke: () => {
      denied = true;
      void client.invalidateQueries({ queryKey: ["eval"] });
    },
  };
}
it("shows report metrics, preserves no-answer null, and paginates questions via URL", async () => {
  setup();
  await screen.findByRole("heading", { name: "逐题结果" });
  expect(screen.queryByText(/q21 ·/)).toBeNull();
  fireEvent.click(screen.getByRole("link", { name: "下一页题目" }));
  const last = await screen.findByText(/q21 · 无答案题/);
  fireEvent.click(last);
  expect(screen.getByText("无答案题，不计算检索相关性指标。")).toBeTruthy();
  expect(screen.getByText("降级：rerank")).toBeTruthy();
});
it("clears report content after a revoked refetch", async () => {
  const state = setup();
  await screen.findByRole("heading", { name: "逐题结果" });
  state.revoke();
  await screen.findByText("当前账号无评测报告权限。");
  expect(screen.queryByRole("heading", { name: "逐题结果" })).toBeNull();
  expect(screen.queryByText(/页面验收集/)).toBeNull();
});
it.each(["viewer", "editor"])(
  "does not request reports for %s",
  async (role) => {
    const { calls } = setup(role);
    await screen.findByText("当前账号无评测报告权限。");
    expect(calls.some((url) => url.includes("eval-reports"))).toBe(false);
  },
);
it("keeps archived reports readable without an import form", async () => {
  setup("admin", "archived");
  await screen.findByRole("heading", { name: "逐题结果" });
  expect(screen.queryByRole("button", { name: "导入报告" })).toBeNull();
});
it("rejects mismatched response digest and a nonadvancing cursor", async () => {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify({ ...stored, digest: "d".repeat(64) })),
  );
  await expect(
    getEvalReport(5, digest, new AbortController().signal),
  ).rejects.toThrow("invalid report response");
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify({ ...page, next_cursor: "d".repeat(64) })),
  );
  await expect(
    listEvalReports(5, "", new AbortController().signal),
  ).rejects.toThrow("invalid report cursor");
});
