import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { getKnowledgeConfig, type KnowledgeBase } from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";
import { KnowledgeConfigPanel } from "./KnowledgeConfig";

const base: KnowledgeBase = {
  id: 5,
  name: "知识库",
  embedding_space_id: 3,
  config_revision: 1,
  egress_policy: "deny",
  role: "admin",
  status: "active",
  created_at: "2026-11-06T00:00:00Z",
};
const identity = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
function response(body: unknown, revision: number) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json", ETag: `"${revision}"` },
  });
}
function failure(status: number, code: string) {
  return new Response(JSON.stringify({ error: { code, trace_id: "trace" } }), {
    status,
  });
}
function mount(value = base) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <KnowledgeConfigPanel identity={identity} base={value} />
    </QueryClientProvider>,
  );
}
function open() {
  // The panel is rendered expanded on the settings page.
  expect(screen.getByRole("heading", { name: "检索默认配置" })).toBeTruthy();
}
function change(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("saves all settings with the returned ETag, including zero threshold, and can disable rerank", async () => {
  let revision = 0;
  const requests: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      expect(url).toBe("/api/v1/kbs/5/config");
      if (options?.method !== "PUT") return response({ revision: 0 }, 0);
      const headers = new Headers(options.headers);
      expect(headers.get("If-Match")).toBe(`"${revision}"`);
      expect(headers.get("X-CSRF-Token")).toBe("csrf");
      const body = JSON.parse(String(options.body));
      requests.push(body);
      revision++;
      return response({ revision, retrieval: body.retrieval }, revision);
    }),
  );
  mount();
  open();
  await screen.findByText("尚未保存配置，当前默认返回 8 条且不重排。");
  expect(screen.getByLabelText("默认返回条数")).toHaveProperty("value", "8");
  fireEvent.click(screen.getByLabelText("默认启用重排"));
  change("默认返回条数", "5");
  change(/重排提供方编号/, "9");
  change("默认重排模型", "rank");
  fireEvent.keyDown(screen.getByLabelText("重排候选数"), { key: "ArrowDown" });
  fireEvent.click(await screen.findByRole("option", { name: "20" }));
  change("最低重排分数（可留空）", "0");
  fireEvent.click(
    screen.getByLabelText("重排服务暂不可用时允许返回未重排结果"),
  );
  fireEvent.click(screen.getByRole("button", { name: "保存检索配置" }));
  await screen.findByText("已保存配置，修订 1。");
  expect(requests[0]).toEqual({
    retrieval: {
      top_k: 5,
      reranker: {
        provider_id: 9,
        model: "rank",
        top_n: 20,
        fallback: true,
        min_score: 0,
      },
    },
  });
  fireEvent.click(screen.getByLabelText("默认启用重排"));
  fireEvent.click(screen.getByRole("button", { name: "保存检索配置" }));
  await screen.findByText("已保存配置，修订 2。");
  expect(requests[1]).toEqual({ retrieval: { top_k: 5 } });
});

it("keeps drafts on conflict and failed reload; only explicit reload accepts a new revision", async () => {
  let reads = 0,
    writes = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, options?: RequestInit) => {
      if (options?.method === "PUT") {
        writes++;
        if (writes === 1) return failure(409, "revision_conflict");
        expect(new Headers(options.headers).get("If-Match")).toBe('"2"');
        return response({ revision: 3, retrieval: { top_k: 10 } }, 3);
      }
      reads++;
      if (reads === 2) return failure(503, "dependency_unavailable");
      return reads === 1
        ? response({ revision: 1, retrieval: { top_k: 8 } }, 1)
        : response({ revision: 2, retrieval: { top_k: 10 } }, 2);
    }),
  );
  mount();
  open();
  await screen.findByText("当前修订 1");
  change("默认返回条数", "5");
  fireEvent.click(screen.getByRole("button", { name: "保存检索配置" }));
  await screen.findByText(/配置已被其他人修改/);
  expect(reads).toBe(1);
  expect(screen.getByLabelText("默认返回条数")).toHaveProperty("value", "5");
  expect(screen.getByRole("button", { name: "保存检索配置" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "载入最新配置（放弃当前修改）" }),
  );
  await screen.findByText("重新加载失败，当前输入已保留。");
  expect(screen.getByLabelText("默认返回条数")).toHaveProperty("value", "5");
  fireEvent.click(
    screen.getByRole("button", { name: "载入最新配置（放弃当前修改）" }),
  );
  await screen.findByText("当前修订 2");
  await waitFor(() =>
    expect(screen.getByLabelText("默认返回条数")).toHaveProperty("value", "10"),
  );
  fireEvent.click(screen.getByRole("button", { name: "保存检索配置" }));
  await screen.findByText("已保存配置，修订 3。");
  expect(writes).toBe(2);
});

