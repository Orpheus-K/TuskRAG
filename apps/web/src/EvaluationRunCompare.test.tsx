import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { compareEvalRuns } from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";
import {
  runComparison,
  runComparisonPage,
} from "../../../tests/eval/run-comparison-fixture";
import { createAppRouter } from "./router";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup({ role = "admin", selected = false, error = "" } = {}) {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  let code = error;
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (raw: string) => {
      calls.push(raw);
      const url = new URL(raw, "http://local");
      let body: unknown;
      if (url.pathname === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (url.pathname === "/api/health/ready") body = { status: "ok" };
      else if (url.pathname === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "对比验收",
          role,
          status: "archived",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: "2026-11-06T00:00:00Z",
        };
      else if (url.pathname.endsWith("eval-runs")) body = runComparisonPage;
      else if (code)
        return new Response(
          JSON.stringify({ error: { code, trace_id: "test" } }),
          { status: code === "forbidden" ? 403 : 409 },
        );
      else
        body = runComparison(
          Number(url.searchParams.get("baseline")),
          Number(url.searchParams.get("current")),
          Number(url.searchParams.get("offset")),
        );
      return new Response(JSON.stringify(body));
    }),
  );
  const history = createMemoryHistory({
    initialEntries: [
      `/kb/5/eval/run-compare${selected ? "?baseline=1&current=2" : ""}`,
    ],
  });
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={createAppRouter(history)} />
    </QueryClientProvider>,
  );
  return {
    calls,
    history,
    fail: (v: string) => {
      code = v;
    },
  };
}
it("selects completed runs, swaps direction, paginates and clears", async () => {
  const x = setup();
  const before = await screen.findAllByRole("button", { name: "设为基线" });
  const after = screen.getAllByRole("button", { name: "设为当前" });
  expect((before[2] as HTMLButtonElement).disabled).toBe(true);
  expect((after[2] as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(before[0] as HTMLElement);
  fireEvent.click(after[1] as HTMLElement);
  await screen.findByRole("region", { name: "对比结果" });
  expect(screen.getAllByText("-0.1000").length).toBeGreaterThan(0);
  expect(x.history.location.search).toContain("baseline=1");
  fireEvent.click(screen.getByRole("button", { name: "下一页差异" }));
  await screen.findByText(/q21 ·/);
  expect(screen.queryByText(/q1 ·/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "交换方向" }));
  await screen.findAllByText("+0.1000");
  expect(x.history.location.search).toContain("baseline=2");
  fireEvent.click(screen.getByRole("button", { name: "清空选择" }));
  await screen.findByText("请选择两次已完成运行。");
  expect(screen.queryByRole("region", { name: "对比结果" })).toBeNull();
});
it.each([
  ["reports_incomparable", "两次运行不可比"],
  ["result_unavailable", "所选运行尚无成功结果"],
])("shows %s without stale metrics", async (error, message) => {
  setup({ selected: true, error });
  await screen.findByText(new RegExp(message));
  expect(screen.queryByRole("region", { name: "对比结果" })).toBeNull();
});
it("hides comparison after revocation", async () => {
  const x = setup({ selected: true });
  await screen.findByRole("region", { name: "对比结果" });
  x.fail("forbidden");
  fireEvent.click(screen.getByRole("button", { name: "刷新对比" }));
  await screen.findByText(/对比内容已隐藏/);
  expect(screen.queryByRole("region", { name: "对比结果" })).toBeNull();
});
it("does not query comparisons for ordinary members", async () => {
  const x = setup({ role: "viewer", selected: true });
  await screen.findByText("当前账号无评测运行权限。");
  expect(x.calls.some((v) => v.includes("eval-run"))).toBe(false);
});
it("rejects source or run binding mismatch and corrupt deltas", async () => {
  for (const data of [
    { ...runComparison(), source: "imported" },
    { ...runComparison(), current_run_id: 7 },
    {
      ...runComparison(),
      comparison: {
        ...runComparison().comparison,
        delta: { RecallAt20: 1, HitAt5: 1, MRRAt10: 1, NDCGAt10: 1 },
      },
    },
  ]) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify(data))),
    );
    await expect(
      compareEvalRuns(5, 1, 2, 0, new AbortController().signal),
    ).rejects.toThrow();
  }
});
