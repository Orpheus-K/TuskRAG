import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  type DocumentPage,
  deleteDocument,
  getKnowledgeBase,
  type Identity,
  importDocumentURL,
  listDocuments,
  listEmbeddingSpaces,
  listPurgeJobs,
  retryJob,
  type SearchOptions,
  searchKnowledgeBase,
  uploadDocument,
  uploadDocumentVersion,
} from "@tuskrag/api-client";
import { Alert } from "@tuskrag/ui/alert";
import { Badge, type BadgeProps } from "@tuskrag/ui/badge";
import { Button } from "@tuskrag/ui/button";
import { Checkbox } from "@tuskrag/ui/checkbox";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@tuskrag/ui/collapsible";
import { ConfirmAction } from "@tuskrag/ui/confirm-action";
import { Dialog } from "@tuskrag/ui/dialog";
import { EmptyState } from "@tuskrag/ui/empty-state";
import { Field } from "@tuskrag/ui/field";
import { HeadingPath } from "@tuskrag/ui/heading-path";
import { Input } from "@tuskrag/ui/input";
import { Label } from "@tuskrag/ui/label";
import { SectionHeader } from "@tuskrag/ui/section-header";
import { Select, SelectItem } from "@tuskrag/ui/select";
import { WorkspaceSplit } from "@tuskrag/ui/workspace-split";
import {
  BookOpen,
  ChevronRight,
  FileText,
  Link2,
  Loader2,
  RotateCcw,
  Search,
  Trash2,
  Upload,
} from "lucide-react";
import { type FormEvent, useRef, useState } from "react";
import { DocumentACLButton } from "./DocumentACL";
import type { PreviewTarget } from "./DocumentPreview";
import { Preview } from "./Preview";
import { SearchTrace } from "./SearchTrace";

const statuses: Record<string, string> = {
  queued: "等待处理",
  parsing: "正在解析",
  embedding: "正在生成向量",
  indexing: "正在建立索引",
  ready: "已就绪",
  partial: "部分就绪",
  failed: "处理失败",
  superseded: "已被替换",
  purging: "正在清理",
};

const processing = ["queued", "parsing", "embedding", "indexing", "purging"];

const statusVariants: Record<string, BadgeProps["variant"]> = {
  ready: "success",
  partial: "warning",
  failed: "danger",
};

const roleLabels: Record<string, string> = {
  owner: "所有者",
  admin: "管理员",
  editor: "编辑者",
  viewer: "查看者",
};

const failures: Record<string, string> = {
  unsupported_format: "文件格式或内容暂不支持",
  encrypted: "文件已加密",
  limit_exceeded: "文件超过处理限制",
  parse_error: "文件解析失败",
  vlm_denied: "图像解析受到出域策略限制",
  model_unavailable: "模型服务不可用",
  storage_unavailable: "文件存储暂不可用",
  model_rejected: "模型服务拒绝请求，请检查模型配置",
  model_invalid_response: "模型返回的数据不符合要求",
  configuration_changed: "处理期间配置发生变化，请重新提交",
  version_superseded: "此版本已被更新版本取代",
  unsupported_embedding: "当前嵌入空间的检索方式暂不支持",
  tokenizer_mismatch: "模型分词与配置不一致",
  egress_denied: "模型调用不符合数据出域策略",
  index_unavailable: "检索索引尚未就绪，请联系管理员",
  quality_review: "文档解析质量不足，需要检查",
  table_header_required: "表格表头需要确认",
  timeout: "处理超时",
  internal: "处理遇到内部错误",
  file_missing: "原文件不可用",
  file_corrupt: "原文件校验失败",
  cancelled: "处理已取消",
  lease_expired: "处理多次中断",
};

const purgeStatuses: Record<string, string> = {
  queued: "等待清理",
  running: "正在清理",
  succeeded: "已清理",
  failed: "清理失败",
  dead: "等待人工重试",
  cancelled: "已取消",
};

function statusIs(error: unknown, status: number): boolean {
  return error instanceof ApiError && error.status === status;
}

