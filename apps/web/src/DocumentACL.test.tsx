// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { Identity } from "@tuskrag/api-client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DocumentACLButton, UserGroupsPanel } from "./DocumentACL";

const identity: Identity = {
  tenant_id: 1,
  user_id: 10,
  csrf_token: "csrf-token",
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

describe("UserGroupsPanel & DocumentACLButton", () => {
  it("hides DocumentACLButton for viewer and editor", () => {
    renderWithQuery(
      <DocumentACLButton
        identity={identity}
        kbId={5}
        documentId={9}
        documentTitle="秘密文档"
        role="viewer"
      />,
    );
    expect(screen.queryByText("文档权限")).toBeNull();
  });

  it("creates user group and updates document ACL to restricted", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === "/api/v1/groups" && (!init?.method || init.method === "GET")) {
        return new Response(
          JSON.stringify({
            items: [
              {
                id: 3,
                name: "security",
                source: "manual",
                created_at: "2026-11-13T00:00:00Z",
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/groups" && init?.method === "POST") {
        return new Response(
          JSON.stringify({
            id: 4,
            name: "finance",
            source: "manual",
            created_at: "2026-11-13T00:00:00Z",
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
      if (url.startsWith("/api/v1/groups/") && url.endsWith("/members")) {
        return new Response(JSON.stringify({ items: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url === "/api/v1/kbs/5/documents/9/acl" && (!init?.method || init.method === "GET")) {
        return new Response(
          JSON.stringify({
            document_id: 9,
            acl_mode: "inherit_kb",
            acl_revision: 1,
            subjects: [],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/kbs/5/documents/9/acl" && init?.method === "PUT") {
        return new Response(
          JSON.stringify({
            document_id: 9,
            acl_mode: "restricted",
            acl_revision: 2,
            subjects: [{ type: "group", id: 3 }],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal("fetch", fetchMock);

    renderWithQuery(
      <>
        <UserGroupsPanel identity={identity} />
        <DocumentACLButton
          identity={identity}
          kbId={5}
          documentId={9}
          documentTitle="秘密文档"
          role="admin"
        />
      </>,
    );

    fireEvent.click(screen.getByText("用户组管理"));
    await waitFor(() => {
      expect(screen.getByLabelText("新建用户组名称")).toBeTruthy();
    });
    fireEvent.change(screen.getByLabelText("新建用户组名称"), {
      target: { value: "finance" },
    });
    fireEvent.click(screen.getByText("创建用户组"));

    fireEvent.click(screen.getByText("文档权限"));
    await waitFor(() => {
      expect(screen.getByLabelText("文档 秘密文档 访问模式")).toBeTruthy();
    });
    fireEvent.change(screen.getByLabelText("文档 秘密文档 访问模式"), {
      target: { value: "restricted" },
    });
    fireEvent.click(screen.getByLabelText("security"));
    fireEvent.click(screen.getByText("保存文档权限"));

    await waitFor(() => {
      expect(screen.getByText("文档权限已保存。")).toBeTruthy();
    });
  });
});
