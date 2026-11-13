import { expect, test } from "@playwright/test";

for (const width of [1280, 375]) {
  test(`retrieval debug remains readable at ${width}px and clears revoked results`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    let revoked = false;
    const requests: unknown[] = [];
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url());
      let body: unknown;
      switch (url.pathname) {
        case "/api/health/ready":
          body = { status: "ok" };
          break;
        case "/api/v1/me":
          body = { tenant_id: 1, user_id: 2, csrf_token: "test" };
          break;
        case "/api/v1/kbs/5":
          body = {
            id: 5,
            name: "检索调试样例",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "viewer",
            created_at: "2026-10-29T00:00:00Z",
          };
          break;
        case "/api/v1/kbs/5/documents":
          body = { items: [], next_cursor: null };
          break;
        case "/api/v1/admin/embedding-spaces":
          body = {
            items: [
              {
                id: 3,
                provider_id: 9,
                provider_name: "本地模型",
                base_url: "http://localhost:9000",
                locality: "internal",
                model: "embed",
                dimensions: 64,
                normalized: true,
                distance: "cosine",
                has_credential: false,
              },
            ],
          };
          break;
        case "/api/v1/search":
          requests.push(route.request().postDataJSON());
          if (revoked) {
            await route.fulfill({
              status: 404,
              json: { error: { code: "not_found", trace_id: "test" } },
            });
            return;
          }
          body = {
            retrieval_revision: 4,
            status: "ok",
            degraded: ["rerank"],
            trace_id: "test",
            evidence: [
              {
                id: "c_11",
                score: 0.03,
                score_kind: "rrf",
                content: "上海住宿每晚限额650元。",
                document: { id: 12, version_id: 13, title: "差旅住宿标准" },
                section: { id: 14, heading_path: ["差旅"] },
                pages: null,
              },
            ],
            trace: {
              retrieval_revision: 4,
              candidates: [
                {
                  ChunkID: 11,
                  DocumentID: 12,
                  VersionID: 13,
                  SectionID: 14,
                  Title: "差旅住宿标准",
                  Content: "上海住宿每晚限额650元。",
                  HeadingPath: ["差旅"],
                  PageStart: null,
                  PageEnd: null,
                  Score: 0.03,
                  RRFRank: 1,
                  LexicalRank: 1,
                  LexicalScore: 2,
                  SemanticRank: 1,
                  SemanticDistance: 0.1,
                  RerankScore: null,
                  RerankRank: 0,
                },
              ],
              timings_ms: { embedding: 15, recall_rrf: 12, recheck: 3 },
              degraded: ["rerank"],
              filters: { DocumentTags: [], MIMEs: [], UpdatedAfter: null },
            },
          };
          break;
        default:
          await route.fulfill({
            status: 404,
            json: { error: { code: "not_found", trace_id: "test" } },
          });
          return;
      }
      await route.fulfill({ json: body });
    });
    await page.goto("/kb/5/documents");
    await page.getByLabel("问题或关键词").fill("上海住宿限额");
    await page.getByRole("button", { name: "检索", exact: true }).click();
    await expect(page.getByText("检索配置修订 4")).toBeVisible();
    expect(requests.at(-1)).toEqual({
      query: "上海住宿限额",
      kb_ids: [5],
      options: { debug: true },
    });
    await page.getByText("重排配置", { exact: true }).click();
    await page.getByLabel("重排方式").selectOption("off");
    await page.getByRole("button", { name: "检索", exact: true }).click();
    await expect.poll(() => requests.length).toBe(2);
    expect(requests.at(-1)).toEqual({
      query: "上海住宿限额",
      kb_ids: [5],
      options: { debug: true, rerank: false },
    });
    await page.getByLabel("重排方式").selectOption("custom");
    await page.getByLabel("模型提供方").selectOption("9");
    await page.getByLabel("重排模型").fill("rank");
    await page.getByRole("button", { name: "检索", exact: true }).click();
    await expect.poll(() => requests.length).toBe(3);
    expect(requests.at(-1)).toEqual({
      query: "上海住宿限额",
      kb_ids: [5],
      options: {
        debug: true,
        rerank: true,
        reranker: { provider_id: 9, model: "rank", top_n: 30, fallback: false },
      },
    });
    await page.getByText("检索调试 · 1 个可见候选").click();
    await expect(
      page.getByRole("columnheader", { name: "BM25 排名 / 分数" }),
    ).toBeVisible();
    await expect(page.getByText("结果未经重排。")).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `test-results/retrieval-debug-${width}.png`,
      fullPage: true,
    });
    revoked = true;
    await page.getByRole("button", { name: "检索", exact: true }).click();
    await expect(page.getByText("知识库不存在或无权访问。")).toBeVisible();
    await expect(page.getByText("上海住宿每晚限额650元。")).toHaveCount(0);
    await expect(page.getByRole("table")).toHaveCount(0);
  });
}
