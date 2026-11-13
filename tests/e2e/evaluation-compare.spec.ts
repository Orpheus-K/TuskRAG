import { expect, test } from "@playwright/test";
import {
  comparison,
  comparisonPage,
  currentDigest,
} from "../eval/comparison-ui-fixture";

for (const width of [1280, 375]) {
  test(`report comparison selection and permissions at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 1000 });
    let revoked = false;
    let incompatible = false;
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url());
      let body: unknown;
      if (url.pathname === "/api/health/ready") body = { status: "ok" };
      else if (url.pathname === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (url.pathname === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "对比验收",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          role: "admin",
          status: "active",
          created_at: "2026-11-06T00:00:00Z",
        };
      else if (url.pathname.endsWith("eval-reports")) body = comparisonPage;
      else if (url.pathname.endsWith("eval-report-comparison")) {
        expect(url.searchParams.get("limit")).toBe("20");
        if (revoked || incompatible) {
          await route.fulfill({
            status: revoked ? 403 : 409,
            json: {
              error: {
                code: revoked ? "forbidden" : "reports_incomparable",
                trace_id: "test",
              },
            },
          });
          return;
        }
        body = comparison(
          Number(url.searchParams.get("offset")),
          url.searchParams.get("baseline") === currentDigest,
        );
      } else {
        await route.fulfill({
          status: 404,
          json: { error: { code: "not_found", trace_id: "test" } },
        });
        return;
      }
      await route.fulfill({ json: body });
    });
    await page.goto("/kb/5/eval");
    await page.getByRole("link", { name: "对比两份报告" }).click();
    await expect(
      page.getByText("请选择两份报告后查看差异。", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("listitem")
      .filter({ hasText: "基线配置" })
      .getByRole("button", { name: "设为基线" })
      .click();
    await page
      .getByRole("listitem")
      .filter({ hasText: "当前配置" })
      .getByRole("button", { name: "设为当前" })
      .click();
    await expect(
      page.getByText("未通过回归门禁：存在超过两百分点的下降。", {
        exact: true,
      }),
    ).toBeVisible();
    await page.getByText("q1 · 证据已变化", { exact: true }).click();
    await expect(
      page.locator("details[open]").getByText("-0.1000", { exact: true }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/evaluation-compare-${width}.png`,
      fullPage: true,
    });
    await page.getByRole("button", { name: "交换方向" }).click();
    await expect(
      page.getByText("通过回归门禁：未发现超过两百分点的下降。", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("region", { name: "对比结果" })
        .getByText("+0.1000", { exact: true })
        .first(),
    ).toBeVisible();
    await page.getByRole("button", { name: "下一页差异" }).click();
    await page.reload();
    await page.getByText("q21 · 证据已变化", { exact: true }).click();
    await expect(
      page.getByText("无答案题，指标与差值不适用。", { exact: true }),
    ).toBeVisible();
    incompatible = true;
    await page.getByRole("button", { name: "刷新对比" }).click();
    await expect(page.getByText(/两份报告不可比：/)).toBeVisible();
    await expect(page.getByRole("region", { name: "对比结果" })).toHaveCount(0);
    incompatible = false;
    revoked = true;
    await page.getByRole("button", { name: "刷新对比" }).click();
    await expect(
      page.getByText("知识库、报告或权限已不可用，无法显示对比。请重新加载。", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(page.getByRole("region", { name: "对比结果" })).toHaveCount(0);
    await expect(page.getByText(/基线配置/)).toHaveCount(0);
  });
}
