import { expect, test } from "@playwright/test";

test("serves the console on a deep route and preserves API 404s", async ({
  page,
  request,
}) => {
  await page.goto("/kb/42/documents");
  await expect(page.getByText("服务已就绪", { exact: true })).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "登录 TuskRAG" }),
  ).toBeVisible();
  const response = await request.get("/api/v1/missing");
  expect(response.status()).toBe(404);
});

test("configures a model, creates a knowledge base, restores a session, and signs out", async ({
  page,
}) => {
  const tenantId = process.env.TUSKRAG_E2E_TENANT_ID;
  const password = process.env.TUSKRAG_E2E_PASSWORD;
  if (!tenantId || !password)
    throw new Error("E2E bootstrap credentials are required");
  await page.goto("/");
  await page.getByLabel("租户编号").fill(tenantId);
  await page.getByLabel("账号").fill("admin");
  await page.getByLabel("密码").fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "知识库", exact: true }),
  ).toBeVisible();
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 1000000)}`;
  const providerName = `browser-provider-${suffix}`;
  const knowledgeName = `browser-knowledge-${suffix}`;
  await page.getByRole("button", { name: "配置模型" }).click();
  await page.getByLabel("提供方名称").fill(providerName);
  await page.getByLabel("提供方基础地址").fill("http://127.0.0.1:9000");
  await page.getByLabel("嵌入模型名称").fill("test-embedding");
  await page.getByLabel("向量维度").fill("64");
  await page.getByRole("button", { name: "保存模型配置" }).click();
  await expect(
    page.getByRole("listitem").filter({ hasText: providerName }),
  ).toBeVisible();
  await page.getByLabel("知识库名称").fill(knowledgeName);
  await page
    .getByLabel("嵌入模型", { exact: true })
    .selectOption({ label: `${providerName} · test-embedding · 64 维` });
  await page.getByRole("button", { name: "创建知识库", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: knowledgeName }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "知识库", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: knowledgeName }),
  ).toBeVisible();
  await page.getByRole("link", { name: knowledgeName }).click();
  await expect(
    page.getByRole("heading", { name: knowledgeName }),
  ).toBeVisible();
  await expect(page.getByText("此知识库暂无文档。")).toBeVisible();
  await page.reload();
  await expect(page.getByText("此知识库暂无文档。")).toBeVisible();
  await page.setViewportSize({ width: 375, height: 800 });
  await page.getByLabel("上传文档").setInputFiles({
    name: "sample.csv",
    mimeType: "text/csv",
    buffer: Buffer.from("name,value\nexample,1\n"),
  });
  await page.getByRole("button", { name: "上传", exact: true }).click();
  await expect(
    page.getByText("已接收 sample.csv，当前状态：等待处理。"),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "sample.csv" })).toBeVisible();
  await expect(page.getByText("尚无生效版本")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    375,
  );
  await page.getByRole("link", { name: "返回知识库" }).click();
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(
    page.getByRole("heading", { name: "登录 TuskRAG" }),
  ).toBeVisible();
});

test("document status remains readable on desktop and mobile", async ({
  page,
}, testInfo) => {
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "viewer",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  await page.route(/\/api\/v1\/kbs\/5\/documents\?.*$/, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 7,
            title: "员工差旅报销制度与住宿标准说明.docx",
            mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            current_version_id: 8,
            latest_version_id: 9,
            latest_job_id: null,
            retryable: false,
            latest_status: "failed",
            byte_size: 100,
            failure_code: "parse_error",
            latest_created_at: "2026-10-28T00:00:00Z",
          },
        ],
        next_cursor: 7,
      },
    }),
  );
  for (const width of [1280, 375]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/kb/5/documents");
    await expect(page.getByText("已有生效版本")).toBeVisible();
    await expect(page.getByText("文件解析失败")).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);
    await page.screenshot({
      path: testInfo.outputPath(`document-${width}.png`),
      fullPage: true,
    });
  }
});

test("uploads a document revision on mobile", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.route("**/api/health/ready", (route) =>
    route.fulfill({ json: { status: "ok" } }),
  );
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "editor",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  await page.route(/\/api\/v1\/kbs\/5\/documents\?cursor=0$/, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 7,
            title: "员工差旅报销制度.csv",
            mime: "text/csv",
            current_version_id: 8,
            latest_version_id: 9,
            latest_job_id: null,
            retryable: false,
            latest_status: "failed",
            byte_size: 10,
            failure_code: "table_header_required",
            latest_created_at: "2026-10-28T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    }),
  );
  let received = false;
  await page.route(
    /\/api\/v1\/kbs\/5\/documents\/7\/versions\?confirm_first_row_headers=true$/,
    (route) => {
      received = route.request().method() === "POST";
      return route.fulfill({
        json: {
          document_id: 7,
          version_id: 10,
          title: "员工差旅报销制度.csv",
          mime: "text/csv",
          status: "queued",
          byte_size: 10,
          created_at: "2026-10-28T00:00:00Z",
        },
      });
    },
  );
  await page.goto("/kb/5/documents");
  await page.getByRole("button", { name: "上传新版本" }).click();
  const input = page.getByLabel("上传 员工差旅报销制度.csv 的新版本");
  await input.setInputFiles({
    name: "修订.csv",
    mimeType: "text/csv",
    buffer: Buffer.from("a,b\n1,2\n"),
  });
  await page.getByLabel(/确认此文件中所有表格的首行都是表头/).check();
  await page.screenshot({
    path: testInfo.outputPath("revision-mobile.png"),
    fullPage: true,
  });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    375,
  );
  await page
    .locator("form")
    .filter({ has: input })
    .getByRole("button", { name: "上传新版本" })
    .click();
  await expect(
    page.getByText("已接收 员工差旅报销制度.csv，当前状态：等待处理。"),
  ).toBeVisible();
  expect(received).toBe(true);
});

test("confirms document deletion on mobile", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.route("**/api/health/ready", (route) =>
    route.fulfill({ json: { status: "ok" } }),
  );
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "editor",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  let deleted = false;
  await page.route(/\/api\/v1\/kbs\/5\/documents\?cursor=0$/, (route) =>
    route.fulfill({
      json: {
        items: deleted
          ? []
          : [
              {
                id: 7,
                title: "制度.csv",
                mime: "text/csv",
                current_version_id: 8,
                latest_version_id: 8,
                latest_job_id: null,
                retryable: false,
                latest_status: "ready",
                byte_size: 10,
                failure_code: null,
                latest_created_at: "2026-10-28T00:00:00Z",
              },
            ],
        next_cursor: null,
      },
    }),
  );
  await page.route("**/api/v1/kbs/5/documents/7", (route) => {
    if (route.request().method() !== "DELETE") return route.fallback();
    deleted = true;
    return route.fulfill({ status: 202, json: { status: "deleting" } });
  });
  page.once("dialog", (dialog) => dialog.accept());
  await page.goto("/kb/5/documents");
  await page.getByRole("button", { name: "删除文档" }).click();
  await expect(
    page.getByText("已移除 制度.csv，后台正在清理原文件。"),
  ).toBeVisible();
  await expect(page.getByText("此知识库暂无文档。")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    375,
  );
});

test("polls active documents and stops after the version becomes terminal", async ({
  page,
}) => {
  await page.clock.install();
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "viewer",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  let requests = 0;
  await page.route(/\/api\/v1\/kbs\/5\/documents\?.*$/, (route) => {
    requests++;
    const active = requests === 1;
    return route.fulfill({
      json: {
        items: [
          {
            id: 7,
            title: "制度.csv",
            mime: "text/csv",
            current_version_id: null,
            latest_version_id: 9,
            latest_job_id: null,
            retryable: false,
            latest_status: active ? "queued" : "failed",
            byte_size: 100,
            failure_code: active ? null : "parse_error",
            latest_created_at: "2026-10-28T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    });
  });
  await page.goto("/kb/5/documents");
  await expect(page.getByText("最新版本：等待处理")).toBeVisible();
  await page.clock.runFor(3100);
  await expect(page.getByText("最新版本：处理失败")).toBeVisible();
  expect(requests).toBe(2);
  await page.clock.runFor(10000);
  expect(requests).toBe(2);
});

test("hides a document and stops polling after access is revoked", async ({
  page,
}) => {
  await page.clock.install();
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "viewer",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  let requests = 0;
  await page.route(/\/api\/v1\/kbs\/5\/documents\?.*$/, (route) => {
    requests++;
    if (requests > 1) {
      return route.fulfill({
        status: 404,
        json: { error: { code: "not_found", trace_id: "test" } },
      });
    }
    return route.fulfill({
      json: {
        items: [
          {
            id: 7,
            title: "不可见文档.csv",
            mime: "text/csv",
            current_version_id: null,
            latest_version_id: 9,
            latest_job_id: null,
            retryable: false,
            latest_status: "queued",
            byte_size: 100,
            failure_code: null,
            latest_created_at: "2026-10-28T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    });
  });
  await page.goto("/kb/5/documents");
  await expect(page.getByText("不可见文档.csv")).toBeVisible();
  await page.clock.runFor(3100);
  await expect(page.getByText("知识库不存在或无权访问。")).toBeVisible();
  await expect(page.getByText("不可见文档.csv")).toHaveCount(0);
  await page.clock.runFor(10000);
  expect(requests).toBe(2);
});

test("search results stay readable on desktop and mobile", async ({
  page,
}, testInfo) => {
  await page.route("**/api/health/ready", (route) =>
    route.fulfill({ json: { status: "ok" } }),
  );
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "viewer",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  await page.route(/\/api\/v1\/kbs\/5\/documents\?.*$/, (route) =>
    route.fulfill({ json: { items: [], next_cursor: null } }),
  );
  await page.route("**/api/v1/search", (route) =>
    route.fulfill({
      json: {
        status: "ok",
        degraded: ["rerank"],
        trace_id: "test",
        evidence: [
          {
            id: "c_11",
            score: 0.03,
            score_kind: "rrf",
            content: "员工出差前应提交申请，经部门负责人批准后预订住宿。",
            document: {
              id: 12,
              version_id: 13,
              title: "差旅管理制度与住宿标准",
            },
            section: { id: 14, heading_path: ["差旅制度", "住宿申请"] },
            pages: [2, 3],
          },
        ],
      },
    }),
  );
  const section = {
    id: 14,
    parent_id: null,
    ordinal: 1,
    level: 2,
    heading: "住宿申请",
    heading_path: ["差旅制度", "住宿申请"],
    pages: [2, 3],
  };
  await page.route(
    /\/api\/v1\/documents\/12\/toc\?cursor=0&limit=30$/,
    (route) =>
      route.fulfill({
        json: {
          document: { id: 12, version_id: 13, title: "差旅管理制度与住宿标准" },
          sections: [section],
          next_cursor: null,
        },
      }),
  );
  await page.route("**/api/v1/sections/14", (route) =>
    route.fulfill({
      json: {
        document: { id: 12, version_id: 13, title: "差旅管理制度与住宿标准" },
        section,
        markdown: "出差前应提交住宿申请。",
        token_count: 10,
      },
    }),
  );
  for (const width of [1280, 375]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/kb/5/documents");
    await page.getByLabel("问题或关键词").fill("住宿如何申请");
    await page.getByRole("button", { name: "检索", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "差旅管理制度与住宿标准" }),
    ).toBeVisible();
    await expect(page.getByText("结果未经重排。")).toBeVisible();
    await page.getByRole("button", { name: "查看章节" }).click();
    await expect(
      page.getByLabel("文档目录与正文").locator("article"),
    ).toContainText("出差前应提交住宿申请。");
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);
    await page.screenshot({
      path: testInfo.outputPath(`search-${width}.png`),
      fullPage: true,
    });
  }
});

for (const sample of [
  { name: "制度.csv", mime: "text/csv", data: "a,b\n1,2\n" },
  { name: "制度.pdf", mime: "application/pdf", data: "%PDF-1.7\n" },
]) {
  test(`confirms ${sample.mime} table headers when uploading from a narrow screen`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 375, height: 800 });
    await page.route("**/api/health/ready", (route) =>
      route.fulfill({ json: { status: "ok" } }),
    );
    await page.route("**/api/v1/me", (route) =>
      route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
    );
    await page.route("**/api/v1/kbs/5", (route) =>
      route.fulfill({
        json: {
          id: 5,
          name: "员工知识库",
          embedding_space_id: 3,
          config_revision: 1,
          egress_policy: "deny",
          status: "active",
          role: "editor",
          created_at: "2026-10-28T00:00:00Z",
        },
      }),
    );
    await page.route(/\/api\/v1\/kbs\/5\/documents\?cursor=0$/, (route) =>
      route.fulfill({ json: { items: [], next_cursor: null } }),
    );
    let confirmed = false;
    await page.route(
      /\/api\/v1\/kbs\/5\/documents\?confirm_first_row_headers=true$/,
      (route) => {
        confirmed = route.request().method() === "POST";
        return route.fulfill({
          json: {
            document_id: 10,
            version_id: 11,
            title: sample.name,
            mime: sample.mime,
            status: "queued",
            byte_size: 10,
            created_at: "2026-10-28T00:00:00Z",
          },
        });
      },
    );
    await page.goto("/kb/5/documents");
    await page.getByLabel("上传文档").setInputFiles({
      name: sample.name,
      mimeType: sample.mime,
      buffer: Buffer.from(sample.data),
    });
    await page.getByLabel(/确认此文件中所有表格的首行都是表头/).check();
    await page.getByRole("button", { name: "上传", exact: true }).click();
    await expect(
      page.getByText(`已接收 ${sample.name}，当前状态：等待处理。`),
    ).toBeVisible();
    expect(confirmed).toBe(true);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(375);
  });
}

test("retries a recoverable document job on mobile", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.route("**/api/health/ready", (route) =>
    route.fulfill({ json: { status: "ok" } }),
  );
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "editor",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  let retried = false;
  await page.route(/\/api\/v1\/kbs\/5\/documents\?cursor=0$/, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 7,
            title: "制度.pdf",
            mime: "application/pdf",
            current_version_id: null,
            latest_version_id: 9,
            latest_job_id: retried ? 15 : 14,
            latest_status: retried ? "queued" : "failed",
            retryable: !retried,
            failure_code: retried ? null : "model_unavailable",
            byte_size: 100,
            latest_created_at: "2026-10-28T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    }),
  );
  await page.route("**/api/v1/jobs/14:retry", (route) => {
    expect(route.request().method()).toBe("POST");
    expect(route.request().headers()["x-csrf-token"]).toBe("test");
    retried = true;
    return route.fulfill({
      status: 202,
      json: { job_id: 15, status: "queued" },
    });
  });
  await page.goto("/kb/5/documents");
  await page.getByRole("button", { name: "重新处理" }).click();
  await expect(
    page.getByText("已重新排队 制度.pdf，等待后台处理。"),
  ).toBeVisible();
  await expect(page.getByText("最新版本：等待处理")).toBeVisible();
  await expect(page.getByRole("button", { name: "重新处理" })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    375,
  );
});

test("opens a current document section without rendering source HTML", async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.route("**/api/health/ready", (route) =>
    route.fulfill({ json: { status: "ok" } }),
  );
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "viewer",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  await page.route(/\/api\/v1\/kbs\/5\/documents\?cursor=0$/, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 7,
            title: "制度.pdf",
            mime: "application/pdf",
            current_version_id: 8,
            latest_version_id: 8,
            latest_job_id: null,
            retryable: false,
            latest_status: "ready",
            failure_code: null,
            byte_size: 100,
            latest_created_at: "2026-10-28T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    }),
  );
  const section = {
    id: 9,
    parent_id: null,
    ordinal: 1,
    level: 1,
    heading: "管理办法",
    heading_path: ["管理办法"],
    pages: [1, 2],
  };
  let version = 8;
  await page.route(
    /\/api\/v1\/documents\/7\/toc\?cursor=0&limit=30$/,
    (route) =>
      route.fulfill({
        json: {
          document: { id: 7, version_id: version, title: "制度.pdf" },
          sections: [section],
          next_cursor: null,
        },
      }),
  );
  const markdown =
    "| 城市 | 住宿上限 |\n| --- | --- |\n| 北京 | 600 |\n\n<script>window.__tuskragXss = true</script>\n\n[危险链接](javascript:alert(1)) [参考](https://example.invalid/)\n\n![外部图片](https://example.invalid/track.png)";
  await page.route("**/api/v1/sections/9", (route) =>
    route.fulfill({
      json: {
        document: { id: 7, version_id: version, title: "制度.pdf" },
        section,
        markdown,
        token_count: 5,
      },
    }),
  );
  await page.goto("/kb/5/documents");
  await page.getByRole("button", { name: "查看目录" }).click();
  await page.getByRole("button", { name: "查看 管理办法" }).click();
  const preview = page.getByLabel("文档目录与正文");
  await expect(preview.getByRole("table")).toBeVisible();
  await expect(preview.getByRole("cell", { name: "北京" })).toBeVisible();
  await expect(preview.locator("script, img")).toHaveCount(0);
  await expect(preview.getByText("危险链接")).toBeVisible();
  await expect(preview.getByRole("link", { name: "危险链接" })).toHaveCount(0);
  await expect(preview.getByRole("link", { name: "参考" })).toHaveAttribute(
    "rel",
    "noopener noreferrer",
  );
  expect(await page.evaluate(() => Object.hasOwn(window, "__tuskragXss"))).toBe(
    false,
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    375,
  );
  await page.getByRole("button", { name: "关闭预览" }).click();
  version = 10;
  await page.getByRole("button", { name: "查看目录" }).click();
  await expect(
    page.getByText("目录不可用，文档可能已更新或权限已变化。"),
  ).toBeVisible();
  await expect(
    page.getByLabel("文档目录与正文").locator("article"),
  ).toHaveCount(0);
});

test("keeps the document list usable if the preview module fails to load", async ({
  page,
}) => {
  await page.route("**/src/DocumentPreview.tsx*", (route) => route.abort());
  await page.route("**/assets/DocumentPreview-*.js", (route) => route.abort());
  await page.route("**/api/health/ready", (route) =>
    route.fulfill({ json: { status: "ok" } }),
  );
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
  );
  await page.route("**/api/v1/kbs/5", (route) =>
    route.fulfill({
      json: {
        id: 5,
        name: "员工知识库",
        embedding_space_id: 3,
        config_revision: 1,
        egress_policy: "deny",
        status: "active",
        role: "viewer",
        created_at: "2026-10-28T00:00:00Z",
      },
    }),
  );
  await page.route(/\/api\/v1\/kbs\/5\/documents\?cursor=0$/, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 7,
            title: "制度.pdf",
            mime: "application/pdf",
            current_version_id: 8,
            latest_version_id: 8,
            latest_job_id: null,
            retryable: false,
            latest_status: "ready",
            failure_code: null,
            byte_size: 100,
            latest_created_at: "2026-10-28T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    }),
  );
  await page.goto("/kb/5/documents");
  await page.getByRole("button", { name: "查看目录" }).click();
  await expect(page.getByRole("alert")).toHaveText(
    "预览加载失败，请刷新页面后重试。",
  );
  await page.getByRole("button", { name: "关闭预览" }).click();
  await expect(page.getByRole("heading", { name: "制度.pdf" })).toBeVisible();
});
