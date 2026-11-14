import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

test.use({
  baseURL: process.env.TUSKRAG_LIVE_QA_URL ?? "http://127.0.0.1:18082",
});
for (const width of [1280, 375]) {
  test(`real PDF answer links both physical pages at ${width}px`, async ({
    page,
  }) => {
    test.skip(
      process.env.TUSKRAG_LIVE_QA !== "project-documents",
      "requires prepared synthetic PDF with real models",
    );
    test.setTimeout(180_000);
    const passwordFile = process.env.TUSKRAG_LIVE_QA_PASSWORD_FILE;
    const model = process.env.TUSKRAG_LIVE_QA_MODEL_ID;
    if (!passwordFile || !model)
      throw new Error("prepared preview credentials and model required");
    const password = (await readFile(passwordFile, "utf8")).trim();
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/");
    await page.getByLabel("租户编号").fill("1");
    await page.getByLabel("账号", { exact: true }).fill("mvp-admin");
    await page.getByLabel("密码", { exact: true }).fill(password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page
      .getByRole("listitem")
      .filter({
        has: page.getByRole("link", { name: "跨页引用验收", exact: true }),
      })
      .getByRole("link", { name: "提问", exact: true })
      .click();
    await page.getByLabel("回答模型").click();
    await page.locator(`[role="option"][data-value="${model}"]`).click();
    await page
      .getByLabel("你的问题")
      .fill("这张跨页住宿限额表中，北京和杭州每人每晚最高分别报销多少元？");
    await page.getByRole("button", { name: "提问", exact: true }).click();
    await expect(page.getByText("回答完成", { exact: true })).toBeVisible({
      timeout: 120_000,
    });
    const answer = page.getByRole("article", { name: "回答", exact: true });
    await expect(answer).toContainText("600");
    await expect(answer).toContainText("380");
    for (const [number, city, amount] of [
      [1, "北京", "600"],
      [2, "杭州", "380"],
    ] as const) {
      await page
        .getByRole("list", { name: "引用来源" })
        .getByRole("button", { name: new RegExp(`第 ${number}—${number} 页`) })
        .first()
        .click();
      const preview = page.getByRole("region", { name: "文档目录与正文" });
      await expect(
        preview.getByRole("cell", { name: city, exact: true }),
      ).toBeVisible();
      await expect(
        preview.getByRole("cell", { name: amount, exact: true }),
      ).toBeVisible();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      await page.screenshot({
        path: `test-results/cross-page-${width}-${number}.png`,
        fullPage: true,
      });
      await page.getByRole("button", { name: "关闭预览" }).click();
    }
    await page.reload();
    await expect(page.getByText("回答完成", { exact: true })).toBeVisible();
    for (const number of [1, 2])
      await expect(
        page
          .getByRole("list", { name: "引用来源" })
          .getByRole("button", {
            name: new RegExp(`第 ${number}—${number} 页`),
          })
          .first(),
      ).toBeVisible();
  });
}
