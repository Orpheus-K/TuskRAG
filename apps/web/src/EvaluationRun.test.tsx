import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { getEvalRun, getEvalRunResult } from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";
import { stored } from "../../../tests/eval/ui-fixture";
import { createAppRouter } from "./router";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup({
  run = 0,
  role = "admin",
  archived = false,
  state = "queued",
  failure = false,
} = {}) {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  let status = state,
    lost = failure,
    revoked = false;
  const writes: { body: string; key: string }[] = [];
  const reads: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      reads.push(url);
      let body: unknown;
      if (url === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (url === "/api/health/ready") body = { status: "ok" };
      else if (url === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "知识库",
          role,
          status: archived ? "archived" : "active",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: stored.created_at,
        };
      else if (revoked)
        return new Response(
          JSON.stringify({ error: { code: "forbidden", trace_id: "test" } }),
          { status: 403 },
        );
      else if (options?.method === "POST") {
        const headers = new Headers(options.headers);
        expect(headers.get("x-csrf-token")).toBe("csrf");
        writes.push({
          body: String(options.body),
          key: headers.get("idempotency-key") ?? "",
        });
        if (lost) throw new Error("lost response");
        body = { id: 20, job_id: 30 };
      } else if (url.endsWith("/result"))
        body = { ...stored, source: "executed" };
      else
        body = {
          id: 20,
          job_id: 30,
          set: { set_id: 9, revision: 1 },
          mode: "rrf",
          status,
          attempts: 1,
          max_attempts: 3,
          failure_code: null,
          result_available: status === "succeeded",
          created_at: stored.created_at,
          finished_at: ["queued", "running"].includes(status)
            ? null
            : stored.created_at,
        };
      return new Response(JSON.stringify(body));
    }),
  );
  const client = new QueryClient();
  render(
    <QueryClientProvider client={client}>
      <RouterProvider
        router={createAppRouter(
          createMemoryHistory({
            initialEntries: [`/kb/5/eval/runs?set=9&revision=1&run=${run}`],
          }),
        )}
      />
    </QueryClientProvider>,
  );
  return {
    writes,
    reads,
    client,
    recover: () => {
      lost = false;
    },
    revoke: () => {
      revoked = true;
    },
    state: (v: string) => {
      status = v;
    },
  };
}
it("retries unchanged input and key after a lost creation response", async () => {
  const x = setup({ failure: true });
  fireEvent.click(await screen.findByRole("button", { name: "开始运行" }));
  await screen.findByRole("button", { name: "原样重试运行" });
  expect((screen.getByRole("combobox") as HTMLSelectElement).disabled).toBe(
    true,
  );
  x.recover();
  fireEvent.click(screen.getByRole("button", { name: "原样重试运行" }));
  await screen.findByText("排队中");
  expect(x.writes).toHaveLength(2);
  expect(x.writes[0]).toEqual(x.writes[1]);
  expect(JSON.parse(x.writes[0]?.body ?? "{}")).toEqual({
    set_id: 9,
    revision: 1,
    mode: "rrf",
  });
  expect(x.writes[0]?.key).toBeTruthy();
});
it("polls queued runs, shows executed results and hides them on revoked access", async () => {
  const x = setup({ run: 20 });
  await screen.findByText("排队中");
  x.state("succeeded");
  await screen.findByRole("region", { name: "报告详情" }, { timeout: 4000 });
  expect(screen.getByText(/服务端执行/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "下一页题目" }));
  await screen.findByText(/q21 ·/);
  expect(screen.queryByText(/q1 ·/)).toBeNull();
  x.revoke();
  fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
  await screen.findByText(/内容已隐藏/);
  expect(screen.queryByRole("region", { name: "报告详情" })).toBeNull();
});
it.each(["failed", "dead", "cancelled"])(
  "stops polling terminal state %s and never fetches a result",
  async (state) => {
    const x = setup({ run: 20, state });
    await screen.findByRole("region", { name: "运行状态" });
    await waitFor(() => expect(screen.queryByText("正在读取状态")).toBeNull());
    const query = x.client
      .getQueryCache()
      .find({ queryKey: ["eval-run", 1, 2, 5, 20, "status"] });
    const observer = query?.getObserversCount();
    expect(observer).toBe(1);
    await new Promise((resolve) => setTimeout(resolve, 2200));
    expect(x.reads.filter((v) => v.endsWith("/eval-runs/20"))).toHaveLength(1);
    expect(x.reads.some((v) => v.endsWith("/result"))).toBe(false);
  },
);
it("archived knowledge bases cannot start runs", async () => {
  const x = setup({ archived: true });
  const button = await screen.findByRole("button", { name: "开始运行" });
  expect((button as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(button);
  expect(x.writes).toHaveLength(0);
});
it("non admins cannot read or create runs", async () => {
  const x = setup({ role: "viewer", run: 20 });
  await screen.findByText("当前账号无评测运行权限。");
  expect(x.reads.some((v) => v.includes("eval-runs"))).toBe(false);
});
it("rejects contradictory status and imported results on the executed endpoint", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ ...stored, source: "imported" })),
    ),
  );
  await expect(
    getEvalRunResult(5, 20, new AbortController().signal),
  ).rejects.toThrow("invalid executed");
  await expect(getEvalRun(5, 20, new AbortController().signal)).rejects.toThrow(
    "invalid evaluation run status",
  );
});
