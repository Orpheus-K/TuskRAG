import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createAppRouter } from "./router";

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

function mount() {
  const history = createMemoryHistory({
    initialEntries: [window.location.pathname + window.location.search],
  });
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={createAppRouter(history)} />
    </QueryClientProvider>,
  );
}

const unauthenticated = {
  ok: false,
  status: 401,
  json: async () => ({ error: { code: "unauthenticated", trace_id: "trace" } }),
};

it("shows an unavailable dependency and recovers after retry", async () => {
  let checks = 0;
  const fetchMock = vi.fn(async (url: string) => {
    if (url === "/api/v1/me") return unauthenticated;
    checks++;
    return checks === 1
      ? { ok: false, status: 503 }
      : { ok: true, json: async () => ({ status: "ok" }) };
  });
  vi.stubGlobal("fetch", fetchMock);
  mount();

  await screen.findByText("服务不可用");
  fireEvent.click(screen.getByRole("button", { name: "重新检查" }));
  await screen.findByText("服务已就绪");
  expect(checks).toBe(2);
});

it("clears the password after a rejected login", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url === "/api/health/ready") {
        return { ok: true, json: async () => ({ status: "ok" }) };
      }
      return unauthenticated;
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "登录 TuskRAG" });
  fireEvent.change(screen.getByLabelText("租户编号"), {
    target: { value: "1" },
  });
  fireEvent.change(screen.getByLabelText("账号"), {
    target: { value: "admin" },
  });
  fireEvent.change(screen.getByLabelText("密码"), {
    target: { value: "incorrect" },
  });
  fireEvent.click(screen.getByRole("button", { name: "登录" }));
  await screen.findByRole("alert");
  expect(screen.getByLabelText("密码")).toHaveProperty("value", "");
});

it("shows the workspace after login and returns to login after logout", async () => {
  const identity = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url === "/api/health/ready") {
        return { ok: true, json: async () => ({ status: "ok" }) };
      }
      if (url === "/api/v1/me") return unauthenticated;
      if (url === "/api/v1/auth/login") {
        return { ok: true, json: async () => identity };
      }
      if (url === "/api/v1/kbs") {
        return { ok: true, json: async () => ({ items: [] }) };
      }
      if (url === "/api/v1/admin/embedding-spaces") {
        return { ok: true, json: async () => ({ items: [] }) };
      }
      if (url === "/api/v1/auth/logout") return { ok: true, status: 204 };
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "登录 TuskRAG" });
  fireEvent.change(screen.getByLabelText("租户编号"), {
    target: { value: "1" },
  });
  fireEvent.change(screen.getByLabelText("账号"), {
    target: { value: "admin" },
  });
  fireEvent.change(screen.getByLabelText("密码"), {
    target: { value: "password" },
  });
  fireEvent.click(screen.getByRole("button", { name: "登录" }));
  await screen.findByRole("heading", { name: "暂无知识库" });
  fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
  await screen.findByRole("heading", { name: "登录 TuskRAG" });
});

it("reuses the same creation key after a failed request", async () => {
  const identity = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
  const space = {
    id: 3,
    provider_id: 4,
    provider_name: "provider",
    base_url: "http://localhost:9000",
    locality: "internal",
    model: "embed",
    dimensions: 64,
    normalized: true,
    distance: "cosine",
    has_credential: false,
  };
  const keys: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me") return { ok: true, json: async () => identity };
      if (url === "/api/v1/admin/embedding-spaces")
        return { ok: true, json: async () => ({ items: [space] }) };
      if (url === "/api/v1/kbs" && options?.method === "POST") {
        const key = new Headers(options.headers).get("Idempotency-Key");
        if (!key) throw new Error("missing idempotency key");
        keys.push(key);
        if (keys.length === 1) throw new Error("network interrupted");
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "test",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "owner",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      }
      if (url === "/api/v1/kbs")
        return { ok: true, json: async () => ({ items: [] }) };
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("option", { name: "provider · embed · 64 维" });
  fireEvent.change(screen.getByLabelText("知识库名称"), {
    target: { value: "test" },
  });
  fireEvent.change(screen.getByLabelText("嵌入模型", { exact: true }), {
    target: { value: "3" },
  });
  fireEvent.click(screen.getByRole("button", { name: "创建知识库" }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "创建知识库" }));
  await vi.waitFor(() => expect(keys).toHaveLength(2));
  expect(keys[0]).toBe(keys[1]);
});

