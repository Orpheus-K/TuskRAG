import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { createAppRouter } from "./router";

const digestA = "a".repeat(64);
const digestB = "b".repeat(64);

const sampleMetrics = {
  valid_citation_rate: 1,
  source_integrity_rate: 1,
  evidence_hit_rate: 1,
  deterministic_pass_rate: 1,
  refusal_correct_rate: 1,
  false_refusal_rate: 0,
  unsupported_rate: 0,
  citation_support_rate: 1,
};

const sampleSummary = {
  cases: 2,
  answerable_cases: 1,
  unanswerable_cases: 1,
  answered_cases: 1,
  refused_cases: 1,
  error_cases: 0,
  metrics: sampleMetrics,
};

const sampleAssembly = {
  context_budget_tokens: 2048,
  parent_backtrack: true,
};

const sampleReport = {
  dataset_version: "e6-fixture-v1",
  dataset_sha256: digestA,
  config_sha256: digestB,
  model_label: "stub-chat",
  model_kind: "stub" as const,
  prompt_version: "answer-v1",
  status: "passed" as const,
  assembly: sampleAssembly,
  summary: sampleSummary,
  rows: [
    {
      id: "q1",
      answerable: true,
      outcome: "answered" as const,
      valid_citation: true,
      source_integrity: true,
      citation_count: 1,
      invalid_citations: 0,
      evidence_hit: true,
      deterministic_pass: true,
      evidence_ids: ["10:20:30"],
      evidence_chunk_counts: [1],
      evidence_documents: [10],
      degraded: [],
      timings_ms: { total: 12 },
    },
    {
      id: "q2",
      answerable: false,
      outcome: "refused" as const,
      valid_citation: true,
      source_integrity: true,
      citation_count: 0,
      invalid_citations: 0,
      evidence_hit: null,
      deterministic_pass: null,
      evidence_ids: [],
      evidence_chunk_counts: [],
      evidence_documents: [],
      degraded: [],
      timings_ms: { total: 8 },
    },
  ],
  latency_ms_p50_p95: { total: [8, 12] as [number, number] },
};

const sampleListItemA = {
  digest: digestA,
  source: "imported" as const,
  created_at: "2026-11-13T08:00:00Z",
  dataset_version: "e6-fixture-v1",
  model_label: "stub-chat",
  model_kind: "stub" as const,
  prompt_version: "answer-v1",
  assembly: sampleAssembly,
  summary: sampleSummary,
};

const sampleListItemB = {
  ...sampleListItemA,
  digest: digestB,
  model_label: "stub-chat-v2",
};

const sampleStored = {
  digest: digestA,
  source: "imported" as const,
  created_at: "2026-11-13T08:00:00Z",
  report: sampleReport,
};

const sampleComparison = {
  gate: "passed" as const,
  baseline: sampleListItemA,
  current: sampleListItemB,
  delta: {
    valid_citation_rate: 0,
    source_integrity_rate: 0,
    evidence_hit_rate: 0,
    deterministic_pass_rate: 0,
    refusal_correct_rate: 0,
    false_refusal_rate: 0,
    unsupported_rate: 0,
    citation_support_rate: 0,
  },
  total: 2,
  offset: 0,
  limit: 20,
  rows: [
    {
      id: "q1",
      answerable: true,
      baseline_outcome: "answered",
      current_outcome: "answered",
      baseline_valid_citation: true,
      current_valid_citation: true,
      baseline_source_integrity: true,
      current_source_integrity: true,
      baseline_citation_count: 1,
      current_citation_count: 1,
      baseline_invalid_citations: 0,
      current_invalid_citations: 0,
    },
    {
      id: "q2",
      answerable: false,
      baseline_outcome: "refused",
      current_outcome: "refused",
      baseline_valid_citation: true,
      current_valid_citation: true,
      baseline_source_integrity: true,
      current_source_integrity: true,
      baseline_citation_count: 0,
      current_citation_count: 0,
      baseline_invalid_citations: 0,
      current_invalid_citations: 0,
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function setup(initialPath: string) {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? "GET"} ${url}`);
      if (url === "/api/v1/me")
        return new Response(
          JSON.stringify({ tenant_id: 1, user_id: 2, csrf_token: "csrf" }),
        );
      if (url === "/api/health/ready")
        return new Response(JSON.stringify({ status: "ok" }));
      if (url === "/api/v1/kbs/5")
        return new Response(
          JSON.stringify({
            id: 5,
            name: "测试知识库",
            embedding_space_id: 3,
            config_revision: 1,
            egress_policy: "deny",
            status: "active",
            role: "admin",
            created_at: "2026-11-13T08:00:00Z",
          }),
        );
      if (
        url.includes("/answer-eval-reports?") &&
        (init?.method ?? "GET") === "GET"
      )
        return new Response(
          JSON.stringify({
            items: [sampleListItemA, sampleListItemB],
            next_cursor: null,
          }),
        );
      if (url.endsWith(`/answer-eval-reports/${digestA}`))
        return new Response(JSON.stringify(sampleStored));
      if (url.endsWith("/answer-eval-reports") && init?.method === "POST")
        return new Response(JSON.stringify(sampleStored));
      if (url.includes("/answer-eval-report-comparison?"))
        return new Response(JSON.stringify(sampleComparison));
      throw new Error(`unexpected fetch: ${url}`);
    }),
  );

  const client = new QueryClient();
  const history = createMemoryHistory({ initialEntries: [initialPath] });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={createAppRouter(history)} />
    </QueryClientProvider>,
  );
  return { calls };
}

it("renders answer evaluation list, detail, and imports a valid E6 report", async () => {
  const { calls } = setup(`/kb/5/answer-evaluation?digest=${digestA}`);
  expect(await screen.findByText(/报告详情 · e6-fixture-v1/)).toBeTruthy();
  expect(screen.getByText(/q1 · 可答 · 结果 answered/)).toBeTruthy();
  expect(screen.getByText(/q2 · 不可答 · 结果 refused/)).toBeTruthy();

  const file = new File([JSON.stringify(sampleReport)], "e6.json", {
    type: "application/json",
  });
  Object.defineProperty(file, "text", {
    value: async () => JSON.stringify(sampleReport),
  });
  vi.spyOn(FormData.prototype, "get").mockImplementation((key) =>
    key === "report" ? file : null,
  );
  const input = screen.getByLabelText(/完整 E6 生成评测报告 JSON/);
  const form = input.closest("form");
  if (!form) throw new Error("missing form");
  fireEvent.submit(form);

  expect(
    await screen.findByText("生成评测报告已导入。相同内容自动去重。"),
  ).toBeTruthy();
  expect(
    calls.some((c) => c === "POST /api/v1/kbs/5/answer-eval-reports"),
  ).toBe(true);
});

it("renders answer evaluation comparison gate and rows", async () => {
  setup(
    `/kb/5/answer-evaluation/compare?baseline=${digestA}&current=${digestB}`,
  );
  expect(await screen.findByText(/门禁状态：通过 \(passed\)/)).toBeTruthy();
  expect(screen.getByText(/逐题对比（共 2 题）/)).toBeTruthy();
  expect(screen.getByText(/基线：answered/)).toBeTruthy();
});
