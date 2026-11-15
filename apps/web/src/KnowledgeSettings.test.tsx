import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { KnowledgeSettingsPage } from "./KnowledgeSettings";

const identity = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mount(role: string) {
  const requests: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requests.push(url);
      if (url === "/api/v1/kbs/5")
        return Response.json({
          id: 5,
          name: "员工手册",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          status: "active",
          role,
          created_at: "2026-11-15T00:00:00Z",
        });
      if (url === "/api/v1/kbs/5/config")
        return new Response(JSON.stringify({ revision: 0 }), {
          headers: { ETag: '"0"' },
        });
      return Response.json({ items: [] });
    }),
  );
  render(
    <QueryClientProvider client={new QueryClient()}>
      <KnowledgeSettingsPage identity={identity} id={5} />
    </QueryClientProvider>,
  );
  return requests;
}

it("shows all settings sections to owners without toggles", async () => {
  mount("owner");
  await screen.findByRole("heading", { name: "员工手册 · 设置" });
  expect(screen.getByRole("region", { name: "检索默认配置" })).toBeTruthy();
  expect(screen.getByRole("region", { name: "成员与出域策略" })).toBeTruthy();
  expect(screen.getByRole("region", { name: "用户组管理" })).toBeTruthy();
  await screen.findByText("尚未保存配置，当前默认返回 8 条且不重排。");
});

it("hides owner-only member settings from admins", async () => {
  mount("admin");
  await screen.findByRole("heading", { name: "员工手册 · 设置" });
  expect(screen.queryByRole("region", { name: "成员与出域策略" })).toBeNull();
});

it.each(["editor", "viewer"])("does not load settings for %s", async (role) => {
  const requests = mount(role);
  await screen.findByText("当前账号无知识库设置权限。");
  expect(requests).toEqual(["/api/v1/kbs/5"]);
});
