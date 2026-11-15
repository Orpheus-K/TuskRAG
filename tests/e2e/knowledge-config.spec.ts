import { expect, test } from "@playwright/test";

for (const width of [1280, 375]) {
  test(`knowledge configuration saves and resolves conflicts at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    let revision = 0;
    let reads = 0;
    let conflict = false;
    let settings: unknown;
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path === "/api/v1/kbs/5/config") {
        if (route.request().method() === "PUT") {
          expect(route.request().headers()["if-match"]).toBe(`"${revision}"`);
          expect(route.request().headers()["x-csrf-token"]).toBe("test");
          if (conflict) {
            conflict = false;
            revision++;
            settings = { top_k: 10 };
            await route.fulfill({
              status: 409,
              json: { error: { code: "revision_conflict", trace_id: "test" } },
            });
            return;
          }
          settings = route.request().postDataJSON().retrieval;
          revision++;
        } else reads++;
        await route.fulfill({
          headers: { ETag: `"${revision}"` },
          json: { revision, ...(settings ? { retrieval: settings } : {}) },
        });
        return;
      }
      const bodies: Record<string, unknown> = {
        "/api/health/ready": { status: "ok" },
        "/api/v1/me": { tenant_id: 1, user_id: 2, csrf_token: "test" },
        "/api/v1/kbs/5": {
          id: 5,
          name: "知识库配置验收",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          role: "admin",
          status: "active",
          created_at: "2026-11-06T00:00:00Z",
        },
        "/api/v1/kbs/5/documents": { items: [], next_cursor: null },
      };
      if (path in bodies) await route.fulfill({ json: bodies[path] });
      else
        await route.fulfill({
          status: 403,
          json: { error: { code: "forbidden", trace_id: "test" } },
        });
    });
    await page.goto("/kb/5/documents");
    await page.getByRole("heading", { name: "知识库配置验收" }).waitFor();
    expect(reads).toBe(0);
    if (width < 768)
      await page.getByRole("button", { name: "展开菜单" }).click();
    await page.getByRole("link", { name: "知识库设置" }).click();
    await page.getByRole("heading", { name: "检索默认配置" }).waitFor();
    await page.getByLabel("默认返回条数").fill("5");
    await page.getByLabel("默认启用重排").check();
    await page.getByLabel(/重排提供方编号/).fill("9");
    await page.getByLabel("默认重排模型").fill("rank");
    await page.getByLabel("最低重排分数（可留空）").fill("0");
    await page.getByRole("button", { name: "保存检索配置" }).click();
    await expect(page.getByText("已保存配置，修订 1。")).toBeVisible();
    expect(settings).toEqual({
      top_k: 5,
      reranker: {
        provider_id: 9,
        model: "rank",
        top_n: 30,
        fallback: false,
        min_score: 0,
      },
    });
    conflict = true;
    await page.getByLabel("默认返回条数").fill("7");
    await page.getByRole("button", { name: "保存检索配置" }).click();
    await expect(page.getByText(/配置已被其他人修改/)).toBeVisible();
    await expect(page.getByLabel("默认返回条数")).toHaveValue("7");
    await expect(
      page.getByRole("button", { name: "保存检索配置" }),
    ).toBeDisabled();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/knowledge-config-${width}.png`,
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "载入最新配置（放弃当前修改）" })
      .click();
    await expect(page.getByLabel("默认返回条数")).toHaveValue("10");
    await expect(page.getByLabel("默认启用重排")).not.toBeChecked();
    await page.getByRole("button", { name: "保存检索配置" }).click();
    await expect(page.getByText("已保存配置，修订 3。")).toBeVisible();
    expect(settings).toEqual({ top_k: 10 });
  });
}
