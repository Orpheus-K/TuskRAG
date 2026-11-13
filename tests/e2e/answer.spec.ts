import { expect, test } from "@playwright/test";

for (const width of [1280, 375]) {
  test(`cited answer, source and refusal at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const base = {
      id: 5,
      name: "员工制度知识库",
      role: "viewer",
      status: "active",
      embedding_space_id: 1,
      config_revision: 1,
      egress_policy: "deny",
      created_at: "2026-11-07T00:00:00Z",
    };
    const section = {
      id: 4,
      parent_id: null,
      ordinal: 1,
      level: 1,
      heading: "差旅住宿标准",
      heading_path: ["员工制度", "差旅住宿标准"],
      pages: [2, 3],
    };
    const document = { id: 2, title: "差旅管理制度", version_id: 3 };
    let count = 0;
    const conversation = {
      id: 7,
      kb_id: 5,
      model_id: 1,
      title: "上海的住宿报销上限是多少？",
      created_at: "2026-11-07T00:00:00Z",
    };
    const messages: Record<string, unknown>[] = [];
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path === "/api/v1/conversations/7" && count === 3) {
        await route.fulfill({
          status: 409,
          json: { error: { code: "evidence_unavailable" } },
        });
        return;
      }
      if (path.endsWith("/feedback")) {
        expect(route.request().postDataJSON()).toEqual({
          rating: 1,
          correction: "原文清楚",
        });
        const first = messages[0];
        if (first) first.feedback = { rating: 1, correction: "原文清楚" };
        await route.fulfill({ json: { status: "pending" } });
        return;
      }
      if (path === "/api/v1/conversations/7/messages") {
        expect(route.request().headers()["x-csrf-token"]).toBe("csrf");
        const frame = (event: string, data: unknown) =>
          `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
        const refused = ++count === 2;
        const evidence = {
          n: 1,
          id: "c_10",
          content: "跨页表格：上海住宿上限为每晚 500 元。",
          document_id: 2,
          version_id: 3,
          section_id: 4,
          title: "差旅管理制度",
          heading_path: section.heading_path,
          pages: [2, 3],
        };
        const text = refused
          ? "知识库中未找到相关依据。"
          : "上海住宿上限为每晚 500 元 [1]。";
        const citations = {
          items: refused ? [] : [{ n: 1, evidence_id: "c_10" }],
          invalid_count: 0,
        };
        const done = {
          message_id: `saved-message-00000${count}`,
          outcome: refused ? "refused" : "answered",
          prompt_version: "grounded-v2",
        };
        if (count !== 3)
          messages.push({
            id: done.message_id,
            question: count === 1 ? conversation.title : "那火星呢？",
            content: text,
            status: "complete",
            evidence: refused ? [] : [evidence],
            citations,
            degraded: [],
            done,
            created_at: "2026-11-07T00:00:00Z",
          });
        await route.fulfill({
          contentType: "text/event-stream",
          body:
            frame("retrieval", {
              evidence: refused ? [] : [evidence],
              degraded: [],
              retrieval_revision: 1,
            }) +
            frame("delta", { text }) +
            (count === 3
              ? frame("error", {
                  code: "evidence_unavailable",
                  message: "权限或原文版本已变化，请重新提问",
                  retryable: false,
                })
              : frame("citations", citations) + frame("done", done)),
        });
        return;
      }
      const bodies: Record<string, unknown> = {
        "/api/health/ready": { status: "ok" },
        "/api/v1/me": { tenant_id: 1, user_id: 2, csrf_token: "csrf" },
        "/api/v1/kbs": { items: [base] },
        "/api/v1/kbs/5": base,
        "/api/v1/conversations": conversation,
        "/api/v1/kbs/5/conversations": {
          items: count ? [conversation] : [],
          next_cursor: null,
        },
        "/api/v1/conversations/7": { conversation, messages },
        "/api/v1/chat-models": {
          items: [{ id: 1, name: "知识助手", model: "chat" }],
        },
        "/api/v1/admin/embedding-spaces": { items: [] },
        "/api/v1/documents/2/toc": {
          document,
          sections: [section],
          next_cursor: null,
        },
        "/api/v1/sections/4": {
          document,
          section,
          markdown: "| 城市 | 每晚上限 |\n| --- | --- |\n| 上海 | 500 元 |",
          token_count: 20,
        },
      };
      await route.fulfill({ json: bodies[path] ?? {} });
    });
    await page.goto("/");
    await page.getByRole("link", { name: "提问", exact: true }).click();
    await page.getByLabel("你的问题").fill("上海的住宿报销上限是多少？");
    await page.getByRole("button", { name: "提问", exact: true }).click();
    await expect(page.getByText("回答完成", { exact: true })).toBeVisible();
    await expect(page).toHaveURL(/conversation=7/);
    await page.reload();
    await expect(page.getByText("回答完成", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "有帮助", exact: true }).click();
    await page.getByLabel("纠错建议（选填）").fill("原文清楚");
    await page.getByRole("button", { name: "保存反馈" }).click();
    await expect(page.getByText("反馈已保存，等待审核。")).toBeVisible();
    await page.getByRole("button", { name: "引用 1：差旅管理制度" }).click();
    await expect(
      page.getByRole("heading", { name: "差旅住宿标准", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("cell", { name: "500 元" })).toBeVisible();
    expect(
      await page.evaluate(
        () => globalThis.document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/conversation-${width}.png`,
      fullPage: true,
    });
    await page.getByLabel("你的问题").fill("那火星呢？");
    await page.getByRole("button", { name: "提问", exact: true }).click();
    await expect(page.getByText("证据不足，无法回答。")).toBeVisible();
    await expect(
      page
        .getByRole("article", { name: "回答" })
        .last()
        .getByRole("button", { name: "引用 1：差旅管理制度" }),
    ).toHaveCount(0);
    await page.getByLabel("你的问题").fill("上海的标准？");
    await page.getByRole("button", { name: "提问", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText(/权限|历史引用/);
    await expect(page.getByLabel("回答", { exact: true })).toHaveCount(0);
  });
}