it("shows a failed latest version without hiding the existing version", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  const base = {
    id: 5,
    name: "员工手册",
    embedding_space_id: 3,
    config_revision: 1,
    egress_policy: "deny",
    status: "active",
    role: "viewer",
    created_at: "2026-10-28T00:00:00Z",
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/kbs/5") return { ok: true, json: async () => base };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: true,
          json: async () => ({
            items: [
              {
                id: 7,
                title: "制度.docx",
                mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                current_version_id: 8,
                latest_version_id: 9,
                latest_job_id: null,
                retryable: false,
                latest_status: "failed",
                byte_size: 12,
                failure_code: "parse_error",
                latest_created_at: "2026-10-28T00:00:00Z",
              },
            ],
            next_cursor: 7,
          }),
        };
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "员工手册" });
  expect(screen.getByRole("heading", { name: "制度.docx" })).toBeTruthy();
  expect(screen.getByText("最新版本：处理失败")).toBeTruthy();
  expect(screen.getByText("已有生效版本")).toBeTruthy();
  expect(screen.getByText("文件解析失败")).toBeTruthy();
  expect(screen.queryByLabelText("上传文档")).toBeNull();
  expect(
    screen.getByRole("link", { name: "下一页" }).getAttribute("href"),
  ).toBe("/kb/5/documents?cursor=7");
});

it("retries an upload with the same key after an interrupted request", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  const keys: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/kbs/5")
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "员工手册",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "editor",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: true,
          json: async () => ({ items: [], next_cursor: null }),
        };
      if (
        url === "/api/v1/kbs/5/documents?confirm_first_row_headers=true" &&
        options?.method === "POST"
      ) {
        const headers = new Headers(options.headers);
        const key = headers.get("Idempotency-Key");
        if (
          !key ||
          headers.get("X-CSRF-Token") !== "csrf" ||
          headers.has("Content-Type")
        )
          throw new Error("invalid upload headers");
        if (
          !(options.body instanceof FormData) ||
          !(options.body.get("file") instanceof File)
        )
          throw new Error("missing upload file");
        keys.push(key);
        if (keys.length === 1) throw new Error("network interrupted");
        return {
          ok: true,
          json: async () => ({
            document_id: 10,
            version_id: 11,
            title: "test.csv",
            mime: "text/csv",
            status: "queued",
            byte_size: 8,
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      }
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "员工手册" });
  expect(screen.getByText(/上传后进入处理队列/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText("上传文档"), {
    target: {
      files: [new File(["a,b\n1,2\n"], "test.csv", { type: "text/csv" })],
    },
  });
  fireEvent.click(screen.getByLabelText(/确认此文件中所有表格的首行都是表头/));
  fireEvent.click(screen.getByRole("button", { name: /^上传$/ }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: /^上传$/ }));
  await screen.findByText("已接收 test.csv，当前状态：等待处理。");
  expect(keys).toHaveLength(2);
  expect(keys[0]).toBe(keys[1]);
});

it("uploads a confirmed new version to the selected document", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  const requests: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/kbs/5")
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "员工手册",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "editor",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: true,
          json: async () => ({
            items: [
              {
                id: 7,
                title: "制度.csv",
                mime: "text/csv",
                current_version_id: 8,
                latest_version_id: 9,
                latest_job_id: null,
                retryable: false,
                latest_status: "failed",
                byte_size: 12,
                failure_code: "table_header_required",
                latest_created_at: "2026-10-28T00:00:00Z",
              },
            ],
            next_cursor: null,
          }),
        };
      if (
        url ===
          "/api/v1/kbs/5/documents/7/versions?confirm_first_row_headers=true" &&
        options?.method === "POST"
      ) {
        const headers = new Headers(options.headers);
        if (
          !headers.get("Idempotency-Key") ||
          headers.get("X-CSRF-Token") !== "csrf" ||
          !(options.body instanceof FormData)
        ) {
          throw new Error("invalid version upload");
        }
        requests.push(url);
        return {
          ok: true,
          json: async () => ({
            document_id: 7,
            version_id: 10,
            title: "制度.csv",
            mime: "text/csv",
            status: "queued",
            byte_size: 8,
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      }
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "制度.csv" });
  fireEvent.click(screen.getByRole("button", { name: "上传新版本" }));
  expect(screen.getByText(/处理期间继续使用当前生效版本/)).toBeTruthy();
  const input = screen.getByLabelText("上传 制度.csv 的新版本");
  fireEvent.change(input, {
    target: { files: [new File(["a,b\n1,2\n"], "revision.csv")] },
  });
  fireEvent.click(screen.getByLabelText(/确认此文件中所有表格的首行都是表头/));
  const form = input.closest("form");
  if (!form) throw new Error("missing version form");
  fireEvent.submit(form);
  await screen.findByText("已接收 制度.csv，当前状态：等待处理。");
  expect(requests).toHaveLength(1);
});

