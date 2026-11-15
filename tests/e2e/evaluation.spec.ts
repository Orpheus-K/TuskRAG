import { expect, test } from "@playwright/test";
import { digest, report, page as reportPage, stored } from "../eval/ui-fixture";

for (const width of [1280, 375]) {
  test(`evaluation import and results at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    let imported = false;
    let revoked = false;
    let posts = 0;
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown;
      if (path === "/api/health/ready") body = { status: "ok" };
      else if (path === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (path === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "评测验收知识库",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          role: "admin",
          status: "active",
          created_at: stored.created_at,
        };
      else if (revoked) {
        await route.fulfill({
          status: 403,
          json: { error: { code: "forbidden", trace_id: "test" } },
        });
        return;
      } else if (path === "/api/v1/kbs/5/eval-reports") {
        if (route.request().method() === "POST") {
          posts++;
          expect(route.request().headers()["x-csrf-token"]).toBe("csrf");
          expect(route.request().postDataJSON()).toEqual(report);
          imported = true;
          body = stored;
        } else body = imported ? reportPage : { items: [], next_cursor: null };
      } else if (path.endsWith(digest)) body = stored;
      else {
        await route.fulfill({
          status: 404,
          json: { error: { code: "not_found", trace_id: "test" } },
        });
        return;
      }
      await route.fulfill({ json: body });
    });
    await page.goto("/kb/5/eval");
    await expect(page.getByText("暂无可见报告。")).toBeVisible();
    await page.getByRole("button", { name: "导入报告", exact: true }).click();
    await page.getByLabel("完整报告 JSON（最大 8 MiB）").setInputFiles({
      name: "broken.json",
      mimeType: "application/json",
      buffer: Buffer.from("{}"),
    });
    await page.getByRole("button", { name: "开始导入", exact: true }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    expect(posts).toBe(0);
    await page.getByLabel("完整报告 JSON（最大 8 MiB）").setInputFiles({
      name: "report.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(report)),
    });
    await page.getByRole("button", { name: "开始导入", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "逐题结果", exact: true }),
    ).toBeVisible();
    expect(posts).toBe(1);
    await page
      .getByRole("button", { name: "q1 · 有答案题 · 存在降级", exact: true })
      .click();
    await expect(
      page
        .locator('[data-state="open"][data-slot="collapsible-content"]')
        .getByText("降级：rerank", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "配置摘要与阶段耗时", exact: true })
      .click();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/evaluation-${width}.png`,
      fullPage: true,
    });
    await page.getByRole("link", { name: "下一页题目" }).click();
    await page.reload();
    await page
      .getByRole("button", { name: "q21 · 无答案题 · 存在降级", exact: true })
      .click();
    await expect(
      page.getByText("无答案题，不计算检索相关性指标。", { exact: true }),
    ).toBeVisible();
    revoked = true;
    await page.getByRole("link", { name: "刷新列表" }).click();
    await expect(
      page.getByText("当前账号无评测报告权限。", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "逐题结果", exact: true }),
    ).toHaveCount(0);
    await expect(page.getByText(/页面验收集/)).toHaveCount(0);
  });
}
