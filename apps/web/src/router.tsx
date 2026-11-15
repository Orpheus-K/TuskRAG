import {
  createRootRoute,
  createRoute,
  createRouter,
  Link,
  type RouterHistory,
} from "@tanstack/react-router";
import { useContext } from "react";
import { App, IdentityContext, Workspace } from "./App";
import { Evaluation } from "./Evaluation";
import { EvaluationCompare } from "./EvaluationCompare";
import { EvaluationSets } from "./EvaluationSets";
import { KnowledgeDetail } from "./Knowledge";

function InvalidAddress() {
  return (
    <section className="py-6">
      <p role="alert">页面地址无效。</p>
      <Link className="mt-4 inline-block text-sm underline" to="/">
        返回知识库
      </Link>
    </section>
  );
}

function WorkspacePage() {
  const identity = useContext(IdentityContext);
  if (!identity) throw new Error("authenticated identity is required");
  return <Workspace identity={identity} />;
}

function DocumentPage() {
  const identity = useContext(IdentityContext);
  const { kbId } = documentRoute.useParams();
  const { cursor } = documentRoute.useSearch();
  const id = Number(kbId);
  if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id)) {
    return <InvalidAddress />;
  }
  if (!identity) throw new Error("authenticated identity is required");
  return <KnowledgeDetail identity={identity} id={id} cursor={cursor} />;
}

