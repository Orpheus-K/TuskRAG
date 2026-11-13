import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { stored } from "../../../tests/eval/ui-fixture";
import { createAppRouter } from "./router";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup({
  job = 0,
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
        if (url.endsWith("/cancel")) {
          status = "cancelled";
          body = { status };
        } else body = record();
      } else body = record();
      return new Response(JSON.stringify(body));
    }),
  );
  function record() {
    return {
      job_id: 30,
      source_space_id: 3,
      source_revision: 1,
      target_space_id: 4,
      status,
      attempts: 1,
      max_attempts: 3,
      failure_code: ["failed", "dead", "succeeded"].includes(status)
        ? "model_unavailable"
        : null,
      created_at: stored.created_at,
      finished_at: ["queued", "running"].includes(status)
        ? null
        : stored.created_at,
    };
  }
  const client = new QueryClient();
  render(
    <QueryClientProvider client={client}>
      <RouterProvider
        router={createAppRouter(
          createMemoryHistory({
            initialEntries: [`/kb/5/rebuild?job=${job}`],
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

it("retries a lost response with identical target revision and key", async () => {
  const x = setup({ failure: true });
  fireEvent.change(await screen.findByLabelText("目标空间编号"), {
    target: { value: "4" },
  });
  fireEvent.click(screen.getByRole("button", { name: "开始重建" }));
  await screen.findByRole("button", { name: "原样重试重建" });
  expect(
    (screen.getByLabelText("目标空间编号") as HTMLInputElement).disabled,
  ).toBe(true);
  x.recover();
  fireEvent.click(screen.getByRole("button", { name: "原样重试重建" }));
  await screen.findByText("排队中");
  expect(x.writes).toHaveLength(2);
  expect(x.writes[0]).toEqual(x.writes[1]);
  expect(JSON.parse(x.writes[0]?.body ?? "{}")).toEqual({
    target_space_id: 4,
    revision: 1,
  });
  expect(x.writes[0]?.key).toBeTruthy();
});
it("rejects invalid or unchanged target before creating", async () => {
  const x = setup();
  const input = await screen.findByLabelText("目标空间编号");
  for (const value of ["0", "3", "-1", "1.5", "9007199254740992", "abc"]) {
    fireEvent.change(input, { target: { value } });
    expect(
      (screen.getByRole("button", { name: "开始重建" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  }
  expect(x.writes).toHaveLength(0);
});
it("polls to successful completion despite a historical failure and hides revoked data", async () => {
  const x = setup({ job: 30 });
  await screen.findByText("排队中");
  x.state("succeeded");
  await screen.findByText(
    "重建完成时已切换至目标空间。",
    {},
    { timeout: 4000 },
  );
  expect(screen.queryByText(/最近一次失败/)).toBeNull();
  expect(screen.queryByRole("button", { name: "取消重建" })).toBeNull();
  x.revoke();
  fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
  await screen.findByText(/内容已隐藏/);
  expect(screen.queryByRole("region", { name: "重建任务" })).toBeNull();
});
it("archived bases disable creation", async () => {
  const x = setup({ archived: true });
  expect(
    (
      (await screen.findByRole("button", {
        name: "开始重建",
      })) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(x.writes).toHaveLength(0);
});
it("archived bases can cancel an existing task and reload its actual status", async () => {
  const x = setup({ archived: true, job: 30 });
  fireEvent.click(await screen.findByRole("button", { name: "取消重建" }));
  await screen.findByText("已取消");
  expect(x.writes).toHaveLength(1);
  expect(x.reads.filter((v) => v.endsWith("/30"))).toHaveLength(2);
});
it("cancellation denial hides evidence and stops polling", async () => {
  const x = setup({ job: 30 });
  await screen.findByText("排队中");
  x.revoke();
  fireEvent.click(screen.getByRole("button", { name: "取消重建" }));
  await screen.findByText(/内容已隐藏/);
  const count = x.reads.length;
  await new Promise((resolve) => setTimeout(resolve, 2200));
  expect(x.reads.length).toBe(count);
});
it("readers never request rebuild details", async () => {
  const x = setup({ job: 30, role: "viewer" });
  await screen.findByText("当前账号无重建权限。");
  expect(x.reads.some((v) => v.includes("rebuilds"))).toBe(false);
});
it.each(["succeeded", "failed", "dead", "cancelled"])(
  "stops polling terminal state %s",
  async (state) => {
    const x = setup({ job: 30, state });
    await screen.findByText(/结束时间/);
    await new Promise((resolve) => setTimeout(resolve, 2200));
    expect(x.reads.filter((v) => v.endsWith("/30"))).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "取消重建" })).toBeNull();
  },
);
it("a status outage hides stale progress until explicit retry", async () => {
  const x = setup({ job: 30 });
  await screen.findByText("排队中");
  vi.mocked(fetch).mockRejectedValueOnce(new Error("offline"));
  fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
  await screen.findByText(/任务状态读取失败/);
  expect(screen.queryByText("排队中")).toBeNull();
  expect(screen.queryByRole("button", { name: "取消重建" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "刷新状态" }));
  await screen.findByText("排队中");
  await waitFor(() =>
    expect(x.reads.filter((v) => v.endsWith("/30"))).toHaveLength(2),
  );
});
