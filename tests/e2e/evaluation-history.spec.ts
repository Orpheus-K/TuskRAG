import { expect, test } from "@playwright/test";
import { historyItems, historyPage } from "../eval/run-history-fixture";
import { stored } from "../eval/ui-fixture";

for (const width of [1280, 375])
  test(`run history at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    let revoked = false;
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url()),
        path = url.pathname;
      let body: unknown;
      if (path === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (path === "/api/health/ready") body = { status: "ok" };
      else if (path === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "运行历史验收",
          role: "admin",
          status: "archived",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: stored.created_at,
        };
      else if (revoked) {
        await route.fulfill({
          status: 403,
          json: { error: { code: "forbidden", trace_id: "test" } },
        });
        return;
      } else if (path.endsWith("/eval-runs"))
        body = historyPage(Number(url.searchParams.get("cursor")));
      else if (path.endsWith("/result"))
        body = { ...stored, source: "executed" };
      else if (path.endsWith("/eval-runs/11")) body = historyItems[10];
      else throw new Error(`unexpected API ${path}`);
      await route.fulfill({ json: body });
    });
    await page.goto("/kb/5/eval/history");
    await expect(
      page.getByRole("link", { name: "运行 1", exact: true }),
    ).toBeVisible();
    await expect(page.getByText("知识库已归档，历史仅供查看。")).toBeVisible();
    await page.getByRole("button", { name: "下一页运行" }).click();
    await expect(page).toHaveURL(/cursor=10/);
    await page.reload();
    await expect(
      page.getByRole("link", { name: "运行 11", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "下一页运行" })).toHaveCount(
      0,
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/run-history-${width}.png`,
      fullPage: true,
    });
    await page.getByRole("link", { name: "运行 11", exact: true }).click();
    await expect(page.getByText(/服务端执行/)).toBeVisible();
    await expect(page).toHaveURL(/run=11/);
    await page.goBack();
    await expect(page).toHaveURL(/cursor=10/);
    await expect(
      page.getByRole("link", { name: "运行 11", exact: true }),
    ).toBeVisible();
    revoked = true;
    await page.getByRole("button", { name: "刷新当前页" }).click();
    await expect(page.getByText(/历史内容已隐藏/)).toBeVisible();
    await expect(page.getByRole("list", { name: "运行历史列表" })).toHaveCount(
      0,
    );
  });