export function KnowledgeDetail({
  identity,
  id,
  cursor,
}: {
  identity: Identity;
  id: number;
  cursor: number;
}) {
  const queryClient = useQueryClient();
  const [uploadOpen, setUploadOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [versionTarget, setVersionTarget] = useState<{
    id: number;
    title: string;
  } | null>(null);
  const [allowDuplicate, setAllowDuplicate] = useState(false);
  const [confirmFirstRowHeaders, setConfirmFirstRowHeaders] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState("");
  const [uploadMessage, setUploadMessage] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [importURL, setImportURL] = useState("");
  const [importTitle, setImportTitle] = useState("");
  const [importDuplicate, setImportDuplicate] = useState(false);
  const [importingURL, setImportingURL] = useState(false);
  const [importError, setImportError] = useState("");
  const [importMessage, setImportMessage] = useState("");
  const [deleting, setDeleting] = useState<number | null>(null);
  const [deleteMessage, setDeleteMessage] = useState("");
  const [deleteError, setDeleteError] = useState("");
  const [retrying, setRetrying] = useState<number | null>(null);
  const [retryMessage, setRetryMessage] = useState("");
  const [retryError, setRetryError] = useState("");
  const [purgeCursor, setPurgeCursor] = useState(0);
  const [retryingPurge, setRetryingPurge] = useState<number | null>(null);
  const [purgeMessage, setPurgeMessage] = useState("");
  const [purgeError, setPurgeError] = useState("");
  const [preview, setPreview] = useState<PreviewTarget | null>(null);
  const [searchText, setSearchText] = useState("");
  const [submittedSearch, setSubmittedSearch] = useState("");
  const [rerankMode, setRerankMode] = useState("inherit");
  const [searchOptions, setSearchOptions] = useState<SearchOptions>({
    debug: true,
  });
  const providers = useQuery({
    queryKey: ["embedding-spaces", identity.tenant_id, identity.user_id],
    queryFn: ({ signal }) => listEmbeddingSpaces(signal),
    retry: false,
  });
  const uploadKey = useRef<string | null>(null);
  const base = useQuery({
    queryKey: ["knowledge-base", identity.tenant_id, identity.user_id, id],
    queryFn: ({ signal }) => getKnowledgeBase(id, signal),
    retry: false,
    gcTime: 0,
  });
  const documents = useQuery({
    queryKey: ["documents", identity.tenant_id, identity.user_id, id, cursor],
    queryFn: ({ signal }) => listDocuments(id, cursor, signal),
    retry: false,
    gcTime: 0,
    refetchInterval: (query) =>
      query.state.status !== "error" &&
      query.state.data?.items.some((item) =>
        processing.includes(item.latest_status),
      )
        ? 3000
        : false,
  });
  const search = useQuery({
    queryKey: [
      "search",
      identity.tenant_id,
      identity.user_id,
      id,
      submittedSearch,
      searchOptions,
    ],
    queryFn: ({ signal }) =>
      searchKnowledgeBase(
        id,
        submittedSearch,
        identity.csrf_token,
        signal,
        searchOptions,
      ),
    enabled:
      submittedSearch.length > 0 &&
      base.data?.status === "active" &&
      !base.isError &&
      !documents.isError,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
  const purgeJobs = useQuery({
    queryKey: [
      "purge-jobs",
      identity.tenant_id,
      identity.user_id,
      id,
      purgeCursor,
    ],
    queryFn: ({ signal }) => listPurgeJobs(id, purgeCursor, signal),
    enabled:
      base.data !== undefined && base.data.role !== "viewer" && !base.isError,
    retry: false,
    gcTime: 0,
    refetchInterval: (query) =>
      query.state.status !== "error" &&
      query.state.data?.items.some((item) =>
        ["queued", "running"].includes(item.status),
      )
        ? 3000
        : false,
  });

  function returnToLogin() {
    queryClient.removeQueries({ queryKey: ["knowledge-base"] });
    queryClient.removeQueries({ queryKey: ["knowledge-bases"] });
    queryClient.removeQueries({ queryKey: ["documents"] });
    queryClient.removeQueries({ queryKey: ["purge-jobs"] });
    queryClient.removeQueries({ queryKey: ["search"] });
    queryClient.removeQueries({ queryKey: ["document-contents"] });
    queryClient.removeQueries({ queryKey: ["document-section"] });
    queryClient.removeQueries({ queryKey: ["embedding-spaces"] });
    queryClient.setQueryData(["identity"], null);
  }

  function resetUpload() {
    setFile(null);
    setAllowDuplicate(false);
    setConfirmFirstRowHeaders(false);
    setUploadError("");
    uploadKey.current = null;
  }

  function openUpload(target: { id: number; title: string } | null) {
    resetUpload();
    setVersionTarget(target);
    setUploadMessage("");
    setUploadOpen(true);
  }

  function submitSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const query = searchText.trim();
    if (!query || search.isFetching) return;
    const form = new FormData(event.currentTarget);
    const mode = form.get("rerank-mode");
    const options: SearchOptions = { debug: true };
    if (mode === "off") options.rerank = false;
    if (mode === "custom") {
      options.rerank = true;
      options.reranker = {
        provider_id: Number(form.get("rerank-provider")),
        model: String(form.get("rerank-model") ?? "").trim(),
        top_n: 30,
        fallback: form.get("rerank-fallback") === "on",
      };
    }
    if (JSON.stringify(options) !== JSON.stringify(searchOptions)) {
      setSearchOptions(options);
      setSubmittedSearch(query);
      return;
    }
    if (query === submittedSearch) {
      void search.refetch();
    } else {
      setSubmittedSearch(query);
    }
  }

  async function submitUpload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!file || uploading || deleting !== null || retrying !== null) return;
    setUploading(true);
    setUploadError("");
    setUploadMessage("");
    try {
      uploadKey.current ??= crypto.randomUUID();
      const result = versionTarget
        ? await uploadDocumentVersion(
            id,
            versionTarget.id,
            file,
            identity.csrf_token,
            uploadKey.current,
            allowDuplicate,
            confirmFirstRowHeaders,
          )
        : await uploadDocument(
            id,
            file,
            identity.csrf_token,
            uploadKey.current,
            allowDuplicate,
            confirmFirstRowHeaders,
          );
      resetUpload();
      setVersionTarget(null);
      setUploadOpen(false);
      setUploadMessage(
        `已接收 ${result.title}，当前状态：${statuses[result.status]}。`,
      );
      void queryClient.invalidateQueries({ queryKey: ["documents"] });
    } catch (cause) {
      if (statusIs(cause, 401)) {
        returnToLogin();
        return;
      }
      if (statusIs(cause, 403) || statusIs(cause, 404)) {
        void base.refetch();
        void documents.refetch();
      }
      setUploadError(uploadErrorMessage(cause));
    } finally {
      setUploading(false);
    }
  }

  async function submitImport(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!importURL.trim() || importingURL) return;
    setImportingURL(true);
    setImportError("");
    setImportMessage("");
    try {
      const res = await importDocumentURL(
        id,
        importURL,
        importTitle,
        identity.csrf_token,
        crypto.randomUUID(),
        importDuplicate,
      );
      setImportURL("");
      setImportTitle("");
      setImportDuplicate(false);
      setImportOpen(false);
      setImportMessage(`已抓取并入队：${res.title}`);
      await queryClient.invalidateQueries({
        queryKey: ["documents", identity.tenant_id, identity.user_id, id],
      });
    } catch (err) {
      setImportError(
        err instanceof ApiError && err.status === 403
          ? "目标地址被安全策略拦截（禁止内网/本机地址）。"
          : "URL 导入失败，请检查链接或格式后重试。",
      );
    } finally {
      setImportingURL(false);
    }
  }

  async function removeDocument(documentId: number, title: string) {
    if (deleting !== null || uploading || retrying !== null) return;
    setDeleting(documentId);
    setDeleteMessage("");
    setDeleteError("");
    try {
      await deleteDocument(id, documentId, identity.csrf_token);
      setSubmittedSearch("");
      queryClient.removeQueries({ queryKey: ["search"] });
      if (preview?.id === documentId) setPreview(null);
      setDeleteMessage(`已移除 ${title}，后台正在清理原文件。`);
      void queryClient.invalidateQueries({ queryKey: ["documents"] });
      void queryClient.invalidateQueries({ queryKey: ["purge-jobs"] });
    } catch (cause) {
      if (statusIs(cause, 401)) {
        returnToLogin();
        return;
      }
      if (statusIs(cause, 403) || statusIs(cause, 404)) {
        void queryClient.invalidateQueries({ queryKey: ["documents"] });
      }
      setDeleteError("删除未完成，请刷新文档列表后重试。");
    } finally {
      setDeleting(null);
    }
  }

  async function retryDocument(jobId: number, title: string) {
    if (retrying !== null || deleting !== null || uploading) return;
    setRetrying(jobId);
    setRetryMessage("");
    setRetryError("");
    try {
      await retryJob(jobId, identity.csrf_token);
      setRetryMessage(`已重新排队 ${title}，等待后台处理。`);
      await queryClient.invalidateQueries({ queryKey: ["documents"] });
    } catch (cause) {
      if (statusIs(cause, 401)) {
        returnToLogin();
        return;
      }
      void queryClient.invalidateQueries({ queryKey: ["documents"] });
      setRetryError("重试未确认，请刷新文档列表查看当前状态。");
    } finally {
      setRetrying(null);
    }
  }

  async function retryPurgeJob(jobId: number, label: string) {
    if (retryingPurge !== null || deleting !== null || uploading) return;
    setRetryingPurge(jobId);
    setPurgeMessage("");
    setPurgeError("");
    try {
      await retryJob(jobId, identity.csrf_token);
      setPurgeMessage(`已重新排队清理 ${label}，等待后台处理。`);
      await queryClient.invalidateQueries({ queryKey: ["purge-jobs"] });
    } catch (cause) {
      if (statusIs(cause, 401)) {
        returnToLogin();
        return;
      }
      void queryClient.invalidateQueries({ queryKey: ["purge-jobs"] });
      setPurgeError("清理重试未确认，请刷新清理任务列表查看当前状态。");
    } finally {
      setRetryingPurge(null);
    }
  }

  if (
    statusIs(base.error, 401) ||
    statusIs(documents.error, 401) ||
    statusIs(search.error, 401)
  ) {
    return (
      <section className="py-6">
        <p role="alert">登录已过期，请重新登录。</p>
        <Button className="mt-4" variant="outline" onClick={returnToLogin}>
          重新登录
        </Button>
      </section>
    );
  }
  if (
    statusIs(base.error, 404) ||
    statusIs(documents.error, 404) ||
    statusIs(search.error, 403) ||
    statusIs(search.error, 404)
  ) {
    return (
      <section className="py-6">
        <p role="alert">知识库不存在或无权访问。</p>
        <Link className="mt-4 inline-block text-sm underline" to="/">
          返回知识库
        </Link>
      </section>
    );
  }
  if (base.isPending || documents.isPending) {
    return (
      <p className="py-6 text-sm text-muted-foreground" role="status">
        正在加载文档
      </p>
    );
  }
  if (base.isError || documents.isError) {
    return (
      <section className="py-6">
        <p role="alert">无法加载知识库文档。</p>
        <Button
          className="mt-4"
          variant="outline"
          onClick={() => {
            void base.refetch();
            void documents.refetch();
          }}
        >
          重试
        </Button>
      </section>
    );
  }

  const role = base.data.role;
  const active = base.data.status === "active";
  const canEdit = active && base.data.role !== "viewer";
  const busy = uploading || deleting !== null || retrying !== null;
  const stalePreview =
    preview?.fromList === true &&
    !documents.data.items.some(
      (item) =>
        item.id === preview.id && item.current_version_id === preview.versionId,
    );

  const uploadDialog = (
    <Dialog
      open={uploadOpen}
      onOpenChange={(open) => {
        if (uploading) return;
        setUploadOpen(open);
        if (!open) {
          resetUpload();
          setVersionTarget(null);
        }
      }}
      title={versionTarget ? `上传新版本 · ${versionTarget.title}` : "上传文档"}
    >
      <form className="space-y-4" onSubmit={submitUpload}>
        <Field
          htmlFor="document-upload"
          label={versionTarget ? "选择新版本文件" : "选择文件"}
          hint={
            versionTarget
              ? "新版本须与原文档格式相同；处理期间继续使用当前生效版本。"
              : "支持 CSV、Markdown、TXT、DOCX、XLSX、PPTX、HTML、PDF，单文件不超过 200 MiB。上传后进入处理队列。"
          }
        >
          <Input
            id="document-upload"
            type="file"
            accept=".csv,.md,.markdown,.txt,.docx,.xlsx,.pptx,.html,.htm,.pdf"
            disabled={uploading}
            onChange={(event) => {
              setFile(event.currentTarget.files?.[0] ?? null);
              setConfirmFirstRowHeaders(false);
              uploadKey.current = null;
              setUploadError("");
            }}
          />
        </Field>
        <div className="space-y-2.5">
          {file && /\.(csv|docx|xlsx|pdf)$/i.test(file.name) && (
            <Label className="flex items-start gap-2 text-sm">
              <Checkbox
                className="mt-0.5"
                checked={confirmFirstRowHeaders}
                disabled={uploading}
                onCheckedChange={(checked) => {
                  setConfirmFirstRowHeaders(checked);
                  uploadKey.current = null;
                  setUploadError("");
                }}
              />
              <span>
                确认此文件中所有表格的首行都是表头；未确认的表格无法入库。
              </span>
            </Label>
          )}
          <Label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={allowDuplicate}
              disabled={uploading}
              onCheckedChange={(checked) => {
                setAllowDuplicate(checked);
                uploadKey.current = null;
                setUploadError("");
              }}
            />
            仍导入相同内容
          </Label>
        </div>
        {uploadError && <Alert variant="danger">{uploadError}</Alert>}
        <div className="flex justify-end gap-2 border-t pt-4">
          <Button
            type="button"
            variant="outline"
            disabled={uploading}
            onClick={() => {
              setUploadOpen(false);
              resetUpload();
              setVersionTarget(null);
            }}
          >
            取消
          </Button>
          <Button type="submit" disabled={!file || busy}>
            {uploading ? "正在上传" : versionTarget ? "上传新版本" : "上传"}
          </Button>
        </div>
      </form>
    </Dialog>
  );

  const importDialog = (
    <Dialog
      open={importOpen}
      onOpenChange={(open) => {
        if (importingURL) return;
        setImportOpen(open);
        if (!open) setImportError("");
      }}
      title="从 URL 导入"
      description="抓取网页或在线文档后进入处理队列；内网与本机地址会被拦截。"
    >
      <form
        className="space-y-4"
        onSubmit={(event) => void submitImport(event)}
      >
        <Field htmlFor="import-url" label="导入 URL">
          <Input
            id="import-url"
            type="url"
            required
            placeholder="https://example.com/guide.html"
            value={importURL}
            disabled={importingURL}
            onChange={(e) => setImportURL(e.target.value)}
          />
        </Field>
        <Field htmlFor="import-title" label="文档标题（可选）">
          <Input
            id="import-title"
            type="text"
            maxLength={200}
            placeholder="默认从 URL 推导"
            value={importTitle}
            disabled={importingURL}
            onChange={(e) => setImportTitle(e.target.value)}
          />
        </Field>
        <Label className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={importDuplicate}
            disabled={importingURL}
            onCheckedChange={(checked) => setImportDuplicate(checked)}
          />
          仍导入相同内容
        </Label>
        {importError && <Alert variant="danger">{importError}</Alert>}
        <div className="flex justify-end gap-2 border-t pt-4">
          <Button
            type="button"
            variant="outline"
            disabled={importingURL}
            onClick={() => setImportOpen(false)}
          >
            取消
          </Button>
          <Button type="submit" disabled={!importURL.trim() || importingURL}>
            {importingURL ? "正在导入" : "导入"}
          </Button>
        </div>
      </form>
    </Dialog>
  );

  const notices = [
    { text: uploadMessage, error: false },
    { text: importMessage, error: false },
    { text: deleteMessage, error: false },
    { text: deleteError, error: true },
    { text: retryMessage, error: false },
    { text: retryError, error: true },
  ].filter((notice) => notice.text);

  function documentRow(item: DocumentPage["items"][number]) {
    const failed = item.latest_status === "failed";
    return (
      <li
        className="flex flex-col gap-3 p-4 @2xl:flex-row @2xl:items-start @2xl:justify-between"
        key={item.id}
      >
        <div className="flex min-w-0 gap-3">
          <FileText
            aria-hidden="true"
            className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground"
          />
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="min-w-0 break-words font-medium">{item.title}</h3>
              <Badge
                title="最新版本状态"
                variant={statusVariants[item.latest_status] ?? "neutral"}
              >
                {processing.includes(item.latest_status) && (
                  <Loader2
                    aria-hidden="true"
                    className="h-3 w-3 animate-spin"
                  />
                )}
                {statuses[item.latest_status]}
              </Badge>
            </div>
            <p className="flex flex-wrap gap-x-1.5 text-xs text-muted-foreground">
              <span>
                {item.current_version_id === null
                  ? "尚无生效版本"
                  : item.current_version_id === item.latest_version_id
                    ? "当前版本已生效"
                    : "已有生效版本"}
              </span>
              {failed && item.failure_code && (
                <>
                  <span aria-hidden="true">·</span>
                  <span className="text-danger">
                    {failures[item.failure_code] ?? "处理未完成"}
                  </span>
                </>
              )}
            </p>
            {failed && (
              <p className="text-xs text-muted-foreground">
                {role === "viewer"
                  ? "请联系管理员处理。"
                  : item.retryable
                    ? "恢复依赖后可重新处理。"
                    : "可上传修订版本，或联系管理员处理。"}
              </p>
            )}
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-1.5 pl-7 @2xl:pl-0">
          {active && item.current_version_id !== null && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => {
                const versionId = item.current_version_id;
                if (versionId !== null)
                  setPreview({
                    id: item.id,
                    title: item.title,
                    versionId,
                    fromList: true,
                  });
              }}
            >
              <BookOpen aria-hidden="true" className="h-3.5 w-3.5" />
              查看目录
            </Button>
          )}
          {canEdit && item.retryable && item.latest_job_id !== null && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => {
                const jobId = item.latest_job_id;
                if (jobId !== null) void retryDocument(jobId, item.title);
              }}
            >
              <RotateCcw aria-hidden="true" className="h-3.5 w-3.5" />
              {retrying === item.latest_job_id ? "正在重新排队" : "重新处理"}
            </Button>
          )}
          {canEdit && (
            <>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => openUpload({ id: item.id, title: item.title })}
              >
                <Upload aria-hidden="true" className="h-3.5 w-3.5" />
                上传新版本
              </Button>
              <DocumentACLButton
                identity={identity}
                kbId={id}
                documentId={item.id}
                documentTitle={item.title}
                role={role}
              />
              <ConfirmAction
                title={`删除“${item.title}”？`}
                description="文档将立即从检索结果中移除，原文件在后台清理。"
                onConfirm={() => void removeDocument(item.id, item.title)}
                trigger={
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-danger hover:bg-danger/10"
                    disabled={busy}
                  >
                    <Trash2 aria-hidden="true" className="h-3.5 w-3.5" />
                    {deleting === item.id ? "正在删除" : "删除文档"}
                  </Button>
                }
              />
            </>
          )}
        </div>
      </li>
    );
  }

  const documentsPane = (
    <div className="space-y-4">
      {notices.map((notice) => (
        <Alert key={notice.text} variant={notice.error ? "danger" : "info"}>
          {notice.text}
        </Alert>
      ))}
      {documents.data.items.length === 0 ? (
        <EmptyState
          title={cursor === 0 ? "此知识库暂无文档。" : "此页没有更多文档。"}
          action={
            canEdit && cursor === 0 ? (
              <Button size="sm" onClick={() => openUpload(null)}>
                <Upload aria-hidden="true" className="h-3.5 w-3.5" />
                上传第一份文档
              </Button>
            ) : undefined
          }
        />
      ) : (
        <ul className="@container divide-y rounded-lg border">
          {documents.data.items.map(documentRow)}
        </ul>
      )}
      {(cursor > 0 || documents.data.next_cursor !== null) && (
        <nav className="flex gap-2" aria-label="文档分页">
          {cursor > 0 && (
            <Button asChild size="xs" variant="outline">
              <Link
                to="/kb/$kbId/documents"
                params={{ kbId: String(id) }}
                search={{ cursor: 0 }}
              >
                返回第一页
              </Link>
            </Button>
          )}
          {documents.data.next_cursor !== null && (
            <Button asChild size="xs" variant="outline">
              <Link
                to="/kb/$kbId/documents"
                params={{ kbId: String(id) }}
                search={{ cursor: documents.data.next_cursor }}
              >
                下一页
                <ChevronRight aria-hidden="true" className="h-3.5 w-3.5" />
              </Link>
            </Button>
          )}
        </nav>
      )}
      {base.data.role !== "viewer" && (
        <section
          className="space-y-3 border-t pt-4"
          aria-labelledby="purge-jobs-title"
        >
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h3 id="purge-jobs-title" className="text-sm font-semibold">
              清理任务
            </h3>
            <span className="text-xs text-muted-foreground">
              已删除文档与被替代版本的后台清理
            </span>
          </div>
          {purgeMessage && <Alert>{purgeMessage}</Alert>}
          {purgeError && <Alert variant="danger">{purgeError}</Alert>}
          {purgeJobs.isPending ? (
            <p className="text-sm text-muted-foreground" role="status">
              正在加载清理任务
            </p>
          ) : purgeJobs.isError ? (
            <p className="text-sm text-danger" role="alert">
              无法加载清理任务。
            </p>
          ) : purgeJobs.data.items.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {purgeCursor === 0 ? "暂无清理任务。" : "此页没有更多清理任务。"}
            </p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {purgeJobs.data.items.map((item) => {
                const label =
                  item.document_title ?? `文档 #${item.document_id}`;
                return (
                  <li
                    className="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
                    key={item.id}
                  >
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="break-words text-sm font-medium">
                          {label}
                        </span>
                        <span className="text-xs text-muted-foreground">
                          版本 #{item.version_id}
                        </span>
                        {item.document_deleted && <Badge>已删除</Badge>}
                      </div>
                      <p className="text-xs text-muted-foreground">
                        状态：{purgeStatuses[item.status] ?? item.status}（尝试{" "}
                        {item.attempts}/{item.max_attempts}）
                      </p>
                      {item.failure_code && (
                        <p className="text-xs text-danger">
                          失败原因：
                          {failures[item.failure_code] ?? item.failure_code}
                        </p>
                      )}
                    </div>
                    {active && item.retryable && (
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={
                          retryingPurge !== null ||
                          deleting !== null ||
                          uploading
                        }
                        onClick={() => void retryPurgeJob(item.id, label)}
                      >
                        {retryingPurge === item.id
                          ? "正在重新排队"
                          : "重试清理"}
                      </Button>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          {(purgeCursor > 0 || purgeJobs.data?.next_cursor != null) && (
            <nav className="flex gap-2" aria-label="清理任务分页">
              {purgeCursor > 0 && (
                <Button
                  type="button"
                  size="xs"
                  variant="outline"
                  onClick={() => setPurgeCursor(0)}
                >
                  返回第一页
                </Button>
              )}
              {purgeJobs.data?.next_cursor != null && (
                <Button
                  type="button"
                  size="xs"
                  variant="outline"
                  onClick={() => {
                    const next = purgeJobs.data?.next_cursor;
                    if (next !== null && next !== undefined)
                      setPurgeCursor(next);
                  }}
                >
                  下一页
                </Button>
              )}
            </nav>
          )}
        </section>
      )}
    </div>
  );

  const searchPane = (
    <div className="space-y-4">
      <form className="space-y-3" onSubmit={submitSearch}>
        <Label className="block text-xs font-medium" htmlFor="search-query">
          问题或关键词
        </Label>
        <div className="flex gap-2">
          <Input
            id="search-query"
            className="flex-1"
            maxLength={4096}
            value={searchText}
            onChange={(event) => setSearchText(event.currentTarget.value)}
          />
          <Button
            type="submit"
            className="shrink-0"
            disabled={!searchText.trim() || search.isFetching}
          >
            {search.isFetching ? (
              <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" />
            ) : (
              <Search aria-hidden="true" className="h-4 w-4" />
            )}
            {search.isFetching ? "正在检索" : "检索"}
          </Button>
        </div>
        <Collapsible className="rounded-lg border px-3 py-2 text-sm">
          <CollapsibleTrigger className="cursor-pointer text-xs">
            <span className="flex-1">重排配置</span>
            {rerankMode !== "inherit" && (
              <Badge variant="warning">
                {rerankMode === "off" ? "本次关闭" : "本次自定义"}
              </Badge>
            )}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-3 grid gap-3 pb-1">
              <Label className="space-y-1.5 text-xs font-medium">
                <span>重排方式</span>
                <Select
                  name="rerank-mode"
                  value={rerankMode}
                  onValueChange={(value) => setRerankMode(value)}
                >
                  <SelectItem value="inherit">使用知识库配置</SelectItem>
                  <SelectItem value="off">本次关闭重排</SelectItem>
                  <SelectItem value="custom">本次自定义重排</SelectItem>
                </Select>
              </Label>
              <fieldset
                disabled={rerankMode !== "custom"}
                className="grid gap-3 disabled:opacity-60"
              >
                <legend className="sr-only">自定义重排</legend>
                <Label className="space-y-1.5 text-xs font-medium">
                  <span>模型提供方</span>
                  <Select
                    name="rerank-provider"
                    placeholder="选择已配置提供方"
                    disabled={rerankMode !== "custom"}
                    required={rerankMode === "custom"}
                  >
                    {providers.data?.map((space) => (
                      <SelectItem key={space.id} value={space.provider_id}>
                        {space.provider_name}
                      </SelectItem>
                    ))}
                  </Select>
                </Label>
                <Label className="space-y-1.5 text-xs font-medium">
                  <span>重排模型</span>
                  <Input
                    name="rerank-model"
                    disabled={rerankMode !== "custom"}
                    required={rerankMode === "custom"}
                    maxLength={200}
                    placeholder="bge-reranker-v2-m3"
                  />
                </Label>
                <Label className="flex items-center gap-2 text-sm">
                  <Checkbox
                    name="rerank-fallback"
                    disabled={rerankMode !== "custom"}
                  />
                  <span>服务不可用时允许返回未重排结果</span>
                </Label>
              </fieldset>
              {rerankMode === "custom" && providers.isError && (
                <p role="alert" className="text-xs text-danger">
                  模型提供方加载失败。
                </p>
              )}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </form>
      {search.isError && (
        <Alert variant="danger">{searchErrorMessage(search.error)}</Alert>
      )}
      {!search.data && !search.isError && !search.isFetching && (
        <p className="text-sm text-muted-foreground">
          检索结果与调试信息将在此显示。
        </p>
      )}
      {search.data && !search.isError && !search.isFetching && (
        <div className="space-y-3">
          <p className="flex flex-wrap gap-x-2 text-xs text-muted-foreground">
            <span>检索配置修订 {search.data.retrieval_revision}</span>
            {search.data.degraded.includes("rerank") && (
              <span className="text-amber-700 dark:text-amber-300">
                结果未经重排。
              </span>
            )}
          </p>
          {search.data.trace && <SearchTrace trace={search.data.trace} />}
          {search.data.status === "no_match" ? (
            <p role="status" className="text-sm">
              未找到匹配内容。
            </p>
          ) : (
            <ol className="space-y-3" aria-label="检索结果">
              {search.data.evidence.map((item, index) => (
                <li className="space-y-2 rounded-lg border p-3" key={item.id}>
                  <div className="flex items-start gap-2">
                    <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-muted text-[11px] font-medium text-muted-foreground">
                      {index + 1}
                    </span>
                    <div className="min-w-0 flex-1">
                      <h3 className="break-words text-sm font-medium">
                        {item.document.title}
                      </h3>
                      <p className="text-xs text-muted-foreground">
                        <HeadingPath parts={item.section.heading_path} />
                        {item.pages &&
                          ` · 第 ${item.pages[0]}—${item.pages[1]} 页`}
                      </p>
                    </div>
                  </div>
                  <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">
                    {item.content}
                  </p>
                  <Button
                    type="button"
                    size="xs"
                    variant="outline"
                    onClick={() =>
                      setPreview({
                        id: item.document.id,
                        title: item.document.title,
                        versionId: item.document.version_id,
                        initialSection: item.section.id,
                      })
                    }
                  >
                    <BookOpen aria-hidden="true" className="h-3.5 w-3.5" />
                    查看章节
                  </Button>
                </li>
              ))}
            </ol>
          )}
        </div>
      )}
    </div>
  );

  return (
    <section className="space-y-5">
      <SectionHeader
        title={base.data.name}
        description={`编号 ${base.data.id} · ${roleLabels[base.data.role] ?? base.data.role}`}
      />
      {!active && <Alert variant="warning">知识库已归档，文档仅供查看。</Alert>}
      <WorkspaceSplit
        className="lg:min-h-[calc(100dvh-11rem)]"
        panes={[
          {
            id: "kb-documents",
            title: "文档",
            content: documentsPane,
            actions: canEdit ? (
              <>
                <Button
                  size="xs"
                  variant="outline"
                  onClick={() => {
                    setImportError("");
                    setImportMessage("");
                    setImportOpen(true);
                  }}
                >
                  <Link2 aria-hidden="true" className="h-3.5 w-3.5" />从 URL
                  导入
                </Button>
                <Button size="xs" onClick={() => openUpload(null)}>
                  <Upload aria-hidden="true" className="h-3.5 w-3.5" />
                  上传文档
                </Button>
              </>
            ) : undefined,
          },
          ...(active
            ? [
                {
                  id: "kb-search",
                  title: "检索测试",
                  defaultWidth: 440,
                  minWidth: 340,
                  maxWidth: 760,
                  collapsible: true,
                  content: searchPane,
                },
              ]
            : []),
        ]}
        storageKey={`tuskrag.documents.${identity.tenant_id}`}
      />
      {canEdit && uploadDialog}
      {canEdit && importDialog}
      {preview && (
        <Preview
          identity={identity}
          target={preview}
          stale={stalePreview}
          onClose={() => setPreview(null)}
        />
      )}
    </section>
  );
}

function searchErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === "egress_denied") return "模型调用受到出域策略限制。";
    if (error.code === "unsupported_embedding")
      return "当前嵌入空间不支持此检索方式。";
    if (error.code === "model_invalid_response")
      return "嵌入模型返回的数据不符合要求。";
    if (error.code === "model_unavailable")
      return "嵌入模型暂不可用，请稍后重试。";
    if (error.code === "dependency_unavailable")
      return "检索索引暂不可用，请联系管理员。";
    if (error.code === "invalid_request") return "查询内容不符合要求。";
    if (error.code === "configuration_changed")
      return "配置已变化，请重新检索。";
  }
  return "检索失败，请稍后重试。";
}

function uploadErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === "duplicate_content")
      return "此内容已存在；如与其他文档重复，可勾选允许重复。";
    if (error.code === "version_format_mismatch")
      return "新版本的文件格式须与原文档相同。";
    if (error.code === "unsupported_format") return "文件格式暂不支持。";
    if (error.code === "limit_exceeded") return "文件超过大小限制。";
    if (error.code === "idempotency_conflict")
      return "请求编号冲突，请重新选择文件。";
    if (error.status === 403 || error.status === 404)
      return "无权上传到此知识库。";
    if (error.status === 503) return "文档存储不可用，请联系管理员。";
  }
  return "上传未完成，可重试同一文件；如已接收，请在文档列表查看。";
}
