import { expect, test } from "@playwright/test";
import {
  runComparison,
  runComparisonPage,
} from "../eval/run-comparison-fixture";

for (const width of [1280, 375])
  test(`executed comparison at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    let revoked = false;
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url());
      let body: unknown;
      if (url.pathname === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (url.pathname === "/api/health/ready") body = { status: "ok" };
      else if (url.pathname === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "运行对比验收",
          role: "admin",
          status: "archived",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: "2026-11-06T00:00:00Z",
        };
      else if (revoked) {
        await route.fulfill({
          status: 403,
          json: { error: { code: "forbidden", trace_id: "test" } },
        });
        return;
      } else if (url.pathname.endsWith("eval-runs")) body = runComparisonPage;
      else
        body = runComparison(
          Number(url.searchParams.get("baseline")),
          Number(url.searchParams.get("current")),
          Number(url.searchParams.get("offset")),
        );
      await route.fulfill({ json: body });
    });
    await page.goto("/kb/5/eval/run-compare");
    await expect(
      page.getByRole("button", { name: "设为基线" }).nth(2),
    ).toBeDisabled();
    await page.getByRole("button", { name: "设为基线" }).nth(0).click();
    await page.getByRole("button", { name: "设为当前" }).nth(1).click();
    await expect(page.getByRole("region", { name: "对比结果" })).toBeVisible();
    await expect(
      page.getByText("-0.1000", { exact: true }).first(),
    ).toBeVisible();
    await page.getByRole("button", { name: "下一页差异" }).click();
    await expect(page.getByText(/q21 ·/)).toBeVisible();
    await page.reload();
    await expect(page.getByText(/q21 ·/)).toBeVisible();
    await page.getByRole("button", { name: "交换方向" }).click();
    await expect(
      page.getByText("+0.1000", { exact: true }).first(),
    ).toBeVisible();
    await expect(page).toHaveURL(/baseline=2/);
    await page.getByRole("button", { name: "下一页差异" }).click();
    await page.getByText(/q21 ·/).click();
    await expect(page.getByText("无答案题，指标与差值不适用。")).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/run-compare-${width}.png`,
      fullPage: true,
    });
    revoked = true;
    await page.getByRole("button", { name: "刷新对比" }).click();
    await expect(page.getByText(/对比内容已隐藏/)).toBeVisible();
    await expect(page.getByRole("region", { name: "对比结果" })).toHaveCount(0);
  });
