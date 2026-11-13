import { expect, test } from "@playwright/test";
import { stored } from "../eval/ui-fixture";

for (const width of [1280, 375])
  test(`evaluation run at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    let attempts = 0,
      reads = 0,
      revoked = false;
    const keys: string[] = [];
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown;
      if (path === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (path === "/api/health/ready") body = { status: "ok" };
      else if (path === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "运行验收",
          role: "admin",
          status: "active",
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
      } else if (route.request().method() === "POST") {
        expect(route.request().postDataJSON()).toEqual({
          set_id: 9,
          revision: 1,
          mode: "rrf",
        });
        expect(route.request().headers()["x-csrf-token"]).toBe("csrf");
        keys.push(route.request().headers()["idempotency-key"] ?? "");
        if (++attempts === 1) {
          await route.abort();
          return;
        }
        body = { id: 20, job_id: 30 };
      } else if (path.endsWith("/result"))
        body = { ...stored, source: "executed" };
      else {
        const status =
          ++reads === 1 ? "queued" : reads === 2 ? "running" : "succeeded";
        body = {
          id: 20,
          job_id: 30,
          set: { set_id: 9, revision: 1 },
          mode: "rrf",
          status,
          attempts: 1,
          max_attempts: 3,
          failure_code: null,
          result_available: status === "succeeded",
          created_at: stored.created_at,
          finished_at: status === "succeeded" ? stored.created_at : null,
        };
      }
      await route.fulfill({ json: body });
    });
    await page.goto("/kb/5/eval/runs?set=9&revision=1");
    await page.getByRole("button", { name: "开始运行" }).click();
    await expect(page.getByRole("combobox")).toBeDisabled();
    await page.getByRole("button", { name: "原样重试运行" }).click();
    expect(keys[0]).toBeTruthy();
    expect(keys[0]).toBe(keys[1]);
    await expect(page).toHaveURL(/run=20/);
    await expect(page.getByText("运行中", { exact: true })).toBeVisible({
      timeout: 6000,
    });
    await expect(page.getByText(/服务端执行/)).toBeVisible({ timeout: 6000 });
    await expect(
      page.getByRole("button", { name: "下一页题目" }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "下一页题目" }).click();
    await expect(page.getByText(/q21 ·/)).toBeVisible();
    await page.reload();
    await expect(page.getByText(/q21 ·/)).toBeVisible();
    expect(attempts).toBe(2);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/evaluation-run-${width}.png`,
      fullPage: true,
    });
    revoked = true;
    await page.getByRole("button", { name: "刷新状态" }).click();
    await expect(page.getByText(/内容已隐藏/)).toBeVisible();
    await expect(page.getByRole("region", { name: "报告详情" })).toHaveCount(0);
  });
