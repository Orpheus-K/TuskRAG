// Transport fixture for UI tests only; not a model quality baseline.
export const digest = "a".repeat(64);
export const metric = { RecallAt20: 1, HitAt5: 1, MRRAt10: 1, NDCGAt10: 1 };
export const report = {
  dataset_version: "页面验收集",
  dataset_sha256: "b".repeat(64), config_sha256: "c".repeat(64),
  model_label: "确定性替身", model_kind: "stub", mode: "rrf", status: "passed",
  unauthorized_evidence: 0,
  summary: { Cases: 21, AnswerableCases: 20, UnanswerableCases: 1, Metrics: metric },
  rows: Array.from({length:21},(_,i)=>({id:`q${i+1}`,metrics:i===20?null:metric,fused:[{ChunkID:1,DocumentID:2,SectionID:3,PageStart:0,PageEnd:0}],ranked:[{ChunkID:1,DocumentID:2,SectionID:3,PageStart:0,PageEnd:0}],timings_ms:{total:2},degraded:["rerank"]})),
  latency_ms_p50_p95: {total:[2,2]},
};
export const stored = {digest, source:"imported",created_at:"2026-11-06T00:00:00Z",report};
export const page = {items:[{digest,source:"imported",created_at:stored.created_at,dataset_version:report.dataset_version,model_label:report.model_label,model_kind:report.model_kind,mode:report.mode,summary:report.summary}],next_cursor:null};
