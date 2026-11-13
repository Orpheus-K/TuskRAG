// Explicit opt-in acceptance against a loopback Linux preview. Credentials and
// generated answer bodies are never written to the report or console.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const [mode, configuration] = process.argv.slice(2);
assert(["setup", "model", "ingest", "evaluate", "compare", "vectors", "pdf"].includes(mode));
const config = JSON.parse(await readFile(configuration, "utf8"));
assert.equal(config.opt_in, "project-documents-external-models");
const endpoint = new URL(config.endpoint);
assert(["127.0.0.1", "localhost", "[::1]"].includes(endpoint.hostname));
const dataset = JSON.parse(await readFile(new URL("project-documents-qa.json", import.meta.url), "utf8"));
const digest = (data) => createHash("sha256").update(data).digest("hex");
const corpus = JSON.parse(await readFile(new URL(dataset.corpus, import.meta.url), "utf8"));
assert.equal(corpus.documents.length, dataset.documents.length);
for (const [index, document] of corpus.documents.entries()) {
  assert.equal(document.source, dataset.documents[index]);
  assert.equal(digest(document.content), document.sha256, "corpus snapshot changed");
}
// TXT sections encode punctuation and control whitespace for safe Markdown
// display, while retrieved chunks carry plain text. Decode only that display
// encoding, preserving literal entities escaped in the original source.
function plainSection(markdown) {
  return markdown.replace(/\\([!-/:-@\[-`{-~])|&#(10|13|9|32);/g, (_match, punctuation, code) => punctuation ?? String.fromCharCode(Number(code)));
}
let cookie = "";
let csrf = "";
async function request(route, method = "GET", body, headers = {}) {
  const form = body instanceof FormData;
  const res = await fetch(new URL(route, endpoint), {
    method, headers: { ...(cookie ? { Cookie: cookie, "X-CSRF-Token": csrf } : {}), ...(!form && body !== undefined ? { "Content-Type": "application/json" } : {}), ...headers },
    body: body === undefined ? undefined : form ? body : JSON.stringify(body),
    signal: AbortSignal.timeout(130_000),
  });
  assert(res.ok, `HTTP ${method} ${route}: ${res.status}`);
  return res;
}
async function json(route, method, body, headers) {
  return (await request(route, method, body, headers)).json();
}
const login = await request("/api/v1/auth/login", "POST", {
  tenant_id: config.tenant_id, login: "mvp-admin", password: (await readFile(config.password_file, "utf8")).trim(),
}, { "X-TuskRAG-Login": "1" });
cookie = login.headers.get("set-cookie").split(";")[0];
csrf = (await login.json()).csrf_token;
let state;
const save = () => writeFile(config.state_file, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
if (mode === "setup") {
  const credential = (await readFile(config.embedding_key_file, "utf8")).trim();
  const chat = JSON.parse(await readFile(config.chat_config_file, "utf8"));
  const space = await json("/api/v1/admin/embedding-spaces", "POST", {
    provider_name: "MVP SiliconFlow", base_url: "https://api.siliconflow.cn", locality: "external", api_key: credential,
    model: "BAAI/bge-m3", dimensions: 1024, normalized: true, distance: "cosine",
  });
  const model = await json("/api/v1/admin/chat-models", "POST", {
    name: "MVP CLI Proxy", base_url: chat.base_url, locality: "external", api_key: chat.api_key, model: chat.model,
  });
  const kb = await json("/api/v1/kbs", "POST", { name: "TuskRAG 技术文档", embedding_space_id: space.id, egress_policy: "allow" });
  await json(`/api/v1/kbs/${kb.id}/config`, "PUT", { retrieval: { top_k: 8, reranker: { provider_id: space.provider_id, model: "BAAI/bge-reranker-v2-m3", top_n: 30, fallback: false } } }, { "If-Match": '"0"' });
  state = { tenant_id: config.tenant_id, kb_id: kb.id, space_id: space.id, model_id: model.id, model: chat.model, documents: [] };
  await save();
  console.log(JSON.stringify({ setup: "passed", tenant_id: state.tenant_id, kb_id: kb.id, space_id: space.id }));
} else {
  state = JSON.parse(await readFile(config.state_file, "utf8"));
}
if (mode === "model") {
  const chat = JSON.parse(await readFile(config.chat_config_file, "utf8"));
  const model = await json("/api/v1/admin/chat-models", "POST", {
    name: `MVP CLI Proxy ${chat.model}`, base_url: chat.base_url, locality: "external", api_key: chat.api_key, model: chat.model,
  });
  state.model_id = model.id;
  state.model = chat.model;
  await save();
  console.log(JSON.stringify({ model_id: model.id, model: chat.model }));
}
if (mode === "ingest") {
  assert.equal(state.documents.length, 0, "ingestion already started; inspect state before rerunning");
  for (const documentSource of corpus.documents) {
    const relative = documentSource.source;
    const content = Buffer.from(documentSource.content, "utf8");
    const form = new FormData();
    form.append("file", new Blob([content], { type: "text/plain" }), `${path.basename(relative)}.txt`);
    const document = await json(`/api/v1/kbs/${state.kb_id}/documents?allow_duplicate=true`, "POST", form);
    state.documents.push({ source: relative, sha256: digest(content), bytes: content.length, document_id: document.document_id, version_id: document.version_id });
    await save();
  }
  const deadline = Date.now() + 300_000;
  while (true) {
    const list = await json(`/api/v1/kbs/${state.kb_id}/documents`);
    const uploaded = list.items.filter((d) => state.documents.some((expected) => expected.document_id === d.id));
    assert.equal(uploaded.length, state.documents.length);
    assert(!uploaded.some((d) => d.latest_status === "failed"), `ingestion failed: ${JSON.stringify(uploaded.filter((d) => d.latest_status === "failed").map((d) => ({ id: d.id, failure: d.failure_code })))}`);
    if (uploaded.every((d) => d.latest_status === "ready")) break;
    assert(Date.now() < deadline, "ingestion deadline exceeded");
    await delay(1000);
  }
  console.log(JSON.stringify({ ingestion: "passed", documents: state.documents.length }));
}
async function ask(question, conversation) {
  if (!conversation) conversation = (await json("/api/v1/conversations", "POST", { kb_id: state.kb_id, model_id: state.model_id, title: question })).id;
  const started = performance.now();
  const res = await request(`/api/v1/conversations/${conversation}/messages`, "POST", { question });
  const result = { conversation, text: "", evidence: [], degraded: [], citations: [], invalid_citations: 0, done: null, first_delta_ms: null, source_integrity: true, source_mismatches: [] };
  let buffer = "";
  const decoder = new TextDecoder();
  for await (const part of res.body) {
    buffer += decoder.decode(part, { stream: true });
    let boundary;
    while ((boundary = buffer.indexOf("\n\n")) >= 0) {
      const lines = buffer.slice(0, boundary).split("\n");
      buffer = buffer.slice(boundary + 2);
      const name = lines.find((line) => line.startsWith("event: "))?.slice(7);
      const data = lines.find((line) => line.startsWith("data: "))?.slice(6);
      if (!name || !data) continue;
      const value = JSON.parse(data);
      assert.notEqual(name, "error", `stream error: ${value.code}`);
      if (name === "retrieval") { result.evidence = value.evidence; result.degraded = value.degraded; }
      if (name === "delta") { result.text += value.text; result.first_delta_ms ??= Math.round(performance.now() - started); }
      if (name === "citations") { result.citations = value.items; result.invalid_citations = value.invalid_count; }
      if (name === "done") result.done = value;
    }
  }
  assert(result.done, "missing final event");
  result.duration_ms = Math.round(performance.now() - started);
  for (const item of result.evidence) assert(state.documents.some((d) => d.document_id === item.document_id && d.version_id === item.version_id), "out-of-scope evidence");
  for (const citation of result.citations) {
    const evidence = result.evidence.find((e) => e.n === citation.n && e.id === citation.evidence_id);
    assert(evidence, "unknown citation");
    const section = await json(`/api/v1/sections/${evidence.section_id}`);
    assert.equal(section.document.id, evidence.document_id);
    assert.equal(section.document.version_id, evidence.version_id);
    let source = section.markdown;
    if (section.section.ordinal > 1) {
      const nearby = await json(`/api/v1/documents/${evidence.document_id}/toc?cursor=${section.section.ordinal - 2}`);
      assert.equal(nearby.document.version_id, evidence.version_id);
      const prior = nearby.sections.find((item) => item.ordinal === section.section.ordinal - 1 && item.level === section.section.level && JSON.stringify(item.heading_path) === JSON.stringify(section.section.heading_path));
      if (prior) {
        const context = await json(`/api/v1/sections/${prior.id}`);
        assert.equal(context.document.id, evidence.document_id);
        assert.equal(context.document.version_id, evidence.version_id);
        source = `${context.markdown}\n\n${source}`;
      }
    }
    const plain = plainSection(source).replace(/\s+/gu, "");
    const snippet = evidence.content.replace(/\s+/gu, "");
    if (!plain.includes(snippet)) {
      result.source_integrity = false;
      result.source_mismatches.push({ evidence_id: evidence.id, section_id: evidence.section_id, section_chars: plain.length, snippet_chars: snippet.length, suffix_in_section: plain.includes(snippet.slice(200)) });
    }
  }
  const history = await json(`/api/v1/conversations/${conversation}`);
  const saved = history.messages.find((m) => m.id === result.done.message_id);
  assert(saved, "answer not persisted");
  assert.equal(saved.content, result.text);
  return result;
}
if (mode === "pdf") {
  const content = await readFile(config.pdf_file);
  assert.equal(digest(content), "fa8c7f3ec7aa6059e789c7a6100cbfafddfb47aba48960c73276a15b2a255db7", "only the checked-in synthetic PDF may leave this fixture");
  await writeFile(config.pdf_state_file, '{"status":"starting"}\n', { mode: 0o600, flag: "wx" });
  const baseline = await json(`/api/v1/kbs/${state.kb_id}/config`);
  const kb = await json("/api/v1/kbs", "POST", { name: "跨页引用验收", embedding_space_id: state.space_id, egress_policy: "allow" });
  await json(`/api/v1/kbs/${kb.id}/config`, "PUT", { retrieval: baseline.retrieval }, { "If-Match": '"0"' });
  const form = new FormData();
  form.append("file", new Blob([content], { type: "application/pdf" }), "policy-cross-page.pdf");
  const uploaded = await json(`/api/v1/kbs/${kb.id}/documents?confirm_first_row_headers=true`, "POST", form);
  state = { ...state, kb_id: kb.id, documents: [{ source: "services/parser/tests/fixtures/pdf/policy-cross-page.pdf", document_id: uploaded.document_id, version_id: uploaded.version_id, sha256: digest(content), bytes: content.length }] };
  await writeFile(config.pdf_state_file, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
  const deadline = Date.now() + 300_000;
  while (true) {
    const list = await json(`/api/v1/kbs/${kb.id}/documents`);
    const document = list.items.find((item) => item.id === uploaded.document_id);
    assert(document && document.latest_status !== "failed", `PDF ingestion failed: ${document?.failure_code}`);
    if (document.latest_status === "ready") break;
    assert(Date.now() < deadline, "PDF ingestion deadline exceeded");
    await delay(1000);
  }
  const result = await ask("这张跨页住宿限额表中，北京和杭州每人每晚最高分别报销多少元？");
  const sources = result.citations.map((citation) => result.evidence.find((e) => e.id === citation.evidence_id));
  const pages = [...new Set(sources.flatMap((e) => e.pages ?? []))].sort();
  const report = { dataset: "cross-page-pdf-v1", model: state.model, prompt_version: result.done.prompt_version, document: state.documents[0], kb_id: kb.id, pass: result.done.outcome === "answered" && /北京/.test(result.text) && /600/.test(result.text) && /杭州/.test(result.text) && /380/.test(result.text) && JSON.stringify(pages) === "[1,2]" && result.source_integrity && result.invalid_citations === 0, outcome: result.done.outcome, cited_pages: pages, source_integrity: result.source_integrity, source_mismatches: result.source_mismatches, invalid_citations: result.invalid_citations, degraded: result.degraded, duration_ms: result.duration_ms, first_delta_ms: result.first_delta_ms, citations: sources.map((e) => ({ id: e.id, section_id: e.section_id, pages: e.pages })) };
  state.conversation_id = result.conversation;
  await writeFile(config.pdf_state_file, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
  await writeFile(config.pdf_report_file, `${JSON.stringify(report, null, 2)}\n`);
  console.log(JSON.stringify(report));
  assert(report.pass, "real PDF cross-page citation assertions failed");
}
if (mode === "evaluate") {
  for (const [index, document] of state.documents.entries()) assert.equal(document.sha256, corpus.documents[index].sha256, "uploaded corpus differs from snapshot");
  const report = { dataset: dataset.version, upload_format: dataset.upload_format, generated_at: new Date().toISOString(), model: state.model, corpus: state.documents, cases: [], retrieval: [], unauthorized_evidence: 0 };
  const conversations = new Map();
  for (const sample of dataset.cases) {
    const result = await ask(sample.question);
    report.prompt_version ??= result.done.prompt_version;
    assert.equal(result.done.prompt_version, report.prompt_version, "prompt changed during evaluation");
    conversations.set(sample.id, result.conversation);
    const factPass = sample.refusal ? result.done.outcome === "refused" && result.citations.length === 0 : result.done.outcome === "answered" && sample.patterns.every((p) => new RegExp(p, "i").test(result.text));
    const sourcePass = sample.refusal || result.citations.some((c) => result.evidence.some((e) => e.id === c.evidence_id && e.document_id === state.documents[sample.document].document_id));
    report.cases.push({ id: sample.id, pass: factPass && sourcePass && result.source_integrity && result.invalid_citations === 0, fact_pass: factPass, expected_source: sourcePass, source_integrity: result.source_integrity, source_mismatches: result.source_mismatches, outcome: result.done.outcome, citations: result.citations.length, invalid_citations: result.invalid_citations, duration_ms: result.duration_ms, first_delta_ms: result.first_delta_ms, degraded: result.degraded });
    const search = await json("/api/v1/search", "POST", { kb_ids: [state.kb_id], query: sample.question, top_k: 8 });
    report.retrieval.push({ id: sample.id, evidence: search.evidence.map((e) => ({ id: e.id, document_id: e.document.id, version_id: e.document.version_id, score: e.score, content_sha256: digest(e.content) })) });
    await writeFile(config.report_file, `${JSON.stringify(report, null, 2)}\n`);
    console.log(JSON.stringify(report.cases.at(-1)));
  }
  const followup = await ask(dataset.followup.question, conversations.get(dataset.followup.after));
  report.cases.push({ id: "followup", pass: followup.done.outcome === "answered" && followup.citations.length > 0 && followup.invalid_citations === 0 && followup.source_integrity && dataset.followup.patterns.every((p) => new RegExp(p).test(followup.text)), outcome: followup.done.outcome, citations: followup.citations.length, invalid_citations: followup.invalid_citations, source_integrity: followup.source_integrity, degraded: followup.degraded, duration_ms: followup.duration_ms });
  await json(`/api/v1/messages/${followup.done.message_id}/feedback`, "POST", { rating: 1, correction: "" });
  const saved = await json(`/api/v1/conversations/${followup.conversation}`);
  assert.equal(saved.messages.at(-1).feedback.rating, 1);
  report.passed = report.cases.filter((c) => c.pass).length;
  report.total = report.cases.length;
  report.feedback = "passed";
  await writeFile(config.report_file, `${JSON.stringify(report, null, 2)}\n`);
  state.conversation_id = followup.conversation;
  state.history_sha256 = digest(JSON.stringify(saved));
  await save();
  assert.equal(report.passed, report.total, "real corpus quality assertions failed; report retained");
}
if (mode === "compare") {
  const baseline = JSON.parse(await readFile(config.report_file, "utf8"));
  const rows = [];
  for (const sample of dataset.cases) {
    const search = await json("/api/v1/search", "POST", { kb_ids: [state.kb_id], query: sample.question, top_k: 8 });
    const expected = baseline.retrieval.find((r) => r.id === sample.id).evidence;
    const actual = search.evidence.map((e) => ({ id: e.id, document_id: e.document.id, version_id: e.document.version_id, score: e.score, content_sha256: digest(e.content) }));
    rows.push({ id: sample.id, evidence: actual.length, identical: JSON.stringify(actual) === JSON.stringify(expected), expected, actual });
  }
  const history = await json(`/api/v1/conversations/${state.conversation_id}`);
  assert.equal(digest(JSON.stringify(history)), state.history_sha256, "restored conversation or feedback changed");
  await writeFile(config.restore_report_file, `${JSON.stringify({ dataset: dataset.version, cases: rows, conversation_and_feedback: "identical" }, null, 2)}\n`);
  console.log(JSON.stringify({ restored_queries: rows.length, identical_queries: rows.filter((row) => row.identical).length, conversation_and_feedback: "identical" }));
  assert(rows.every((row) => row.identical), "live model search changed; full comparison retained");
}
if (mode === "vectors") {
  // Capture each real query embedding once for both database recall runs. This
  // isolates restoration from independent, non-deterministic provider calls.
  const credential = (await readFile(config.embedding_key_file, "utf8")).trim();
  const response = await fetch("https://api.siliconflow.cn/v1/embeddings", {
    method: "POST", headers: { Authorization: `Bearer ${credential}`, "Content-Type": "application/json" },
    body: JSON.stringify({ model: "BAAI/bge-m3", input: dataset.cases.map((sample) => sample.question) }),
    signal: AbortSignal.timeout(60_000),
  });
  assert(response.ok, `query embedding failed: ${response.status}`);
  const embeddings = await response.json();
  assert.equal(embeddings.data.length, dataset.cases.length);
  const cases = dataset.cases.map((sample, index) => {
    const vector = embeddings.data.find((item) => item.index === index)?.embedding;
    assert(Array.isArray(vector) && vector.length === 1024 && vector.every(Number.isFinite));
    return { id: sample.id, query: sample.question, vector };
  });
  await writeFile(config.recall_fixture_file, `${JSON.stringify({ dataset: dataset.version, tenant_id: config.tenant_id, user_id: 1, kb_id: state.kb_id, allowed_document_ids: state.documents.map((d) => d.document_id), cases })}\n`, { mode: 0o600, flag: "wx" });
  console.log(JSON.stringify({ captured_query_vectors: cases.length, dimensions: 1024 }));
}
