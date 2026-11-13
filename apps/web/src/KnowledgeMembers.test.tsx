// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { Identity, KnowledgeBase } from "@tuskrag/api-client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KnowledgeMembersPanel } from "./KnowledgeMembers";

const identity: Identity = {
  tenant_id: 1,
  user_id: 10,
  csrf_token: "csrf-token",
};

const ownerBase: KnowledgeBase = {
  id: 5,
  name: "治理知识库",
  embedding_space_id: 1,
  config_revision: 1,
  egress_policy: "deny",
  status: "active",
  created_at: "2026-11-13T00:00:00Z",
  role: "owner",
};

function renderWithQuery(ui: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>{ui}</QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("KnowledgeMembersPanel", () => {
  it("hides panel for non-owner roles", () => {
    renderWithQuery(
      <KnowledgeMembersPanel
        identity={identity}
        base={{ ...ownerBase, role: "admin" }}
      />,
    );
    expect(screen.queryByText("成员与出域策略")).toBeNull();
  });

  it("loads members and updates egress policy and membership", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === "/api/v1/kbs/5/members" && (!init?.method || init.method === "GET")) {
        return new Response(
          JSON.stringify({
            items: [
              {
                user_id: 10,
                display_name: "Alice",
                role: "owner",
                created_at: "2026-11-13T00:00:00Z",
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/tenant/users") {
        return new Response(
          JSON.stringify({
            items: [
              { id: 10, display_name: "Alice", created_at: "2026-11-13T00:00:00Z" },
              { id: 11, display_name: "Bob", created_at: "2026-11-13T00:00:00Z" },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/kbs/5/egress-policy" && init?.method === "PUT") {
        return new Response(
          JSON.stringify({
            error: { code: "egress_denied", message: "denied" },
          }),
          { status: 422, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/kbs/5/members/11" && init?.method === "PUT") {
        return new Response(null, { status: 204 });
      }
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal("fetch", fetchMock);

    renderWithQuery(
      <KnowledgeMembersPanel identity={identity} base={ownerBase} />,
    );

    fireEvent.click(screen.getByText("成员与出域策略"));

    await waitFor(() => {
      expect(screen.getByText("Alice")).toBeTruthy();
      expect(screen.getByText("Bob (#11)")).toBeTruthy();
    });

    fireEvent.change(screen.getByLabelText("知识库出域策略"), {
      target: { value: "allow" },
    });
    fireEvent.click(screen.getByText("保存出域策略"));

    await waitFor(() => {
      expect(
        screen.getByText("所选出域策略超出租户允许的上限。"),
      ).toBeTruthy();
    });

    fireEvent.click(screen.getByText("添加成员"));
    await waitFor(() => {
      expect(screen.getByText("成员角色已更新。")).toBeTruthy();
    });
  });
});