it("confirms deletion and removes displayed evidence", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  vi.spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValueOnce(true);
  let deleted = false;
  let deleteCalls = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/kbs/5")
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "员工手册",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "editor",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: true,
          json: async () => ({
            items: deleted
              ? []
              : [
                  {
                    id: 7,
                    title: "制度.csv",
                    mime: "text/csv",
                    current_version_id: 8,
                    latest_version_id: 8,
                    latest_job_id: null,
                    retryable: false,
                    latest_status: "ready",
                    byte_size: 12,
                    failure_code: null,
                    latest_created_at: "2026-10-28T00:00:00Z",
                  },
                ],
            next_cursor: null,
          }),
        };
      if (url === "/api/v1/search" && options?.method === "POST")
        return {
          ok: true,
          json: async () => ({
            status: "ok",
            retrieval_revision: 0,
            degraded: ["rerank"],
            trace_id: "trace",
            evidence: [
              {
                id: "c_1",
                score: 0.1,
                score_kind: "rrf",
                content: "可检索的制度内容",
                document: { id: 7, title: "制度.csv", version_id: 8 },
                section: { id: 1, heading_path: ["制度"] },
                pages: [1, 1],
              },
            ],
          }),
        };
      if (url === "/api/v1/kbs/5/documents/7" && options?.method === "DELETE") {
        if (new Headers(options.headers).get("X-CSRF-Token") !== "csrf")
          throw new Error("missing CSRF");
        deleteCalls++;
        deleted = true;
        return {
          ok: true,
          status: 202,
          json: async () => ({ status: "deleting" }),
        };
      }
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "制度.csv" });
  fireEvent.change(screen.getByLabelText("问题或关键词"), {
    target: { value: "制度" },
  });
  fireEvent.click(screen.getByRole("button", { name: "检索" }));
  await screen.findByText("可检索的制度内容");
  fireEvent.click(screen.getByRole("button", { name: "删除文档" }));
  expect(deleteCalls).toBe(0);
  fireEvent.click(screen.getByRole("button", { name: "删除文档" }));
  await screen.findByText("已移除 制度.csv，后台正在清理原文件。");
  expect(deleteCalls).toBe(1);
  expect(screen.queryByText("可检索的制度内容")).toBeNull();
});

it("does not render a knowledge base title when document access is revoked", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/kbs/5")
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "不可见标题",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "viewer",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: false,
          status: 404,
          json: async () => ({
            error: { code: "not_found", trace_id: "trace" },
          }),
        };
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("alert");
  expect(screen.queryByText("不可见标题")).toBeNull();
  expect(screen.getByText("知识库不存在或无权访问。")).toBeTruthy();
});

it("searches with explicit rerank degradation and hides results after access is revoked", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  let searches = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/kbs/5")
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "员工手册",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "viewer",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: true,
          json: async () => ({ items: [], next_cursor: null }),
        };
      if (url === "/api/v1/search") {
        expect(options?.method).toBe("POST");
        expect(new Headers(options?.headers).get("X-CSRF-Token")).toBe("csrf");
        expect(JSON.parse(String(options?.body))).toEqual({
          query: "申请",
          kb_ids: [5],
          options: { debug: true },
        });
        searches++;
        if (searches > 1)
          return {
            ok: false,
            status: 404,
            json: async () => ({
              error: { code: "not_found", trace_id: "trace" },
            }),
          };
        return {
          ok: true,
          json: async () => ({
            status: "ok",
            retrieval_revision: 0,
            degraded: ["rerank"],
            trace_id: "trace",
            evidence: [
              {
                id: "c_11",
                score: 0.03,
                score_kind: "rrf",
                content: "申请。",
                document: { id: 12, version_id: 13, title: "差旅制度" },
                section: { id: 14, heading_path: ["制度", "差旅"] },
                pages: [1, 2],
              },
            ],
          }),
        };
      }
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "员工手册" });
  fireEvent.change(screen.getByLabelText("问题或关键词"), {
    target: { value: "申请" },
  });
  fireEvent.click(screen.getByRole("button", { name: "检索" }));
  await screen.findByRole("heading", { name: "差旅制度" });
  expect(screen.getByText("结果未经重排。")).toBeTruthy();
  expect(screen.getByText("申请。")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "检索" }));
  await screen.findByText("知识库不存在或无权访问。");
  expect(screen.queryByText("申请。")).toBeNull();
  expect(screen.queryByText("差旅制度")).toBeNull();
  expect(screen.queryByText("员工手册")).toBeNull();
});

