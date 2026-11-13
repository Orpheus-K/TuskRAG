import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { DocumentPreview } from "./DocumentPreview";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function preview(prior: "ok" | "revoked" | "changed") {
  const document = { id: 7, version_id: 9, title: "技术文档" };
  const section = (id: number, ordinal: number) => ({
    id,
    parent_id: null,
    ordinal,
    level: 1,
    heading: "治理",
    heading_path: ["治理"],
    pages: null,
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const route = new URL(String(input), "http://localhost").pathname;
      if (route === "/api/v1/documents/7/toc")
        return new Response(
          JSON.stringify({
            document,
            sections: [section(10, 1), section(11, 2)],
            next_cursor: null,
          }),
        );
      if (route === "/api/v1/sections/11")
        return new Response(
          JSON.stringify({
            document,
            section: section(11, 2),
            markdown: "当前片段正文。",
            token_count: 8,
          }),
        );
      if (route === "/api/v1/sections/10") {
        if (prior === "revoked") return new Response(null, { status: 404 });
        return new Response(
          JSON.stringify({
            document: { ...document, version_id: prior === "changed" ? 8 : 9 },
            section: section(10, 1),
            markdown: "上文中的衔接句。",
            token_count: 8,
          }),
        );
      }
      throw new Error(`unexpected route ${route}`);
    }),
  );
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <DocumentPreview
        identity={{ tenant_id: 1, user_id: 2, csrf_token: "csrf" }}
        target={{
          id: 7,
          versionId: 9,
          title: "技术文档",
          initialSection: 11,
          includePrevious: true,
        }}
        onClose={() => {}}
      />
    </QueryClientProvider>,
  );
}

it("shows the preceding source piece alongside a cited section", async () => {
  preview("ok");
  expect(await screen.findByText("上文中的衔接句。")).toBeTruthy();
  expect(screen.getByText("当前片段正文。")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "上文 · 治理" })).toBeTruthy();
});

for (const failure of ["revoked", "changed"] as const) {
  it(`hides both pieces when the preceding source is ${failure}`, async () => {
    preview(failure);
    expect(
      await screen.findByText("引用上文不可用，文档可能已更新或权限已变化。"),
    ).toBeTruthy();
    expect(screen.queryByText("上文中的衔接句。")).toBeNull();
    expect(screen.queryByText("当前片段正文。")).toBeNull();
  });
}
