import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ApiError,
  deleteDocument,
  getKnowledgeBase,
  type Identity,
  listDocuments,
  listEmbeddingSpaces,
  listPurgeJobs,
  retryJob,
  type SearchOptions,
  searchKnowledgeBase,
  uploadDocument,
  uploadDocumentVersion,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { type FormEvent, useRef, useState } from "react";
import type { PreviewTarget } from "./DocumentPreview";
import { DocumentACLButton, UserGroupsPanel } from "./DocumentACL";
import { KnowledgeConfigPanel } from "./KnowledgeConfig";
import { KnowledgeMembersPanel } from "./KnowledgeMembers";
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
  const uploadInput = useRef<HTMLInputElement | null>(null);
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
        ["queued", "parsing", "embedding", "indexing", "purging"].includes(
          item.latest_status,
        ),
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
    const form = event.currentTarget;
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
      form.reset();
      setFile(null);
      setVersionTarget(null);
      setAllowDuplicate(false);
      setConfirmFirstRowHeaders(false);
      uploadKey.current = null;
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

  async function removeDocument(documentId: number, title: string) {
    if (
      deleting !== null ||
      uploading ||
      retrying !== null ||
      !window.confirm(
        `删除“${title}”？文档将立即从检索结果中移除，原文件在后台清理。`,
      )
    )
      return;
    setDeleting(documentId);
    setDeleteMessage("");
    setDeleteError("");
    try {
      await deleteDocument(id, documentId, identity.csrf_token);
      setSubmittedSearch("");
      queryClient.removeQueries({ queryKey: ["search"] });
      if (preview?.id === documentId) setPreview(null);
      if (versionTarget?.id === documentId) {
        setVersionTarget(null);
        setFile(null);
        setAllowDuplicate(false);
        setConfirmFirstRowHeaders(false);
        uploadKey.current = null;
        if (uploadInput.current) uploadInput.current.value = "";
      }
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
      <section className="py-12">
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
      <section className="py-12">
        <p role="alert">知识库不存在或无权访问。</p>
        <Link className="mt-4 inline-block text-sm underline" to="/">
          返回知识库
        </Link>
      </section>
    );
  }
  if (base.isPending || documents.isPending) {
    return (
      <p className="py-12 text-sm text-muted-foreground" role="status">
        正在加载文档
      </p>
    );
  }
  if (base.isError || documents.isError) {
    return (
      <section className="py-12">
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
  return (
    <section className="py-12 sm:py-16">
      <Link className="text-sm text-muted-foreground underline" to="/">
        返回知识库
      </Link>
      <h1 className="mt-5 break-words text-3xl font-semibold tracking-tight">
        {base.data.name}
      </h1>
      <KnowledgeConfigPanel identity={identity} base={base.data} />
      <KnowledgeMembersPanel identity={identity} base={base.data} />
      <UserGroupsPanel identity={identity} />
      {(base.data.role === "owner" || base.data.role === "admin") && (
        <Link
          className="mt-4 mr-5 inline-block text-sm underline"
          to="/kb/$kbId/rebuild"
          params={{ kbId: String(id) }}
          search={{ job: 0 }}
        >
          嵌入重建
        </Link>
      )}
      {(base.data.role === "owner" || base.data.role === "admin") && (
        <Link
          className="mt-4 inline-block text-sm underline"
          to="/kb/$kbId/eval"
          params={{ kbId: String(id) }}
          search={{ cursor: "", digest: "", row: 0 }}
        >
          评测报告
        </Link>
      )}
      {base.data.status === "active" && (
        <section className="mt-8 rounded-xl border p-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 className="text-lg font-medium">检索知识库</h2>
            <Link
              className="text-sm underline"
              to="/kb/$kbId/answer"
              params={{ kbId: String(id) }}
            >
              向知识库提问
            </Link>
          </div>
          <form className="mt-4 space-y-3" onSubmit={submitSearch}>
            <label className="block text-sm font-medium" htmlFor="search-query">
              问题或关键词
            </label>
            <input
              id="search-query"
              className="w-full rounded-md border bg-background px-3 py-2 text-sm"
              maxLength={4096}
              value={searchText}
              onChange={(event) => setSearchText(event.currentTarget.value)}
            />
            <Button
              type="submit"
              disabled={!searchText.trim() || search.isFetching}
            >
              {search.isFetching ? "正在检索" : "检索"}
            </Button>
            <details className="text-sm">
              <summary className="cursor-pointer">重排配置</summary>
              <div className="mt-3 grid gap-3 sm:grid-cols-2">
                <label>
                  重排方式
                  <select
                    name="rerank-mode"
                    value={rerankMode}
                    onChange={(event) =>
                      setRerankMode(event.currentTarget.value)
                    }
                    className="mt-1 block w-full rounded-md border bg-background p-2"
                  >
                    <option value="inherit">使用知识库配置</option>
                    <option value="off">本次关闭重排</option>
                    <option value="custom">本次自定义重排</option>
                  </select>
                </label>
                <label>
                  <input
                    type="checkbox"
                    name="rerank-fallback"
                    disabled={rerankMode !== "custom"}
                  />{" "}
                  服务不可用时允许返回未重排结果
                </label>
                <label>
                  模型提供方
                  <select
                    name="rerank-provider"
                    disabled={rerankMode !== "custom"}
                    required={rerankMode === "custom"}
                    className="mt-1 block w-full rounded-md border bg-background p-2"
                  >
                    <option value="">选择已配置提供方</option>
                    {providers.data?.map((space) => (
                      <option key={space.id} value={space.provider_id}>
                        {space.provider_name}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  重排模型
                  <input
                    name="rerank-model"
                    disabled={rerankMode !== "custom"}
                    required={rerankMode === "custom"}
                    maxLength={200}
                    placeholder="bge-reranker-v2-m3"
                    className="mt-1 block w-full rounded-md border bg-background p-2"
                  />
                </label>
              </div>
              {rerankMode === "custom" && providers.isError && (
                <p role="alert">模型提供方加载失败。</p>
              )}
            </details>
          </form>
          {search.isError && (
            <p role="alert" className="mt-4 text-sm">
              {searchErrorMessage(search.error)}
            </p>
          )}
          {search.data && !search.isError && !search.isFetching && (
            <div className="mt-5 space-y-3">
              <p className="text-sm text-muted-foreground">
                检索配置修订 {search.data.retrieval_revision}
              </p>
              {search.data.degraded.includes("rerank") && (
                <p className="text-sm text-muted-foreground">结果未经重排。</p>
              )}
              {search.data.trace && <SearchTrace trace={search.data.trace} />}
              {search.data.status === "no_match" ? (
                <p role="status" className="text-sm">
                  未找到匹配内容。
                </p>
              ) : (
                <ul className="divide-y rounded-md border">
                  {search.data.evidence.map((item) => (
                    <li className="space-y-2 p-4" key={item.id}>
                      <h3 className="break-words font-medium">
                        {item.document.title}
                      </h3>
                      <p className="text-xs text-muted-foreground">
                        {item.section.heading_path.join(" › ")}
                        {item.pages &&
                          ` · 第 ${item.pages[0]}—${item.pages[1]} 页`}
                      </p>
                      <p className="whitespace-pre-wrap break-words text-sm">
                        {item.content}
                      </p>
                      <Button
                        type="button"
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
                        查看章节
                      </Button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </section>
      )}
      {base.data.status === "active" && base.data.role !== "viewer" && (
        <form
          className="mt-8 space-y-3 rounded-xl border p-5"
          onSubmit={submitUpload}
        >
          <label
            className="block text-sm font-medium"
            htmlFor="document-upload"
          >
            {versionTarget
              ? `上传 ${versionTarget.title} 的新版本`
              : "上传文档"}
          </label>
          <input
            id="document-upload"
            ref={uploadInput}
            className="block w-full text-sm"
            type="file"
            accept=".csv,.md,.markdown,.txt,.docx,.xlsx,.pdf"
            disabled={uploading}
            onChange={(event) => {
              setFile(event.currentTarget.files?.[0] ?? null);
              setConfirmFirstRowHeaders(false);
              uploadKey.current = null;
              setUploadError("");
              setUploadMessage("");
            }}
          />
          <p className="text-sm text-muted-foreground">
            支持 CSV、Markdown、TXT、DOCX、XLSX、数字 PDF，单文件不超过 200
            MiB。上传后进入处理队列。
          </p>
          {versionTarget && (
            <p className="text-sm text-muted-foreground">
              新版本须与原文档格式相同；处理期间继续使用当前生效版本。
            </p>
          )}
          {file && /\.(csv|docx|xlsx|pdf)$/i.test(file.name) && (
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={confirmFirstRowHeaders}
                disabled={uploading}
                onChange={(event) => {
                  setConfirmFirstRowHeaders(event.currentTarget.checked);
                  uploadKey.current = null;
                  setUploadError("");
                }}
              />
              <span>
                确认此文件中所有表格的首行都是表头；未确认的表格无法入库。
              </span>
            </label>
          )}
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={allowDuplicate}
              disabled={uploading}
              onChange={(event) => {
                setAllowDuplicate(event.currentTarget.checked);
                uploadKey.current = null;
                setUploadError("");
              }}
            />
            仍导入相同内容
          </label>
          {uploadError && (
            <p role="alert" className="text-sm">
              {uploadError}
            </p>
          )}
          {uploadMessage && (
            <p role="status" className="text-sm">
              {uploadMessage}
            </p>
          )}
          <Button
            type="submit"
            disabled={
              !file || uploading || deleting !== null || retrying !== null
            }
          >
            {uploading ? "正在上传" : versionTarget ? "上传新版本" : "上传"}
          </Button>
          {versionTarget && (
            <Button
              type="button"
              variant="outline"
              disabled={uploading}
              onClick={() => {
                setVersionTarget(null);
                setFile(null);
                setAllowDuplicate(false);
                setConfirmFirstRowHeaders(false);
                setUploadError("");
                uploadKey.current = null;
                if (uploadInput.current) uploadInput.current.value = "";
              }}
            >
              取消更新
            </Button>
          )}
        </form>
      )}
      <h2 className="mt-10 text-lg font-medium">文档</h2>
      {deleteMessage && (
        <p role="status" className="mt-3 text-sm">
          {deleteMessage}
        </p>
      )}
      {deleteError && (
        <p role="alert" className="mt-3 text-sm">
          {deleteError}
        </p>
      )}
      {retryMessage && (
        <p role="status" className="mt-3 text-sm">
          {retryMessage}
        </p>
      )}
      {retryError && (
        <p role="alert" className="mt-3 text-sm">
          {retryError}
        </p>
      )}
      {documents.data.items.length === 0 ? (
        <p className="mt-4 rounded-xl border border-dashed p-6 text-sm text-muted-foreground">
          {cursor === 0 ? "此知识库暂无文档。" : "此页没有更多文档。"}
        </p>
      ) : (
        <ul className="mt-4 divide-y rounded-xl border">
          {documents.data.items.map((item) => (
            <li className="space-y-2 p-5" key={item.id}>
              <div className="flex flex-wrap items-start justify-between gap-2">
                <h3 className="min-w-0 break-words font-medium">
                  {item.title}
                </h3>
                <span className="text-sm text-muted-foreground">
                  最新版本：{statuses[item.latest_status]}
                </span>
              </div>
              <p className="text-sm text-muted-foreground">
                {item.current_version_id === null
                  ? "尚无生效版本"
                  : item.current_version_id === item.latest_version_id
                    ? "当前版本已生效"
                    : "已有生效版本"}
              </p>
              {item.latest_status === "failed" && item.failure_code && (
                <p className="text-sm text-muted-foreground">
                  {failures[item.failure_code] ?? "处理未完成"}
                </p>
              )}
              {item.latest_status === "failed" && (
                <p className="text-sm text-muted-foreground">
                  {base.data.role === "viewer"
                    ? "请联系管理员处理。"
                    : item.retryable
                      ? "恢复依赖后可重新处理。"
                      : "可上传修订版本，或联系管理员处理。"}
                </p>
              )}
              {base.data.status === "active" &&
                item.current_version_id !== null && (
                  <Button
                    type="button"
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
                    查看目录
                  </Button>
                )}
              {base.data.status === "active" && base.data.role !== "viewer" && (
                <div className="flex flex-wrap gap-2">
                  {item.retryable && item.latest_job_id !== null && (
                    <Button
                      type="button"
                      variant="outline"
                      disabled={
                        uploading || deleting !== null || retrying !== null
                      }
                      onClick={() => {
                        const jobId = item.latest_job_id;
                        if (jobId !== null)
                          void retryDocument(jobId, item.title);
                      }}
                    >
                      {retrying === item.latest_job_id
                        ? "正在重新排队"
                        : "重新处理"}
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="outline"
                    disabled={
                      uploading || deleting !== null || retrying !== null
                    }
                    onClick={() => {
                      setVersionTarget({ id: item.id, title: item.title });
                      setFile(null);
                      setAllowDuplicate(false);
                      setConfirmFirstRowHeaders(false);
                      setUploadError("");
                      setUploadMessage("");
                      uploadKey.current = null;
                      if (uploadInput.current) {
                        uploadInput.current.value = "";
                        uploadInput.current.focus();
                      }
                    }}
                  >
                    上传新版本
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={
                      uploading || deleting !== null || retrying !== null
                    }
                    onClick={() => void removeDocument(item.id, item.title)}
                  >
                    {deleting === item.id ? "正在删除" : "删除文档"}
                  </Button>
                  <DocumentACLButton
                    identity={identity}
                    kbId={id}
                    documentId={item.id}
                    documentTitle={item.title}
                    role={base.data.role}
                  />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {preview &&
        (preview.fromList &&
        !documents.data.items.some(
          (item) =>
            item.id === preview.id &&
            item.current_version_id === preview.versionId,
        ) ? (
          <section className="mt-6 rounded-xl border p-5">
            <p role="status" className="text-sm">
              文档已更新或不再可见，请重新打开目录。
            </p>
            <Button
              className="mt-3"
              type="button"
              variant="outline"
              onClick={() => setPreview(null)}
            >
              关闭预览
            </Button>
          </section>
        ) : (
          <Preview
            identity={identity}
            target={preview}
            onClose={() => setPreview(null)}
          />
        ))}
      <nav className="mt-5 flex gap-5 text-sm" aria-label="文档分页">
        {cursor > 0 && (
          <Link
            className="underline"
            to="/kb/$kbId/documents"
            params={{ kbId: String(id) }}
            search={{ cursor: 0 }}
          >
            返回第一页
          </Link>
        )}
        {documents.data.next_cursor !== null && (
          <Link
            className="underline"
            to="/kb/$kbId/documents"
            params={{ kbId: String(id) }}
            search={{ cursor: documents.data.next_cursor }}
          >
            下一页
          </Link>
        )}
      </nav>
      {base.data.role !== "viewer" && (
        <section className="mt-8 border-t pt-6" aria-label="清理任务">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h2 className="text-lg font-medium">清理任务</h2>
            <span className="text-xs text-muted-foreground">
              展示已删除文档与被替代版本的后台清理状态
            </span>
          </div>
          {purgeMessage && (
            <p className="mt-2 text-sm text-muted-foreground" role="status">
              {purgeMessage}
            </p>
          )}
          {purgeError && (
            <p className="mt-2 text-sm text-destructive" role="alert">
              {purgeError}
            </p>
          )}
          {purgeJobs.isPending ? (
            <p className="mt-3 text-sm text-muted-foreground" role="status">
              正在加载清理任务
            </p>
          ) : purgeJobs.isError ? (
            <p className="mt-3 text-sm text-destructive" role="alert">
              无法加载清理任务。
            </p>
          ) : purgeJobs.data.items.length === 0 ? (
            <p className="mt-3 rounded-xl border border-dashed p-5 text-sm text-muted-foreground">
              {purgeCursor === 0 ? "暂无清理任务。" : "此页没有更多清理任务。"}
            </p>
          ) : (
            <ul className="mt-3 divide-y rounded-xl border">
              {purgeJobs.data.items.map((item) => {
                const label =
                  item.document_title ?? `文档 #${item.document_id}`;
                return (
                  <li
                    className="flex flex-wrap items-center justify-between gap-3 p-4"
                    key={item.id}
                  >
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium break-words">
                          {label}
                        </span>
                        <span className="text-xs text-muted-foreground">
                          版本 #{item.version_id}
                        </span>
                        {item.document_deleted && (
                          <span className="rounded border px-1.5 py-0.5 text-xs text-muted-foreground">
                            已删除
                          </span>
                        )}
                      </div>
                      <p className="text-sm text-muted-foreground">
                        状态：{purgeStatuses[item.status] ?? item.status}（尝试{" "}
                        {item.attempts}/{item.max_attempts}）
                      </p>
                      {item.failure_code && (
                        <p className="text-sm text-muted-foreground">
                          失败原因：
                          {failures[item.failure_code] ?? item.failure_code}
                        </p>
                      )}
                    </div>
                    {base.data.status === "active" && item.retryable && (
                      <Button
                        type="button"
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
          {(purgeCursor > 0 || purgeJobs.data?.next_cursor !== null) && (
            <nav className="mt-3 flex gap-4 text-sm" aria-label="清理任务分页">
              {purgeCursor > 0 && (
                <button
                  type="button"
                  className="underline"
                  onClick={() => setPurgeCursor(0)}
                >
                  返回第一页
                </button>
              )}
              {purgeJobs.data?.next_cursor !== null &&
                purgeJobs.data?.next_cursor !== undefined && (
                  <button
                    type="button"
                    className="underline"
                    onClick={() => {
                      const next = purgeJobs.data?.next_cursor;
                      if (next !== null && next !== undefined)
                        setPurgeCursor(next);
                    }}
                  >
                    下一页
                  </button>
                )}
            </nav>
          )}
        </section>
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
