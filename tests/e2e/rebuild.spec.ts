import { expect, test } from "@playwright/test";

for (const width of [1280, 375]) {
  test(`rebuild lifecycle at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    let attempts = 0,
      reads = 0,
      revoked = false,
      cancelled = false;
    const keys: string[] = [];
    await page.route("**/api/**", async (route) => {
      const request = route.request(),
        path = new URL(request.url()).pathname;
      let body: unknown;
      if (path === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (path === "/api/health/ready") body = { status: "ok" };
      else if (path === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "重建验收知识库",
          role: "admin",
          status: "active",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: "2026-11-07T00:00:00Z",
        };
      else if (revoked) {
        await route.fulfill({
          status: 403,
          json: { error: { code: "forbidden", trace_id: "test" } },
        });
        return;
      } else if (path.endsWith("/cancel")) {
        expect(request.headers()["x-csrf-token"]).toBe("csrf");
        cancelled = true;
        body = { status: "cancelled" };
      } else {
        if (request.method() === "POST") {
          expect(request.postDataJSON()).toEqual({
            target_space_id: 4,
            revision: 1,
          });
          expect(request.headers()["x-csrf-token"]).toBe("csrf");
          keys.push(request.headers()["idempotency-key"] ?? "");
          if (++attempts === 1) {
            await route.abort();
            return;
          }
        }
        const status = cancelled
          ? "cancelled"
          : request.method() === "POST" || ++reads === 1
            ? "queued"
            : "running";
        body = {
          job_id: 30,
          source_space_id: 3,
          target_space_id: 4,
          source_revision: 1,
          status,
          attempts: 1,
          max_attempts: 3,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
          finished_at: cancelled ? "2026-11-07T00:01:00Z" : null,
        };
      }
      await route.fulfill({ json: body });
    });
    await page.goto("/kb/5/rebuild");
    await page.getByLabel("目标空间编号").fill("4");
    await page.getByRole("button", { name: "开始重建" }).click();
    await expect(page.getByLabel("目标空间编号")).toBeDisabled();
    await page.getByRole("button", { name: "原样重试重建" }).click();
    expect(keys[0]).toBeTruthy();
    expect(keys[0]).toBe(keys[1]);
    await expect(page).toHaveURL(/job=30/);
    await expect(page.getByText("运行中", { exact: true })).toBeVisible({
      timeout: 6000,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/rebuild-${width}.png`,
      fullPage: true,
    });
    await page.getByRole("button", { name: "取消重建" }).click();
    await expect(page.getByText("已取消", { exact: true })).toBeVisible();
    await page.reload();
    await expect(page.getByText("已取消", { exact: true })).toBeVisible();
    expect(attempts).toBe(2);
    revoked = true;
    await page.getByRole("button", { name: "刷新状态" }).click();
    await expect(page.getByText(/内容已隐藏/)).toBeVisible();
    await expect(page.getByRole("region", { name: "重建任务" })).toHaveCount(0);
  });
}
