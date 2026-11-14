import { Label } from "@tuskrag/ui/label";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  type EgressPolicy,
  type Identity,
  type KnowledgeBase,
  type KnowledgeBaseRole,
  listKnowledgeBaseMembers,
  listTenantUsers,
  removeKnowledgeBaseMember,
  setKnowledgeBaseEgressPolicy,
  setKnowledgeBaseMember,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { Select, SelectItem } from "@tuskrag/ui/select";
import { type FormEvent, useState } from "react";

const roleLabels: Record<KnowledgeBaseRole, string> = {
  owner: "所有者 (owner)",
  admin: "管理员 (admin)",
  editor: "编辑者 (editor)",
  viewer: "只读成员 (viewer)",
};

const egressLabels: Record<EgressPolicy, string> = {
  deny: "禁止出域 (deny)",
  internal_only: "仅内网 (internal_only)",
  allow: "允许出域 (allow)",
};

function memberErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === "last_owner") {
      return "知识库必须至少保留一名所有者 (owner)。";
    }
    if (error.code === "egress_denied") {
      return "所选出域策略超出租户允许的上限。";
    }
    if (error.status === 401) return "登录已过期，请重新登录。";
    if (error.status === 403) return "只有知识库所有者可以管理成员与出域策略。";
    if (error.status === 404) return "知识库或目标用户不存在。";
    if (error.code === "knowledge_base_archived") {
      return "知识库已归档，无法修改出域策略。";
    }
  }
  return "操作未确认，请刷新页面后重试。";
}

export function KnowledgeMembersPanel({
  identity,
  base,
}: {
  identity: Identity;
  base: KnowledgeBase;
}) {
  const [open, setOpen] = useState(false);
  const [hasOpened, setHasOpened] = useState(false);
  if (base.role !== "owner") return null;
  return (
    <section className="mt-4">
      <Button
        variant="outline"
        aria-expanded={open}
        aria-controls="knowledge-members"
        onClick={() => {
          setHasOpened(true);
          setOpen(!open);
        }}
      >
        成员与出域策略
      </Button>
      {hasOpened && (
        <div hidden={!open}>
          <MembersAndEgressEditor
            key={`${identity.tenant_id}:${identity.user_id}:${base.id}`}
            identity={identity}
            base={base}
          />
        </div>
      )}
    </section>
  );
}