it("distinguishes no matches from a model failure", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  let searches = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/kbs/5")
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "员工手册",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "viewer",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: true,
          json: async () => ({ items: [], next_cursor: null }),
        };
      if (url === "/api/v1/search") {
        searches++;
        return searches === 1
          ? {
              ok: true,
              json: async () => ({
                status: "no_match",
                retrieval_revision: 0,
                degraded: ["rerank"],
                evidence: [],
                trace_id: "trace",
              }),
            }
          : {
              ok: false,
              status: 503,
              json: async () => ({
                error: { code: "model_unavailable", trace_id: "trace" },
              }),
            };
      }
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "员工手册" });
  fireEvent.change(screen.getByLabelText("问题或关键词"), {
    target: { value: "申请" },
  });
  fireEvent.click(screen.getByRole("button", { name: "检索" }));
  await screen.findByText("未找到匹配内容。");
  fireEvent.click(screen.getByRole("button", { name: "检索" }));
  await screen.findByText("嵌入模型暂不可用，请稍后重试。");
  expect(screen.queryByText("未找到匹配内容。")).toBeNull();
});

it("shows purge jobs and retries dead purge jobs for editors", async () => {
  window.history.replaceState(null, "", "/kb/5/documents");
  let retried = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
      const url = String(input);
      if (url === "/api/health/ready")
        return { ok: true, json: async () => ({ status: "ok" }) };
      if (url === "/api/v1/me")
        return {
          ok: true,
          json: async () => ({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        };
      if (url === "/api/v1/embedding-spaces")
        return { ok: true, json: async () => ({ items: [] }) };
      if (url === "/api/v1/kbs/5")
        return {
          ok: true,
          json: async () => ({
            id: 5,
            name: "员工手册",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "editor",
            created_at: "2026-10-28T00:00:00Z",
          }),
        };
      if (url === "/api/v1/kbs/5/documents?cursor=0")
        return {
          ok: true,
          json: async () => ({ items: [], next_cursor: null }),
        };
      if (url === "/api/v1/kbs/5/purge-jobs?cursor=0")
        return {
          ok: true,
          json: async () => ({
            items: [
              {
                id: retried ? 19 : 18,
                kb_id: 5,
                document_id: 7,
                version_id: 9,
                document_title: "过期制度.pdf",
                document_deleted: true,
                status: retried ? "queued" : "dead",
                attempts: retried ? 0 : 3,
                max_attempts: 3,
                failure_code: retried ? null : "storage_unavailable",
                retryable: !retried,
                created_at: "2026-11-13T00:00:00Z",
                finished_at: retried ? null : "2026-11-13T00:01:00Z",
              },
            ],
            next_cursor: null,
          }),
        };
      if (url === "/api/v1/jobs/18:retry" && options?.method === "POST") {
        retried = true;
        return {
          ok: true,
          json: async () => ({ job_id: 19, status: "queued" }),
        };
      }
      throw new Error(`unexpected request: ${url}`);
    }),
  );
  mount();
  await screen.findByRole("heading", { name: "清理任务" });
  await screen.findByText("过期制度.pdf");
  expect(screen.getByText("已删除")).toBeTruthy();
  expect(screen.getByText("状态：等待人工重试（尝试 3/3）")).toBeTruthy();
  expect(screen.getByText("失败原因：文件存储暂不可用")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "重试清理" }));
  await screen.findByText("已重新排队清理 过期制度.pdf，等待后台处理。");
  await screen.findByText("状态：等待清理（尝试 0/3）");
  expect(screen.queryByRole("button", { name: "重试清理" })).toBeNull();
});
