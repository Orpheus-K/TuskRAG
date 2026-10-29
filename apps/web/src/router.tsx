import {
  createRootRoute,
  createRoute,
  createRouter,
  Link,
  type RouterHistory,
} from "@tanstack/react-router";
import { useContext } from "react";
import { App, IdentityContext, Workspace } from "./App";
import { KnowledgeDetail } from "./Knowledge";

function InvalidAddress() {
  return (
    <section className="py-12">
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
const routeTree = rootRoute.addChildren([indexRoute, documentRoute]);

export function createAppRouter(history?: RouterHistory) {
  return createRouter({ routeTree, ...(history ? { history } : {}) });
}

export const router = createAppRouter();

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
