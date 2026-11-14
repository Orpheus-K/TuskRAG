import { expect, test } from "@playwright/test";

test("workspace navigation, split sizing and mobile layout", async ({
  page,
}, testInfo) => {
  await page.route("**/api/health/ready", (route) =>
    route.fulfill({ json: { status: "ok" } }),
  );
  await page.route("**/api/v1/**", (route) =>
    route.fulfill({
      json: new URL(route.request().url()).pathname.endsWith("/me")
        ? { tenant_id: 1, user_id: 2, csrf_token: "fixture" }
        : { items: [] },
    }),
  );
  await page.setViewportSize({ width: 1600, height: 900 });
  await page.goto("/");
  const sidebar = page.getByLabel("主导航栏");
  const main = page.getByRole("region", { name: "知识库工作区" });
  const models = page.getByRole("region", { name: "模型配置" });
  await expect(sidebar).toBeVisible();
  await expect(page.getByRole("button", { name: "展开菜单" })).toBeHidden();
  await expect(main.getByText("暂无知识库")).toBeVisible();
  await expect(page.getByText("服务已就绪")).toHaveCount(0);
  const bounds = async (el: typeof main) =>
    el.evaluate((node) => {
      const { x, y, right, width } = node.getBoundingClientRect();
      return { x, y, right, width };
    });
  const [nav, list, config] = await Promise.all([
    bounds(sidebar),
    bounds(main),
    bounds(models),
  ]);
  expect(nav.right).toBeLessThanOrEqual(list.x);
  expect(list.right).toBeLessThan(config.x);
  expect(list.y).toEqual(config.y);
  expect(config.right).toBeGreaterThan(1550);
  await page.screenshot({ path: testInfo.outputPath("desktop.png") });
  await main.getByRole("button", { name: "新建知识库" }).click();
  const dialog = page.getByRole("dialog", { name: "新建知识库" });
  await expect(dialog.getByLabel("知识库名称")).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("create-dialog.png") });
  await dialog.getByRole("button", { name: "关闭" }).click();
  await expect(dialog).toHaveCount(0);
  await sidebar.getByRole("button", { name: "账号菜单" }).click();
  await expect(page.getByRole("menuitem", { name: "退出登录" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("account-menu.png") });
  await page.keyboard.press("Escape");
  const handle = await bounds(page.getByLabel("调整模型配置宽度"));
  await page.mouse.move(handle.x + 3, handle.y + 30);
  await page.mouse.down();
  await page.mouse.move(handle.x - 47, handle.y + 30);
  await page.mouse.up();
  await expect
    .poll(async () => (await bounds(models)).width)
    .toBe(config.width + 50);
  await page.reload();
  await expect
    .poll(async () => (await bounds(models)).width)
    .toBe(config.width + 50);
  await page.getByRole("button", { name: "收起模型配置" }).click();
  await expect(models).toHaveCount(0);
  await page.getByRole("button", { name: "展开模型配置" }).click();
  await expect(models).toBeVisible();
  await page.getByRole("button", { name: "折叠侧边栏" }).click();
  await expect(sidebar.getByRole("button", { name: "账号菜单" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("collapsed.png") });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(sidebar).toBeHidden();
  await page.getByRole("button", { name: "展开菜单" }).click();
  await expect(sidebar.getByText("知识库", { exact: true })).toBeVisible();
  await expect(sidebar.getByText("用户 #2")).toBeVisible();
  await page.getByRole("button", { name: "收起菜单" }).click();
  expect((await bounds(models)).y).toBeGreaterThan((await bounds(main)).y);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    390,
  );
  await page.screenshot({ path: testInfo.outputPath("mobile.png") });
});
