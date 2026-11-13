// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { Identity } from "@tuskrag/api-client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { APIKeysPage } from "./APIKeys";

const identity: Identity = {
  tenant_id: 1,
  user_id: 10,
  csrf_token: "csrf-token",
};

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const root = createRootRoute();
  const keysRoute = createRoute({
    getParentRoute: () => root,
    path: "/api-keys",
    component: () => <APIKeysPage identity={identity} />,
  });
  const homeRoute = createRoute({
    getParentRoute: () => root,
    path: "/",
    component: () => <div>home</div>,
  });
  const router = createRouter({
    routeTree: root.addChildren([keysRoute, homeRoute]),
    history: createMemoryHistory({ initialEntries: ["/api-keys"] }),
  });
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("APIKeysPage", () => {
  it("lists, creates, and displays one-time plaintext API key", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === "/api/v1/api-keys" && (!init?.method || init.method === "GET")) {
        return new Response(
          JSON.stringify({
            items: [
              {
                id: 1,
                name: "existing-bot",
                prefix: "AbCd1234",
                kb_ids: [],
                actions: ["search"],
                ip_allowlist: [],
                rate_limit_rpm: 60,
                monthly_token_quota: 10000,
                month_tokens_used: 250,
                created_at: "2026-11-13T00:00:00Z",
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/kbs") {
        return new Response(JSON.stringify({ items: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url === "/api/v1/api-keys" && init?.method === "POST") {
        return new Response(
          JSON.stringify({
            key: {
              id: 2,
              name: "agent-prod",
              prefix: "ZxYv9876",
              kb_ids: [],
              actions: ["search", "read", "answer"],
              ip_allowlist: [],
              rate_limit_rpm: 60,
              monthly_token_quota: 0,
              month_tokens_used: 0,
              created_at: "2026-11-13T00:00:00Z",
            },
            plaintext: "tsk_ZxYv9876_0123456789abcdef0123456789abcdef",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    await waitFor(() => {
      expect(screen.getByText("existing-bot")).toBeTruthy();
    });

    fireEvent.change(screen.getByLabelText("密钥名称"), {
      target: { value: "agent-prod" },
    });
    fireEvent.click(screen.getByText("生成 API Key"));

    await waitFor(() => {
      expect(
        screen.getByText("tsk_ZxYv9876_0123456789abcdef0123456789abcdef"),
      ).toBeTruthy();
      expect(screen.getByText(/Agent 接入（MCP Streamable HTTP）/)).toBeTruthy();
      expect(screen.getByText(/"mcpServers"/)).toBeTruthy();
    });
  });
});
