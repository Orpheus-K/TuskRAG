import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type ACLMode,
  type ACLSubject,
  ApiError,
  addGroupMember,
  createGroup,
  deleteGroup,
  getDocumentACL,
  type Identity,
  listGroupMembers,
  listGroups,
  listTenantUsers,
  removeGroupMember,
  setDocumentACL,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@tuskrag/ui/card";
import { Checkbox } from "@tuskrag/ui/checkbox";
import { ConfirmAction } from "@tuskrag/ui/confirm-action";
import { Dialog } from "@tuskrag/ui/dialog";
import { Input } from "@tuskrag/ui/input";
import { Label } from "@tuskrag/ui/label";
import { Select, SelectItem } from "@tuskrag/ui/select";
import { Lock, Trash2 } from "lucide-react";
import { type FormEvent, useState } from "react";

function aclErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) return "登录已过期，请重新登录。";
    if (error.status === 403) return "当前角色无权修改文档权限或用户组。";
    if (error.status === 404) return "文档、用户或用户组不存在。";
    if (error.code === "name_conflict") return "用户组名称已存在。";
  }
  return "操作未确认，请稍后重试。";
}

export function UserGroupsPanel({ identity }: { identity: Identity }) {
  return (
    <Card aria-labelledby="user-groups-title" role="region">
      <CardHeader>
        <CardTitle id="user-groups-title">用户组管理</CardTitle>
        <CardDescription>
          用户组在租户内共享，可在文档权限中授权给整组成员。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <UserGroupsEditor identity={identity} />
      </CardContent>
    </Card>
  );
}
function UserGroupsEditor({ identity }: { identity: Identity }) {
  const queryClient = useQueryClient();
  const [groupName, setGroupName] = useState("");
  const [selectedGroupId, setSelectedGroupId] = useState<number | null>(null);
  const [selectedUserId, setSelectedUserId] = useState<string>("");
  const [error, setError] = useState<string | null>(null);

  const groupsKey = ["user-groups", identity.tenant_id, identity.user_id];
  const groups = useQuery({
    queryKey: groupsKey,
    queryFn: ({ signal }) => listGroups(signal),
    retry: false,
  });

  const users = useQuery({
    queryKey: ["tenant-users", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listTenantUsers(signal),
    retry: false,
  });

  const activeGroupId = selectedGroupId ?? groups.data?.[0]?.id ?? null;

  const groupMembers = useQuery({
    queryKey: ["group-members", identity.tenant_id, activeGroupId],
    queryFn: ({ signal }) =>
      activeGroupId !== null
        ? listGroupMembers(activeGroupId, signal)
        : Promise.resolve([]),
    enabled: activeGroupId !== null,
    retry: false,
  });

  const create = useMutation({
    mutationFn: (name: string) => createGroup(name, identity.csrf_token),
    onSuccess: async (created) => {
      setError(null);
      setGroupName("");
      setSelectedGroupId(created.id);
      await queryClient.invalidateQueries({ queryKey: groupsKey });
    },
    onError: (err) => setError(aclErrorMessage(err)),
  });

  const removeGroup = useMutation({
    mutationFn: (id: number) => deleteGroup(id, identity.csrf_token),
    onSuccess: async (_, deletedId) => {
      setError(null);
      if (selectedGroupId === deletedId) setSelectedGroupId(null);
      await queryClient.invalidateQueries({ queryKey: groupsKey });
    },
    onError: (err) => setError(aclErrorMessage(err)),
  });

  const addMember = useMutation({
    mutationFn: ({ groupId, userId }: { groupId: number; userId: number }) =>
      addGroupMember(groupId, userId, identity.csrf_token),
    onSuccess: async () => {
      setError(null);
      await queryClient.invalidateQueries({
        queryKey: ["group-members", identity.tenant_id, activeGroupId],
      });
    },
    onError: (err) => setError(aclErrorMessage(err)),
  });

  const removeMember = useMutation({
    mutationFn: ({ groupId, userId }: { groupId: number; userId: number }) =>
      removeGroupMember(groupId, userId, identity.csrf_token),
    onSuccess: async () => {
      setError(null);
      await queryClient.invalidateQueries({
        queryKey: ["group-members", identity.tenant_id, activeGroupId],
      });
    },
    onError: (err) => setError(aclErrorMessage(err)),
  });

  const memberIds = new Set((groupMembers.data ?? []).map((m) => m.user_id));
  const candidates = (users.data ?? []).filter((u) => !memberIds.has(u.id));

  function handleCreate(event: FormEvent) {
    event.preventDefault();
    const trimmed = groupName.trim();
    if (!trimmed) return;
    create.mutate(trimmed);
  }

  const activeGroup = groups.data?.find((g) => g.id === activeGroupId);

  return (
    <div id="user-groups-panel" className="space-y-4 text-sm">
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      <form onSubmit={handleCreate} className="flex flex-wrap items-end gap-2">
        <Label className="block min-w-0 flex-1 space-y-1.5 text-xs font-medium">
          <span>新建用户组</span>
          <Input
            aria-label="新建用户组名称"
            value={groupName}
            onChange={(e) => setGroupName(e.target.value)}
            placeholder="例如：finance-team"
          />
        </Label>
        <Button
          type="submit"
          variant="outline"
          disabled={create.isPending || !groupName.trim()}
        >
          创建用户组
        </Button>
      </form>

      {groups.isPending && (
        <p role="status" className="text-muted-foreground">
          正在加载用户组
        </p>
      )}
      {groups.data?.length === 0 && (
        <p className="text-muted-foreground">暂无用户组。</p>
      )}
      {groups.data && groups.data.length > 0 && (
        <div className="grid gap-4 md:grid-cols-[minmax(10rem,14rem)_minmax(0,1fr)]">
          <ul className="space-y-0.5" aria-label="用户组列表">
            {groups.data.map((g) => (
              <li key={g.id} className="flex items-center gap-1">
                <Button
                  type="button"
                  size="sm"
                  variant={activeGroupId === g.id ? "secondary" : "ghost"}
                  className="min-w-0 flex-1 justify-start truncate"
                  aria-current={activeGroupId === g.id ? "true" : undefined}
                  onClick={() => setSelectedGroupId(g.id)}
                >
                  {g.name}
                </Button>
                <ConfirmAction
                  title={`删除用户组“${g.name}”？`}
                  description="使用该用户组授权的文档将不再对组成员可见。"
                  onConfirm={() => removeGroup.mutate(g.id)}
                  trigger={
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      aria-label={`删除用户组 ${g.name}`}
                      disabled={removeGroup.isPending}
                    >
                      <Trash2 aria-hidden="true" className="h-3.5 w-3.5" />
                    </Button>
                  }
                />
              </li>
            ))}
          </ul>

          {activeGroupId !== null && (
            <section
              aria-label="组成员"
              className="space-y-3 rounded-lg border p-3"
            >
              <p className="text-xs font-semibold text-muted-foreground">
                {activeGroup ? `${activeGroup.name} · 组成员` : "组成员"}
              </p>
              {groupMembers.data?.length === 0 && (
                <p className="text-muted-foreground">该组暂无成员。</p>
              )}
              <ul className="divide-y">
                {(groupMembers.data ?? []).map((m) => (
                  <li
                    key={m.user_id}
                    className="flex items-center justify-between gap-2 py-1.5"
                  >
                    <span className="min-w-0 truncate">
                      {m.display_name}
                      <span className="ml-1.5 text-xs text-muted-foreground">
                        #{m.user_id}
                      </span>
                    </span>
                    <Button
                      type="button"
                      size="xs"
                      variant="ghost"
                      onClick={() =>
                        removeMember.mutate({
                          groupId: activeGroupId,
                          userId: m.user_id,
                        })
                      }
                    >
                      移出组
                    </Button>
                  </li>
                ))}
              </ul>
              {candidates.length > 0 && (
                <div className="flex flex-wrap items-center gap-2 border-t pt-3">
                  <Select
                    aria-label="选择加入组的用户"
                    className="h-8 w-auto min-w-40"
                    value={selectedUserId || String(candidates[0]?.id ?? "")}
                    onValueChange={(value) => setSelectedUserId(value)}
                  >
                    {candidates.map((u) => (
                      <SelectItem key={u.id} value={String(u.id)}>
                        {u.display_name} (#{u.id})
                      </SelectItem>
                    ))}
                  </Select>
                  <Button
                    type="button"
                    size="sm"
                    onClick={() => {
                      const uid = Number(selectedUserId || candidates[0]?.id);
                      if (uid > 0) {
                        addMember.mutate({
                          groupId: activeGroupId,
                          userId: uid,
                        });
                      }
                    }}
                  >
                    加入组
                  </Button>
                </div>
              )}
            </section>
          )}
        </div>
      )}
    </div>
  );
}
export function DocumentACLButton({
  identity,
  kbId,
  documentId,
  documentTitle,
  role,
}: {
  identity: Identity;
  kbId: number;
  documentId: number;
  documentTitle: string;
  role: string;
}) {
  const [open, setOpen] = useState(false);
  if (role !== "owner" && role !== "admin") return null;
  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title={`文档权限 · ${documentTitle}`}
      className="w-[min(40rem,94vw)]"
      trigger={
        <Button type="button" size="sm" variant="ghost">
          <Lock aria-hidden="true" className="h-3.5 w-3.5" />
          文档权限
        </Button>
      }
    >
      <DocumentACLEditor
        identity={identity}
        kbId={kbId}
        documentId={documentId}
        documentTitle={documentTitle}
      />
    </Dialog>
  );
}
function DocumentACLEditor({
  identity,
  kbId,
  documentId,
  documentTitle,
}: {
  identity: Identity;
  kbId: number;
  documentId: number;
  documentTitle: string;
}) {
  const queryClient = useQueryClient();
  const aclKey = ["document-acl", identity.tenant_id, kbId, documentId];
  const acl = useQuery({
    queryKey: aclKey,
    queryFn: ({ signal }) => getDocumentACL(kbId, documentId, signal),
    retry: false,
  });
  const users = useQuery({
    queryKey: ["tenant-users", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listTenantUsers(signal),
    retry: false,
  });
  const groups = useQuery({
    queryKey: ["user-groups", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listGroups(signal),
    retry: false,
  });

  const [draftMode, setDraftMode] = useState<ACLMode | null>(null);
  const [draftSubjects, setDraftSubjects] = useState<ACLSubject[] | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const mode: ACLMode =
    draftMode ?? (acl.data?.acl_mode as ACLMode | undefined) ?? "inherit_kb";
  const subjects: ACLSubject[] =
    draftSubjects ?? (acl.data?.subjects as ACLSubject[] | undefined) ?? [];

  const save = useMutation({
    mutationFn: () =>
      setDocumentACL(
        kbId,
        documentId,
        mode,
        mode === "inherit_kb" ? [] : subjects,
        identity.csrf_token,
      ),
    onSuccess: async () => {
      setError(null);
      setStatus("文档权限已保存。");
      setDraftMode(null);
      setDraftSubjects(null);
      await queryClient.invalidateQueries({ queryKey: aclKey });
    },
    onError: (err) => {
      setStatus(null);
      setError(aclErrorMessage(err));
    },
  });

  function toggleSubject(type: "user" | "group", id: number) {
    const exists = subjects.some((s) => s.type === type && s.id === id);
    if (exists) {
      setDraftSubjects(
        subjects.filter((s) => !(s.type === type && s.id === id)),
      );
    } else {
      setDraftSubjects([...subjects, { type, id }]);
    }
  }

  return (
    <div className="space-y-3 text-sm">
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
      {status && (
        <p role="status" className="text-emerald-700 dark:text-emerald-300">
          {status}
        </p>
      )}
      {acl.isPending && (
        <p role="status" className="text-muted-foreground">
          正在加载文档权限
        </p>
      )}
      {acl.isError && !error && (
        <p role="alert" className="text-danger">
          {aclErrorMessage(acl.error)}
        </p>
      )}
      {acl.data && (
        <div className="space-y-4">
          <Label className="block space-y-1.5 text-xs font-medium">
            <span>访问模式</span>
            <Select
              aria-label={`文档 ${documentTitle} 访问模式`}
              value={mode}
              onValueChange={(value) => setDraftMode(value as ACLMode)}
            >
              <SelectItem value="inherit_kb">
                继承知识库成员 (inherit_kb)
              </SelectItem>
              <SelectItem value="restricted">
                仅指定主体可见 (restricted)
              </SelectItem>
            </Select>
          </Label>

          {mode === "restricted" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="rounded-lg border p-3">
                <p className="text-xs font-semibold text-muted-foreground">
                  允许的用户
                </p>
                <div className="mt-2 max-h-56 space-y-1.5 overflow-y-auto">
                  {(users.data ?? []).map((u) => (
                    <Label key={u.id} className="flex items-center gap-2">
                      <Checkbox
                        checked={subjects.some(
                          (s) => s.type === "user" && s.id === u.id,
                        )}
                        onCheckedChange={() => toggleSubject("user", u.id)}
                      />
                      <span>
                        {u.display_name} (#{u.id})
                      </span>
                    </Label>
                  ))}
                </div>
              </div>
              <div className="rounded-lg border p-3">
                <p className="text-xs font-semibold text-muted-foreground">
                  允许的用户组
                </p>
                <div className="mt-2 max-h-56 space-y-1.5 overflow-y-auto">
                  {groups.data?.length === 0 && (
                    <p className="text-muted-foreground">暂无用户组。</p>
                  )}
                  {(groups.data ?? []).map((g) => (
                    <Label key={g.id} className="flex items-center gap-2">
                      <Checkbox
                        checked={subjects.some(
                          (s) => s.type === "group" && s.id === g.id,
                        )}
                        onCheckedChange={() => toggleSubject("group", g.id)}
                      />
                      <span>{g.name}</span>
                    </Label>
                  ))}
                </div>
              </div>
            </div>
          )}

          <div className="flex justify-end">
            <Button
              type="button"
              disabled={save.isPending}
              onClick={() => save.mutate()}
            >
              {save.isPending ? "保存中..." : "保存文档权限"}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
