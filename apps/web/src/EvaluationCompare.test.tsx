import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { compareEvalReports } from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";
import {
  comparison,
  comparisonPage,
  currentDigest,
} from "../../../tests/eval/comparison-ui-fixture";
import { digest } from "../../../tests/eval/ui-fixture";
import { createAppRouter } from "./router";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup(role = "admin", code = 0) {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const calls: string[] = [];
  let status = code;
  vi.stubGlobal("fetch", async (url: string) => {
    calls.push(url);
    const path = new URL(url, "http://localhost");
    let body: unknown;
    if (path.pathname === "/api/health/ready") body = { status: "ok" };
    else if (path.pathname === "/api/v1/me")
      body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
    else if (path.pathname === "/api/v1/kbs/5")
      body = {
        id: 5,
        name: "对比知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        role,
        status: "active",
        created_at: "2026-11-06T00:00:00Z",
      };
    else if (path.pathname.endsWith("eval-reports")) body = comparisonPage;
    else if (status)
      return new Response(
        JSON.stringify({
          error: {
            code: status === 409 ? "reports_incomparable" : "forbidden",
            trace_id: "test",
          },
        }),
        { status },
      );
    else
      body = comparison(
        Number(path.searchParams.get("offset")),
        path.searchParams.get("baseline") === currentDigest,
      );
    return new Response(JSON.stringify(body));
  });
  const client = new QueryClient();
  const history = createMemoryHistory({
    initialEntries: [
      `/kb/5/eval/compare?baseline=${digest}&current=${currentDigest}`,
    ],
  });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={createAppRouter(history)} />
    </QueryClientProvider>,
  );
  return {
    calls,
    revoke: () => {
      status = 403;
      void client.invalidateQueries({ queryKey: ["eval"] });
    },
  };
}
it("shows signed differences, swaps direction, and preserves no-answer null on later pages", async () => {
  setup();
  await screen.findByText("未通过回归门禁：存在超过两百分点的下降。");
  expect(screen.getAllByText("-0.1000").length).toBeGreaterThan(0);
  fireEvent.click(screen.getByRole("button", { name: "交换方向" }));
  await screen.findByText("通过回归门禁：未发现超过两百分点的下降。");
  expect(screen.getAllByText("+0.1000").length).toBeGreaterThan(0);
  fireEvent.click(screen.getByRole("button", { name: "下一页差异" }));
  fireEvent.click(await screen.findByText("q21 · 证据已变化"));
  expect(screen.getByText("无答案题，指标与差值不适用。")).toBeTruthy();
});
it("keeps selectors usable for incomparable reports and shows no result", async () => {
  setup("admin", 409);
  await screen.findByText(/两份报告不可比/);
  expect(screen.queryByRole("region", { name: "对比结果" })).toBeNull();
  expect(screen.getAllByRole("button", { name: "设为基线" })).toHaveLength(2);
});
it("hides both cached comparisons and selections after revoked refetch", async () => {
  const state = setup();
  await screen.findByText("未通过回归门禁：存在超过两百分点的下降。");
  state.revoke();
  await screen.findByText(/知识库、报告或权限已不可用/);
  expect(screen.queryByRole("region", { name: "对比结果" })).toBeNull();
  expect(screen.queryByText(/基线配置/)).toBeNull();
});
it("does not fetch comparisons for a viewer", async () => {
  const state = setup("viewer");
  await screen.findByText("当前账号无评测报告权限。");
  expect(state.calls.some((url) => url.includes("eval-report"))).toBe(false);
});
it("selects reports without a complete initial pair", async () => {
  setup();
  await screen.findByText("未通过回归门禁：存在超过两百分点的下降。");
  fireEvent.click(screen.getByRole("button", { name: "清空选择" }));
  await screen.findByText("请选择两份报告后查看差异。");
  const before = screen.getByText(/基线配置/).closest("li");
  const after = screen.getByText(/当前配置/).closest("li");
  if (!before || !after) throw new Error("missing report options");
  fireEvent.click(within(before).getByRole("button", { name: "设为基线" }));
  fireEvent.click(within(after).getByRole("button", { name: "设为当前" }));
  await screen.findByText("未通过回归门禁：存在超过两百分点的下降。");
});
it.each(["digest", "delta", "gate", "offset", "row"])(
  "rejects a malformed %s comparison",
  async (kind) => {
    const value = comparison();
    if (kind === "digest") value.current_digest = digest;
    if (kind === "delta") value.delta.MRRAt10 = 0.1;
    if (kind === "gate") value.gate = "passed";
    if (kind === "offset") value.next_offset = 0;
    if (kind === "row") {
      const first = value.rows[0];
      if (!first) throw new Error("missing fixture row");
      first.id = "wrong";
    }
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify(value)));
    await expect(
      compareEvalReports(
        5,
        digest,
        currentDigest,
        0,
        new AbortController().signal,
      ),
    ).rejects.toThrow();
  },
);
