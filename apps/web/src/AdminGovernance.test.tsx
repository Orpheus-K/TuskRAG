import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AdminGovernancePage } from "./AdminGovernance";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}));

describe("AdminGovernancePage", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("renders audit logs, model providers, and tenant egress policy", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = typeof input === "string" ? input : input.toString();
      if (url.startsWith("/api/v1/admin/audit-logs?")) {
        return new Response(
          JSON.stringify({
            items: [
              {
                id: 101,
                tenant_id: 1,
                actor_user_id: 2,
                actor_name: "Alice",
                action: "api_key.create",
                object_type: "api_key",
                object_id: 9,
                result: "allow",
                details: {},
                created_at: "2026-11-13T10:00:00Z",
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/admin/model-providers") {
        return new Response(
          JSON.stringify({
            items: [
              {
                id: 5,
                name: "Local vLLM",
                base_url: "http://127.0.0.1:8000/v1",
                locality: "internal",
                has_credential: false,
                created_at: "2026-11-13T10:00:00Z",
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/admin/model-providers/5/test") {
        return new Response(
          JSON.stringify({
            reachable: true,
            status_code: 200,
            latency_ms: 12,
            message: "HTTP 200 OK",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url === "/api/v1/admin/tenant-egress") {
        return new Response(
          JSON.stringify({ egress_policy_max: "internal_only" }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response("{}", { status: 404 });
    });

    render(
      <AdminGovernancePage
        identity={{ tenant_id: 1, user_id: 2, csrf_token: "csrf-123" }}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText("api_key.create")).toBeTruthy();
    });

    fireEvent.click(screen.getByText("模型供应商"));
    await waitFor(() => {
      expect(screen.getByText("Local vLLM")).toBeTruthy();
    });

    fireEvent.click(screen.getByText("测试连通性"));
    await waitFor(() => {
      expect(screen.getByText(/HTTP 200 OK/)).toBeTruthy();
    });

    fireEvent.click(screen.getByText("租户出域上限"));
    await waitFor(() => {
      const select = screen.getByLabelText(/egress_policy_max/) as HTMLSelectElement;
      expect(select.value).toBe("internal_only");
    });

    expect(fetchMock).toHaveBeenCalled();
  });
});
