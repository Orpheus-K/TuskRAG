import { expect, test } from "@playwright/test";

for (const width of [1280, 375]) {
  test(`api keys management and confirm revoke at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.route("**/api/health/ready", (route) =>
      route.fulfill({ json: { status: "ok" } }),
    );
    await page.route("**/api/v1/me", (route) =>
      route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
    );
    await page.route("**/api/v1/kbs", (route) =>
      route.fulfill({ json: { items: [] } }),
    );

    let revoked = false;
    let created = false;
    await page.route("**/api/v1/api-keys", (route) => {
      if (route.request().method() === "POST") {
        created = true;
        return route.fulfill({
          json: {
            key: {
              id: 2,
              name: "agent-e2e",
              prefix: "ZxYv9876",
              kb_ids: [],
              actions: ["search", "read", "answer"],
              ip_allowlist: [],
              rate_limit_rpm: 60,
              monthly_token_quota: 0,
              month_tokens_used: 0,
              created_at: "2026-11-15T00:00:00Z",
            },
            plaintext: "tsk_ZxYv9876_0123456789abcdef0123456789abcdef",
          },
        });
      }
      return route.fulfill({
        json: {
          items: [
            {
              id: 1,
              name: "existing-bot",
              prefix: "AbCd1234",
              kb_ids: [],
              actions: ["search"],
              ip_allowlist: [],
              rate_limit_rpm: 60,
              monthly_token_quota: 10000,
              month_tokens_used: 250,
              revoked_at: revoked ? "2026-11-15T01:00:00Z" : null,
              created_at: "2026-11-13T00:00:00Z",
            },
            ...(created
              ? [
                  {
                    id: 2,
                    name: "agent-e2e",
                    prefix: "ZxYv9876",
                    kb_ids: [],
                    actions: ["search", "read", "answer"],
                    ip_allowlist: [],
                    rate_limit_rpm: 60,
                    monthly_token_quota: 0,
                    month_tokens_used: 0,
                    revoked_at: null,
                    created_at: "2026-11-15T00:00:00Z",
                  },
                ]
              : []),
          ],
        },
      });
    });
    await page.route("**/api/v1/api-keys/1", (route) => {
      if (route.request().method() !== "DELETE") return route.fallback();
      revoked = true;
      return route.fulfill({ status: 204, body: "" });
    });

    await page.goto("/api-keys");
    await expect(page.getByText("existing-bot")).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);

    await page.getByLabel("密钥名称").fill("agent-e2e");
    await page.getByRole("button", { name: "生成 API Key" }).click();
    await expect(
      page.getByText("tsk_ZxYv9876_0123456789abcdef0123456789abcdef", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(page.getByText(/"mcpServers"/)).toBeVisible();

    await page.getByRole("button", { name: "吊销" }).first().click();
    await expect(
      page.getByRole("alertdialog", { name: "吊销 API Key「existing-bot」" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "确认吊销" }).click();
    await expect(page.getByText("已吊销")).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);
  });

  test(`admin governance tabs and confirm delete at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.route("**/api/health/ready", (route) =>
      route.fulfill({ json: { status: "ok" } }),
    );
    await page.route("**/api/v1/me", (route) =>
      route.fulfill({ json: { tenant_id: 1, user_id: 2, csrf_token: "test" } }),
    );
    await page.route("**/api/v1/kbs", (route) =>
      route.fulfill({ json: { items: [] } }),
    );
    await page.route(/\/api\/v1\/admin\/audit-logs\?.*$/, (route) =>
      route.fulfill({
        json: {
          items: [
            {
              id: 101,
              tenant_id: 1,
              actor_user_id: 2,
              actor_name: "Alice",
              action: "api_key.create",
              object_type: "api_key",
              object_id: 9,
              result: "allow",
              details: {},
              created_at: "2026-11-13T10:00:00Z",
            },
          ],
        },
      }),
    );

    let providerDeleted = false;
    await page.route("**/api/v1/admin/model-providers", (route) =>
      route.fulfill({
        json: {
          items: providerDeleted
            ? []
            : [
                {
                  id: 5,
                  name: "Local vLLM",
                  base_url: "http://127.0.0.1:8000/v1",
                  locality: "internal",
                  has_credential: false,
                  created_at: "2026-11-13T10:00:00Z",
                },
              ],
        },
      }),
    );
    await page.route("**/api/v1/admin/model-providers/5/test", (route) =>
      route.fulfill({
        json: {
          reachable: true,
          status_code: 200,
          latency_ms: 12,
          message: "HTTP 200 OK",
        },
      }),
    );
    await page.route("**/api/v1/admin/model-providers/5", (route) => {
      if (route.request().method() !== "DELETE") return route.fallback();
      providerDeleted = true;
      return route.fulfill({ status: 204, body: "" });
    });
    await page.route("**/api/v1/admin/tenant-egress", (route) =>
      route.fulfill({ json: { egress_policy_max: "internal_only" } }),
    );
    await page.route("**/api/v1/admin/oidc-providers", (route) =>
      route.fulfill({
        json: { items: [], disable_local_login: false },
      }),
    );

    await page.goto("/admin");
    await expect(page.getByText("api_key.create")).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);

    await page.getByRole("button", { name: "模型供应商" }).click();
    await expect(page.getByText("Local vLLM")).toBeVisible();
    await page.getByRole("button", { name: "测试连通性" }).click();
    await expect(page.getByText(/HTTP 200 OK/)).toBeVisible();
    await page.getByRole("button", { name: "删除" }).click();
    await expect(
      page.getByRole("alertdialog", { name: "删除模型供应商「Local vLLM」" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "确认删除" }).click();
    await expect(page.getByText("Local vLLM")).toHaveCount(0);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);

    await page.getByRole("button", { name: "身份源（OIDC）" }).click();
    await expect(page.getByText("禁用本地密码登录")).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);
  });
}