function MembersAndEgressEditor({
  identity,
  base,
}: {
  identity: Identity;
  base: KnowledgeBase;
}) {
  const queryClient = useQueryClient();
  const [policy, setPolicy] = useState<EgressPolicy>(
    base.egress_policy as EgressPolicy,
  );
  const [selectedUserId, setSelectedUserId] = useState<string>("");
  const [selectedRole, setSelectedRole] = useState<KnowledgeBaseRole>("viewer");
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const membersKey = [
    "kb-members",
    identity.tenant_id,
    identity.user_id,
    base.id,
  ];
  const members = useQuery({
    queryKey: membersKey,
    queryFn: ({ signal }) => listKnowledgeBaseMembers(base.id, signal),
    retry: false,
  });

  const tenantUsers = useQuery({
    queryKey: ["tenant-users", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listTenantUsers(signal),
    retry: false,
  });

  const updatePolicy = useMutation({
    mutationFn: (nextPolicy: EgressPolicy) =>
      setKnowledgeBaseEgressPolicy(base.id, nextPolicy, identity.csrf_token),
    onSuccess: async (updated) => {
      setErrorMessage(null);
      setStatusMessage(`出域策略已更新为 ${updated.egress_policy}。`);
      await queryClient.invalidateQueries({
        queryKey: ["kb", identity.tenant_id, identity.user_id, base.id],
      });
    },
    onError: (err) => {
      setStatusMessage(null);
      setErrorMessage(memberErrorMessage(err));
    },
  });

  const saveMember = useMutation({
    mutationFn: ({
      userId,
      role,
    }: {
      userId: number;
      role: KnowledgeBaseRole;
    }) => setKnowledgeBaseMember(base.id, userId, role, identity.csrf_token),
    onSuccess: async () => {
      setErrorMessage(null);
      setStatusMessage("成员角色已更新。");
      await queryClient.invalidateQueries({ queryKey: membersKey });
    },
    onError: (err) => {
      setStatusMessage(null);
      setErrorMessage(memberErrorMessage(err));
    },
  });

  const removeMember = useMutation({
    mutationFn: (userId: number) =>
      removeKnowledgeBaseMember(base.id, userId, identity.csrf_token),
    onSuccess: async () => {
      setErrorMessage(null);
      setStatusMessage("成员已移除。");
      await queryClient.invalidateQueries({ queryKey: membersKey });
    },
    onError: (err) => {
      setStatusMessage(null);
      setErrorMessage(memberErrorMessage(err));
    },
  });

  const memberIds = new Set((members.data ?? []).map((m) => m.user_id));
  const candidateUsers = (tenantUsers.data ?? []).filter(
    (u) => !memberIds.has(u.id),
  );

  function handleAddMember(event: FormEvent) {
    event.preventDefault();
    const userId = Number(selectedUserId || candidateUsers[0]?.id);
    if (!Number.isSafeInteger(userId) || userId <= 0) return;
    saveMember.mutate({ userId, role: selectedRole });
    setSelectedUserId("");
  }

  return (
    <div
      id="knowledge-members"
      className="mt-4 space-y-6 rounded-md border p-4"
    >
      {errorMessage && (
        <p role="alert" className="text-sm text-destructive">
          {errorMessage}
        </p>
      )}
      {statusMessage && (
        <p role="status" className="text-sm text-emerald-700">
          {statusMessage}
        </p>
      )}

      <div className="space-y-3">
        <h3 className="text-sm font-semibold">数据出域策略</h3>
        <div className="flex flex-wrap items-end gap-3">
          <Label className="block min-w-[220px] space-y-1 text-sm">
            <span>知识库出域策略</span>
            <Select
              aria-label="知识库出域策略"
              value={policy}
              onValueChange={(value) => setPolicy(value as EgressPolicy)}
            >
              <SelectItem value="deny">{egressLabels.deny}</SelectItem>
              <SelectItem value="internal_only">
                {egressLabels.internal_only}
              </SelectItem>
              <SelectItem value="allow">{egressLabels.allow}</SelectItem>
            </Select>
          </Label>
          <Button
            type="button"
            disabled={updatePolicy.isPending}
            onClick={() => updatePolicy.mutate(policy)}
          >
            {updatePolicy.isPending ? "保存中..." : "保存出域策略"}
          </Button>
        </div>
      </div>

      <div className="space-y-3 border-t pt-4">
        <h3 className="text-sm font-semibold">知识库成员</h3>
        {members.isPending && (
          <p className="text-sm text-muted-foreground">正在加载成员列表...</p>
        )}
        {members.isError && (
          <p role="alert" className="text-sm text-destructive">
            {memberErrorMessage(members.error)}
          </p>
        )}
        {members.data && (
          <ul className="divide-y rounded-md border">
            {members.data.map((member) => (
              <li
                key={member.user_id}
                className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm"
              >
                <div>
                  <span className="font-medium">{member.display_name}</span>
                  <span className="ml-2 text-xs text-muted-foreground">
                    #{member.user_id}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <Select
                    aria-label={`成员 ${member.display_name} 角色`}
                    className="h-8 w-auto px-2 py-1"
                    value={member.role}
                    disabled={saveMember.isPending}
                    onValueChange={(value) =>
                      saveMember.mutate({
                        userId: member.user_id,
                        role: value as KnowledgeBaseRole,
                      })
                    }
                  >
                    <SelectItem value="owner">{roleLabels.owner}</SelectItem>
                    <SelectItem value="admin">{roleLabels.admin}</SelectItem>
                    <SelectItem value="editor">{roleLabels.editor}</SelectItem>
                    <SelectItem value="viewer">{roleLabels.viewer}</SelectItem>
                  </Select>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={removeMember.isPending}
                    onClick={() => removeMember.mutate(member.user_id)}
                  >
                    移除
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}

        {candidateUsers.length > 0 && (
          <form
            onSubmit={handleAddMember}
            className="flex flex-wrap items-end gap-3 pt-2"
          >
            <Label className="block min-w-[200px] space-y-1 text-sm">
              <span>添加租户成员</span>
              <Select
                aria-label="选择租户用户"
                value={selectedUserId || String(candidateUsers[0]?.id ?? "")}
                onValueChange={(value) => setSelectedUserId(value)}
              >
                {candidateUsers.map((u) => (
                  <SelectItem key={u.id} value={String(u.id)}>
                    {u.display_name} (#{u.id})
                  </SelectItem>
                ))}
              </Select>
            </Label>
            <Label className="block min-w-[180px] space-y-1 text-sm">
              <span>分配角色</span>
              <Select
                aria-label="新成员角色"
                value={selectedRole}
                onValueChange={(value) =>
                  setSelectedRole(value as KnowledgeBaseRole)
                }
              >
                <SelectItem value="viewer">{roleLabels.viewer}</SelectItem>
                <SelectItem value="editor">{roleLabels.editor}</SelectItem>
                <SelectItem value="admin">{roleLabels.admin}</SelectItem>
                <SelectItem value="owner">{roleLabels.owner}</SelectItem>
              </Select>
            </Label>
            <Button type="submit" disabled={saveMember.isPending}>
              添加成员
            </Button>
          </form>
        )}
      </div>
    </div>
  );
}
