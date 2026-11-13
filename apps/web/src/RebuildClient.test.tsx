import {
  ApiError,
  cancelRebuild,
  createRebuild,
  getRebuild,
  type Rebuild,
} from "@tuskrag/api-client";
import { afterEach, expect, it, vi } from "vitest";

const queued: Rebuild = {
  job_id: 9,
  source_space_id: 1,
  source_revision: 2,
  target_space_id: 3,
  status: "queued",
  attempts: 0,
  max_attempts: 3,
  failure_code: null,
  created_at: "2026-11-07T00:00:00Z",
  finished_at: null,
};
afterEach(() => vi.unstubAllGlobals());
const reply = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });

it("sends only the rebuild request and preserves the same idempotency key on retry", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(
      reply(
        {
          error: {
            code: "temporary",
            message: "retry",
            retryable: true,
            trace_id: "t",
          },
        },
        503,
      ),
    )
    .mockResolvedValueOnce(reply(queued));
  vi.stubGlobal("fetch", fetcher);
  const input = { target_space_id: 3, revision: 2 };
  await expect(
    createRebuild(7, input, "csrf", "same-key"),
  ).rejects.toBeInstanceOf(ApiError);
  await expect(createRebuild(7, input, "csrf", "same-key")).resolves.toEqual(
    queued,
  );
  expect(fetcher.mock.calls[0]).toEqual(fetcher.mock.calls[1]);
  expect(fetcher.mock.calls[1]).toEqual([
    "/api/v1/kbs/7/rebuilds",
    {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": "csrf",
        "Idempotency-Key": "same-key",
      },
      body: JSON.stringify(input),
    },
  ]);
});

it("rejects invalid rebuild addresses and input before network access", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  await expect(
    createRebuild(
      7,
      { target_space_id: 3, revision: 2147483647 },
      "csrf",
      "key",
    ),
  ).rejects.toThrow();
  await expect(
    createRebuild(7, { target_space_id: 3, revision: 2 }, "csrf", "bad key"),
  ).rejects.toThrow();
  await expect(
    getRebuild(7, 0, new AbortController().signal),
  ).rejects.toThrow();
  await expect(cancelRebuild(-1, 9, "csrf")).rejects.toThrow();
  expect(fetcher).not.toHaveBeenCalled();
});

it("checks returned job and request binding", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValueOnce(reply({ ...queued, job_id: 10 }))
      .mockResolvedValueOnce(reply({ ...queued, target_space_id: 4 }))
      .mockResolvedValueOnce(reply({ ...queued, source_revision: 3 })),
  );
  await expect(getRebuild(7, 9, new AbortController().signal)).rejects.toThrow(
    "invalid rebuild response",
  );
  for (let i = 0; i < 2; i++)
    await expect(
      createRebuild(7, { target_space_id: 3, revision: 2 }, "csrf", "key"),
    ).rejects.toThrow("invalid rebuild response");
});

it.each([
  { status: "unknown" },
  { failure_code: undefined },
  { finished_at: undefined },
  { attempts: 4 },
  { target_space_id: 1 },
  { source_space_id: 0 },
  { created_at: "bad" },
  { status: "succeeded" },
  { status: "failed", finished_at: "2026-11-07T00:01:00Z" },
  { status: "running", finished_at: "2026-11-07T00:01:00Z" },
  { status: "succeeded", finished_at: "2026-11-06T00:00:00Z" },
])("rejects inconsistent rebuild payload %j", async (change) => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(reply({ ...queued, ...change })),
  );
  await expect(getRebuild(7, 9, new AbortController().signal)).rejects.toThrow(
    "invalid rebuild response",
  );
});

it.each(["queued", "running", "succeeded", "failed", "dead", "cancelled"])(
  "accepts real state %s and forwards cancellation signal",
  async (status) => {
    const terminal = !["queued", "running"].includes(status);
    const result = {
      ...queued,
      status,
      attempts: 1,
      failure_code: ["failed", "dead"].includes(status)
        ? "model_invalid_response"
        : null,
      finished_at: terminal ? "2026-11-07T00:01:00Z" : null,
    };
    const fetcher = vi.fn().mockResolvedValue(reply(result));
    vi.stubGlobal("fetch", fetcher);
    const signal = new AbortController().signal;
    await expect(getRebuild(7, 9, signal)).resolves.toEqual(result);
    expect(fetcher).toHaveBeenCalledWith("/api/v1/kbs/7/rebuilds/9", {
      credentials: "same-origin",
      signal,
    });
  },
);

it("requires a real cancellation acknowledgement and sends CSRF", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(reply({ status: "running" }))
    .mockResolvedValueOnce(reply({ status: "cancelled" }));
  vi.stubGlobal("fetch", fetcher);
  await expect(cancelRebuild(7, 9, "csrf")).rejects.toThrow();
  await expect(cancelRebuild(7, 9, "csrf")).resolves.toBeUndefined();
  expect(fetcher.mock.calls[1]).toEqual([
    "/api/v1/kbs/7/rebuilds/9/cancel",
    {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": "csrf" },
    },
  ]);
});

it("accepts successful retries retaining the queue's last failure code", async () => {
  const recovered = {
    ...queued,
    status: "succeeded",
    attempts: 2,
    failure_code: "model_unavailable",
    finished_at: "2026-11-07T00:01:00Z",
  };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(reply(recovered)));
  await expect(getRebuild(7, 9, new AbortController().signal)).resolves.toEqual(
    recovered,
  );
});