const rootRoute = createRootRoute({
  component: App,
  notFoundComponent: InvalidAddress,
});
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: WorkspacePage,
});
const documentRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/documents",
  validateSearch: (search: Record<string, unknown>) => {
    const cursor = search.cursor ?? 0;
    if (
      typeof cursor !== "number" ||
      !Number.isSafeInteger(cursor) ||
      cursor < 0
    ) {
      throw new Error("invalid document cursor");
    }
    return { cursor };
  },
  errorComponent: InvalidAddress,
  component: DocumentPage,
});
const evalRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/eval",
  validateSearch: (search: Record<string, unknown>) => {
    const cursor = search.cursor ?? "",
      digest = search.digest ?? "",
      row = search.row ?? 0;
    if (
      typeof cursor !== "string" ||
      typeof digest !== "string" ||
      !/^([0-9a-f]{64})?$/.test(cursor) ||
      !/^([0-9a-f]{64})?$/.test(digest) ||
      typeof row !== "number" ||
      !Number.isSafeInteger(row) ||
      row < 0 ||
      row > 499
    )
      throw new Error("invalid evaluation address");
    return { cursor, digest, row };
  },
  errorComponent: InvalidAddress,
  component: () => {
    const identity = useContext(IdentityContext);
    const { kbId } = evalRoute.useParams();
    const search = evalRoute.useSearch();
    const navigate = evalRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <Evaluation
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        cursor={search.cursor}
        digest={search.digest}
        rowPage={search.row}
        onSelect={(digest) => {
          void navigate({ search: { cursor: "", digest, row: 0 } });
        }}
      />
    );
  },
});
const compareRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/eval/compare",
  errorComponent: InvalidAddress,
  validateSearch: (search: Record<string, unknown>) => {
    const baseline = search.baseline ?? "",
      current = search.current ?? "",
      cursor = search.cursor ?? "",
      offset = search.offset ?? 0;
    if (
      typeof baseline !== "string" ||
      typeof current !== "string" ||
      typeof cursor !== "string" ||
      ![baseline, current, cursor].every((v) => /^([0-9a-f]{64})?$/.test(v)) ||
      typeof offset !== "number" ||
      !Number.isSafeInteger(offset) ||
      offset < 0 ||
      offset > 10000
    )
      throw new Error("invalid comparison address");
    return { baseline, current, cursor, offset };
  },
  component: () => {
    const identity = useContext(IdentityContext);
    const { kbId } = compareRoute.useParams();
    const selection = compareRoute.useSearch();
    const navigate = compareRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <EvaluationCompare
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        selection={selection}
        onChange={(search) => {
          void navigate({ search: (previous) => ({ ...previous, ...search }) });
        }}
      />
    );
  },
});
const setsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/eval/sets",
  errorComponent: InvalidAddress,
  validateSearch: (search: Record<string, unknown>) => {
    const cursor = search.cursor ?? 0,
      set = search.set ?? 0;
    if (
      typeof cursor !== "number" ||
      !Number.isSafeInteger(cursor) ||
      cursor < 0 ||
      typeof set !== "number" ||
      !Number.isSafeInteger(set) ||
      set < 0
    )
      throw new Error("invalid evaluation set address");
    return { cursor, set };
  },
  component: () => {
    const identity = useContext(IdentityContext),
      { kbId } = setsRoute.useParams(),
      search = setsRoute.useSearch(),
      navigate = setsRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <EvaluationSets
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        cursor={search.cursor}
        selected={search.set}
        onChange={(change) =>
          void navigate({ search: (previous) => ({ ...previous, ...change }) })
        }
      />
    );
  },
});
const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/eval/runs",
  errorComponent: InvalidAddress,
  validateSearch: (search: Record<string, unknown>) => {
    const set = search.set ?? 0,
      revision = search.revision ?? 0,
      run = search.run ?? 0,
      row = search.row ?? 0;
    if (
      ![set, revision, run, row].every(
        (v) => typeof v === "number" && Number.isSafeInteger(v) && v >= 0,
      ) ||
      Number(row) > 499 ||
      Number(revision) > 2147483647 ||
      (run === 0 && (set === 0 || revision === 0))
    )
      throw new Error("invalid run address");
    return {
      set: Number(set),
      revision: Number(revision),
      run: Number(run),
      row: Number(row),
    };
  },
  component: () => {
    const identity = useContext(IdentityContext),
      { kbId } = runRoute.useParams(),
      search = runRoute.useSearch(),
      navigate = runRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <EvaluationRun
        key={`${identity.tenant_id}:${identity.user_id}:${id}:${search.set}:${search.revision}:${search.run}`}
        identity={identity}
        id={id}
        selection={search}
        onChange={(change) =>
          void navigate({ search: (previous) => ({ ...previous, ...change }) })
        }
      />
    );
  },
});
const historyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/eval/history",
  errorComponent: InvalidAddress,
  validateSearch: (search: Record<string, unknown>) => {
    const cursor = search.cursor ?? 0;
    if (
      typeof cursor !== "number" ||
      !Number.isSafeInteger(cursor) ||
      cursor < 0
    )
      throw new Error("invalid history cursor");
    return { cursor };
  },
  component: () => {
    const identity = useContext(IdentityContext),
      { kbId } = historyRoute.useParams(),
      { cursor } = historyRoute.useSearch(),
      navigate = historyRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <EvaluationHistory
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        cursor={cursor}
        onCursor={(cursor) => void navigate({ search: { cursor } })}
      />
    );
  },
});
const runCompareRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/eval/run-compare",
  errorComponent: InvalidAddress,
  validateSearch: (search: Record<string, unknown>) => {
    const baseline = search.baseline ?? 0,
      current = search.current ?? 0,
      cursor = search.cursor ?? 0,
      offset = search.offset ?? 0;
    if (
      ![baseline, current, cursor, offset].every(
        (v) => typeof v === "number" && Number.isSafeInteger(v) && v >= 0,
      ) ||
      Number(offset) > 10000
    )
      throw new Error("invalid run comparison address");
    return {
      baseline: Number(baseline),
      current: Number(current),
      cursor: Number(cursor),
      offset: Number(offset),
    };
  },
  component: () => {
    const identity = useContext(IdentityContext),
      { kbId } = runCompareRoute.useParams(),
      selection = runCompareRoute.useSearch(),
      navigate = runCompareRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <EvaluationRunCompare
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        selection={selection}
        onChange={(change) =>
          void navigate({ search: (previous) => ({ ...previous, ...change }) })
        }
      />
    );
  },
});
const rebuildRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/rebuild",
  errorComponent: InvalidAddress,
  validateSearch: (search: Record<string, unknown>) => {
    const job = search.job ?? 0;
    if (typeof job !== "number" || !Number.isSafeInteger(job) || job < 0)
      throw new Error("invalid rebuild address");
    return { job };
  },
  component: () => {
    const identity = useContext(IdentityContext),
      { kbId } = rebuildRoute.useParams(),
      { job } = rebuildRoute.useSearch(),
      navigate = rebuildRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <RebuildPage
        key={`${identity.tenant_id}:${identity.user_id}:${id}:${job}`}
        identity={identity}
        id={id}
        job={job}
        onCreated={(job) => void navigate({ search: { job } })}
      />
    );
  },
});
const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/settings",
  errorComponent: InvalidAddress,
  component: () => {
    const identity = useContext(IdentityContext);
    const { kbId } = settingsRoute.useParams();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <KnowledgeSettingsPage
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
      />
    );
  },
});
const answerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/answer",
  validateSearch: (
    search: Record<string, unknown>,
  ): { conversation?: number } => {
    const conversation = search.conversation;
    if (conversation === undefined) return {};
    if (
      typeof conversation !== "number" ||
      !Number.isSafeInteger(conversation) ||
      conversation <= 0
    )
      throw new Error("invalid conversation");
    return { conversation };
  },
  errorComponent: InvalidAddress,
  component: () => {
    const identity = useContext(IdentityContext);
    const { kbId } = answerRoute.useParams();
    const { conversation } = answerRoute.useSearch();
    const navigate = answerRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <AnswerPage
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        conversation={conversation}
        onSelect={(selected) => {
          void navigate({ search: selected ? { conversation: selected } : {} });
        }}
      />
    );
  },
});
const answerEvalRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/answer-evaluation",
  validateSearch: (search: Record<string, unknown>) => {
    const cursor = search.cursor ?? "",
      digest = search.digest ?? "",
      row = search.row ?? 0;
    if (
      typeof cursor !== "string" ||
      typeof digest !== "string" ||
      !/^([0-9a-f]{64})?$/.test(cursor) ||
      !/^([0-9a-f]{64})?$/.test(digest) ||
      typeof row !== "number" ||
      !Number.isSafeInteger(row) ||
      row < 0 ||
      row > 499
    )
      throw new Error("invalid answer evaluation address");
    return { cursor, digest, row };
  },
  errorComponent: InvalidAddress,
  component: () => {
    const identity = useContext(IdentityContext);
    const { kbId } = answerEvalRoute.useParams();
    const search = answerEvalRoute.useSearch();
    const navigate = answerEvalRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <AnswerEvaluation
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        cursor={search.cursor}
        digest={search.digest}
        rowPage={search.row}
        onSelect={(digest) => {
          void navigate({ search: { cursor: "", digest, row: 0 } });
        }}
      />
    );
  },
});
const answerEvalCompareRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "kb/$kbId/answer-evaluation/compare",
  errorComponent: InvalidAddress,
  validateSearch: (search: Record<string, unknown>) => {
    const baseline = search.baseline ?? "",
      current = search.current ?? "",
      cursor = search.cursor ?? "",
      offset = search.offset ?? 0;
    if (
      typeof baseline !== "string" ||
      typeof current !== "string" ||
      typeof cursor !== "string" ||
      ![baseline, current, cursor].every((v) => /^([0-9a-f]{64})?$/.test(v)) ||
      typeof offset !== "number" ||
      !Number.isSafeInteger(offset) ||
      offset < 0 ||
      offset > 10000
    )
      throw new Error("invalid answer comparison address");
    return { baseline, current, cursor, offset };
  },
  component: () => {
    const identity = useContext(IdentityContext);
    const { kbId } = answerEvalCompareRoute.useParams();
    const selection = answerEvalCompareRoute.useSearch();
    const navigate = answerEvalCompareRoute.useNavigate();
    const id = Number(kbId);
    if (!identity) throw new Error("authenticated identity is required");
    if (!/^[1-9]\d*$/.test(kbId) || !Number.isSafeInteger(id))
      return <InvalidAddress />;
    return (
      <AnswerEvaluationCompare
        key={`${identity.tenant_id}:${identity.user_id}:${id}`}
        identity={identity}
        id={id}
        selection={selection}
        onChange={(search) => {
          void navigate({ search: (previous) => ({ ...previous, ...search }) });
        }}
      />
    );
  },
});
const apiKeysRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "api-keys",
  component: () => {
    const identity = useContext(IdentityContext);
    if (!identity) throw new Error("authenticated identity is required");
    return <APIKeysPage identity={identity} />;
  },
});
const adminRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "admin",
  component: () => {
    const identity = useContext(IdentityContext);
    if (!identity) throw new Error("authenticated identity is required");
    return <AdminGovernancePage identity={identity} />;
  },
});

const routeTree = rootRoute.addChildren([
  adminRoute,
  apiKeysRoute,
  answerRoute,
  answerEvalRoute,
  answerEvalCompareRoute,
  rebuildRoute,
  settingsRoute,
  runCompareRoute,
  historyRoute,
  runRoute,
  indexRoute,
  documentRoute,
  evalRoute,
  compareRoute,
  setsRoute,
]);

export function createAppRouter(history?: RouterHistory) {
  return createRouter({ routeTree, ...(history ? { history } : {}) });
}

export const router = createAppRouter();

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

import { AdminGovernancePage } from "./AdminGovernance";
import { AnswerPage } from "./Answer";
import { AnswerEvaluation } from "./AnswerEvaluation";
import { AnswerEvaluationCompare } from "./AnswerEvaluationCompare";
import { APIKeysPage } from "./APIKeys";
import { EvaluationHistory } from "./EvaluationHistory";
import { EvaluationRun } from "./EvaluationRun";
import { EvaluationRunCompare } from "./EvaluationRunCompare";
import { KnowledgeSettingsPage } from "./KnowledgeSettings";
import { RebuildPage } from "./Rebuild";