it.each([
  [401, "unauthenticated"],
  [403, "forbidden"],
  [404, "not_found"],
])("removes config fields after access is lost (%s)", async (status, code) => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, options?: RequestInit) =>
      options?.method === "PUT"
        ? failure(Number(status), String(code))
        : response({ revision: 1, retrieval: { top_k: 8 } }, 1),
    ),
  );
  mount();
  open();
  await screen.findByText("当前修订 1");
  fireEvent.click(screen.getByRole("button", { name: "保存检索配置" }));
  await screen.findByRole("alert");
  expect(screen.queryByLabelText("默认返回条数")).toBeNull();
});

it("keeps an invalid provider draft editable without calling platform-admin APIs", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      expect(url).toBe("/api/v1/kbs/5/config");
      return options?.method === "PUT"
        ? failure(404, "provider_not_found")
        : response(
            {
              revision: 1,
              retrieval: {
                top_k: 8,
                reranker: { provider_id: 9, model: "rank", top_n: 30 },
              },
            },
            1,
          );
    }),
  );
  mount();
  open();
  await screen.findByText("当前修订 1");
  change(/重排提供方编号/, "99");
  fireEvent.click(screen.getByRole("button", { name: "保存检索配置" }));
  await screen.findByText(/模型提供方编号不存在/);
  expect(screen.getByLabelText(/重排提供方编号/)).toHaveProperty("value", "99");
  expect(screen.getByRole("button", { name: "保存检索配置" })).toHaveProperty(
    "disabled",
    false,
  );
});

it.each(["viewer", "editor"])(
  "does not fetch or offer settings to %s",
  (role) => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    mount({ ...base, role });
    expect(screen.queryByRole("heading", { name: "检索默认配置" })).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  },
);

it("shows archived settings read-only", async () => {
  const fetch = vi.fn(async () =>
    response({ revision: 1, retrieval: { top_k: 8 } }, 1),
  );
  vi.stubGlobal("fetch", fetch);
  mount({ ...base, status: "archived" });
  open();
  await screen.findByText("知识库已归档，配置仅供查看。");
  expect(
    screen.getByLabelText("默认返回条数").closest("fieldset"),
  ).toHaveProperty("disabled", true);
  fireEvent.submit(
    screen
      .getByRole("button", { name: "保存检索配置" })
      .closest("form") as HTMLFormElement,
  );
  expect(fetch).toHaveBeenCalledTimes(1);
});

it("rejects mismatched ETags and invalid saved configurations", async () => {
  for (const [body, etag] of [
    [{ revision: 2, retrieval: { top_k: 8 } }, '"1"'],
    [{ revision: 1 }, '"1"'],
    [{ revision: 0, retrieval: { top_k: 8 } }, '"0"'],
    [{ revision: 1, retrieval: { top_k: 31 } }, '"1"'],
    [
      {
        revision: 1,
        retrieval: {
          top_k: 8,
          reranker: { provider_id: 2, model: "m", top_n: 21 },
        },
      },
      '"1"',
    ],
  ]) {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify(body), {
            headers: { ETag: String(etag) },
          }),
      ),
    );
    await expect(
      getKnowledgeConfig(5, new AbortController().signal),
    ).rejects.toThrow();
  }
});
