import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

// Opt-in only. This exercises a prepared loopback Linux deployment and real
// configured models; ordinary CI never sends documents to an external model.
test.use({
  baseURL: process.env.TUSKRAG_LIVE_QA_URL ?? "http://127.0.0.1:18082",
});
for (const width of [1280, 375]) {
  test(`live login, cited answer, history and feedback at ${width}px`, async ({
    page,
  }) => {
    test.skip(
      process.env.TUSKRAG_LIVE_QA !== "project-documents",
      "requires prepared real-model preview",
    );
    test.setTimeout(180_000);
    const passwordFile = process.env.TUSKRAG_LIVE_QA_PASSWORD_FILE;
    if (!passwordFile)
      throw new Error("private acceptance password file required");
    const password = (await readFile(passwordFile, "utf8")).trim();
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/");
    await page.getByLabel("租户编号").fill("1");
    await page.getByLabel("账号", { exact: true }).fill("mvp-admin");
    await page.getByLabel("密码", { exact: true }).fill(password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.locator('a[href^="/kb/1/answer"]').click();
    if (process.env.TUSKRAG_LIVE_QA_MODEL_ID)
      await page
        .getByLabel("回答模型")
        .selectOption(process.env.TUSKRAG_LIVE_QA_MODEL_ID);
    if (width === 375) {
      const history = await page
        .getByRole("complementary", { name: "会话历史" })
        .boundingBox();
      expect(history?.height).toBeLessThanOrEqual(300);
    }
    await page
      .getByLabel("你的问题")
      .fill("当前单轮问答的上游模型和整条 HTTP 流的超时各是多少秒？");
    await page.getByRole("button", { name: "提问", exact: true }).click();
    await expect(page.getByText("回答完成", { exact: true })).toBeVisible({
      timeout: 120_000,
    });
    await expect(page).toHaveURL(/conversation=\d+/);
    await page
      .getByRole("button", { name: /^引用 \d+：/ })
      .first()
      .click();
    await expect(
      page.getByRole("region", { name: "文档目录与正文" }),
    ).toBeVisible();
    await expect(page.getByRole("heading", { name: /^上文 ·/ })).toBeVisible({
      timeout: 15_000,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/live-answer-${width}.png`,
      fullPage: true,
    });
    await page.getByRole("button", { name: "关闭预览" }).click();
    await page.reload();
    await expect(page.getByText("回答完成", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "有帮助", exact: true }).click();
    await page.getByRole("button", { name: "保存反馈", exact: true }).click();
    await expect(page.getByText("反馈已保存，等待审核。")).toBeVisible();
    await page.getByLabel("你的问题").fill("那整条 HTTP 流的超时是多少秒？");
    await page.getByRole("button", { name: "提问", exact: true }).click();
    await expect(page.getByText("回答完成", { exact: true })).toHaveCount(2, {
      timeout: 120_000,
    });
    await page.reload();
    await expect(page.getByText("回答完成", { exact: true })).toHaveCount(2);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  });
}
