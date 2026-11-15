import { useQuery } from "@tanstack/react-query";
import { getKnowledgeBase, type Identity } from "@tuskrag/api-client";
import { Alert } from "@tuskrag/ui/alert";
import { SectionHeader } from "@tuskrag/ui/section-header";
import { UserGroupsPanel } from "./DocumentACL";
import { KnowledgeConfigPanel } from "./KnowledgeConfig";
import { KnowledgeMembersPanel } from "./KnowledgeMembers";

/** Low-frequency knowledge base settings, kept off the documents page. */
export function KnowledgeSettingsPage({
  identity,
  id,
}: {
  identity: Identity;
  id: number;
}) {
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
    gcTime: 0,
  });
  if (base.isPending)
    return (
      <p role="status" className="py-6 text-sm text-muted-foreground">
        正在加载知识库
      </p>
    );
  if (base.isError)
    return (
      <p role="alert" className="py-6">
        知识库不可用或访问权限已变化。
      </p>
    );
  if (base.data.role !== "owner" && base.data.role !== "admin")
    return (
      <p role="alert" className="py-6">
        当前账号无知识库设置权限。
      </p>
    );
  return (
    <section className="space-y-5">
      <SectionHeader title={`${base.data.name} · 设置`} />
      {base.data.status === "archived" && (
        <Alert variant="warning">知识库已归档，设置仅供查看。</Alert>
      )}
      <div className="grid items-start gap-4 xl:grid-cols-2">
        <KnowledgeConfigPanel identity={identity} base={base.data} />
        <KnowledgeMembersPanel identity={identity} base={base.data} />
        <UserGroupsPanel identity={identity} />
      </div>
    </section>
  );
}
