import { expect, test } from "@playwright/test";
import type { StoredEvalSet } from "../../packages/api-client/src/index";
import { savedSet } from "../eval/set-ui-fixture";

for (const width of [1280, 375]) {
  test(`evaluation set editor at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    let saved: StoredEvalSet = structuredClone(savedSet),
      conflict = true,
      revoked = false;
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      let body: unknown;
      if (path === "/api/v1/me")
        body = { tenant_id: 1, user_id: 2, csrf_token: "csrf" };
      else if (path === "/api/health/ready") body = { status: "ok" };
      else if (path === "/api/v1/kbs/5")
        body = {
          id: 5,
          name: "题集验收",
          role: "admin",
          status: "active",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          created_at: saved.created_at,
        };
      else if (revoked) {
        await route.fulfill({
          status: 403,
          json: { error: { code: "forbidden", trace_id: "test" } },
        });
        return;
      } else if (
        route.request().method() === "PUT" ||
        route.request().method() === "POST"
      ) {
        expect(route.request().headers()["x-csrf-token"]).toBe("csrf");
        if (route.request().method() === "PUT") {
          expect(route.request().headers()["if-match"]).toBe('"1"');
          if (conflict) {
            await route.fulfill({
              status: 409,
              json: { error: { code: "revision_conflict", trace_id: "test" } },
            });
            return;
          }
        } else
          expect(route.request().headers()["idempotency-key"]).toBeTruthy();
        const definition: StoredEvalSet["definition"] = route
          .request()
          .postDataJSON();
        if (route.request().method() === "POST")
          expect(definition.cases?.[0]?.expected).toEqual([
            { document_id: 3, section_id: 4, page: 2 },
          ]);
        saved = {
          ...saved,
          id: route.request().method() === "POST" ? 10 : 9,
          revision: route.request().method() === "POST" ? 1 : 2,
          definition,
        };
        body = saved;
      } else if (path.endsWith("eval-sets"))
        body = {
          items: [
            {
              id: saved.id,
              revision: saved.revision,
              digest: saved.digest,
              name: saved.definition.name,
              case_count: saved.definition.cases?.length,
              created_at: saved.created_at,
            },
          ],
          next_cursor: null,
        };
      else body = saved;
      await route.fulfill({
        json: body,
        headers: { ETag: `"${saved.revision}"` },
      });
    });
    await page.goto("/kb/5/eval/sets?set=9");
    await expect(
      page.getByRole("textbox", { name: "问题", exact: true }),
    ).toHaveValue("申请条件？");
    await page
      .getByRole("textbox", { name: "问题", exact: true })
      .fill("草稿问题？");
    await page.getByRole("button", { name: "保存新修订" }).click();
    await expect(page.getByText(/题集已被修改/)).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "问题", exact: true }),
    ).toHaveValue("草稿问题？");
    await expect(
      page.getByRole("button", { name: "保存新修订" }),
    ).toBeDisabled();
    conflict = false;
    await page
      .getByRole("button", { name: "载入最新题集（放弃当前草稿）" })
      .click();
    await expect(
      page.getByRole("textbox", { name: "问题", exact: true }),
    ).toHaveValue("申请条件？");
    await page.getByRole("button", { name: "下一题", exact: true }).click();
    await expect(page.getByLabel("应有答案")).not.toBeChecked();
    await page.getByRole("button", { name: "上一题", exact: true }).click();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/evaluation-sets-${width}.png`,
      fullPage: true,
    });
    await page
      .getByRole("textbox", { name: "问题", exact: true })
      .fill("确认问题？");
    await page.getByRole("button", { name: "保存新修订" }).click();
    await expect(page.getByText("已保存题集，修订 2。")).toBeVisible();
    await page
      .getByRole("button", { name: "新建题集（放弃当前草稿）" })
      .click();
    await page.getByLabel("题集名称").fill("新建验收");
    await page.getByLabel("题号", { exact: true }).fill("new-q1");
    await page
      .getByRole("textbox", { name: "问题", exact: true })
      .fill("申请条件？");
    await page.getByLabel("应有答案").check();
    await page.getByRole("button", { name: "添加期望位置" }).click();
    await page.getByLabel("文档编号", { exact: true }).fill("3");
    await page.getByLabel("节编号", { exact: true }).fill("4");
    await page.getByLabel("页码", { exact: true }).fill("2");
    await page.getByRole("button", { name: "创建题集", exact: true }).click();
    await expect(page.getByText("已保存题集，修订 1。")).toBeVisible();
    await expect(page).toHaveURL(/set=10/);
    await page.reload();
    await expect(page.getByLabel("题集名称")).toHaveValue("新建验收");
    revoked = true;
    await page.getByRole("button", { name: "刷新列表" }).click();
    await expect(
      page.getByText("知识库、题集或权限已不可用，内容已隐藏。"),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "问题", exact: true }),
    ).toHaveCount(0);
  });
}
