import { useQuery } from "@tanstack/react-query";
import {
  getDocumentContents,
  type Identity,
  readSection,
} from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { useEffect, useRef, useState } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

export type PreviewTarget = {
  id: number;
  versionId: number;
  title: string;
  initialSection?: number;
  includePrevious?: boolean;
  fromList?: boolean;
};

export function DocumentPreview({
  identity,
  target,
  onClose,
}: {
  identity: Identity;
  target: PreviewTarget;
  onClose: () => void;
}) {
  const [cursor, setCursor] = useState(0);
  const previewRef = useRef<HTMLElement | null>(null);
  const [sectionId, setSectionId] = useState<number | null>(
    target.initialSection ?? null,
  );
  useEffect(() => {
    previewRef.current?.scrollIntoView?.({ block: "start" });
    previewRef.current?.focus({ preventScroll: true });
  }, []);
  const contents = useQuery({
    queryKey: [
      "document-contents",
      identity.tenant_id,
      identity.user_id,
      target.id,
      target.versionId,
      cursor,
    ],
    queryFn: async ({ signal }) => {
      const result = await getDocumentContents(target.id, cursor, signal);
      if (result.document.version_id !== target.versionId)
        throw new Error("document version changed");
      return result;
    },
    retry: false,
    gcTime: 0,
  });
  const section = useQuery({
    queryKey: [
      "document-section",
      identity.tenant_id,
      identity.user_id,
      target.id,
      target.versionId,
      sectionId,
    ],
    queryFn: async ({ signal }) => {
      if (sectionId === null) throw new Error("section is not selected");
      const result = await readSection(sectionId, signal);
      if (
        result.document.id !== target.id ||
        result.document.version_id !== target.versionId
      ) {
        throw new Error("document version changed");
      }
      return result;
    },
    enabled: sectionId !== null,
    retry: false,
    gcTime: 0,
  });

  const current = section.data;
  const showContext =
    target.includePrevious === true && sectionId === target.initialSection;
  const previous = useQuery({
    queryKey: [
      "citation-previous",
      identity.tenant_id,
      identity.user_id,
      target.id,
      target.versionId,
      sectionId,
    ],
    queryFn: async ({ signal }) => {
      if (!current || current.section.ordinal <= 1) return null;
      const nearby = await getDocumentContents(
        target.id,
        current.section.ordinal - 2,
        signal,
      );
      if (nearby.document.version_id !== target.versionId)
        throw new Error("document version changed");
      const prior = nearby.sections.find(
        (item) =>
          item.ordinal === current.section.ordinal - 1 &&
          item.level === current.section.level &&
          JSON.stringify(item.heading_path) ===
            JSON.stringify(current.section.heading_path),
      );
      if (!prior) return null;
      const result = await readSection(prior.id, signal);
      if (
        result.document.id !== target.id ||
        result.document.version_id !== target.versionId
      )
        throw new Error("document version changed");
      return result;
    },
    enabled:
      showContext && !!current && !section.isError && !section.isFetching,
    retry: false,
    gcTime: 0,
  });

  return (
    <section
      ref={previewRef}
      tabIndex={-1}
      className="mt-6 space-y-4 rounded-xl border p-5"
      aria-label="文档目录与正文"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <h3 className="min-w-0 break-words text-lg font-medium">
          {target.title} · 目录
        </h3>
        <Button type="button" variant="outline" onClick={onClose}>
          关闭预览
        </Button>
      </div>
      {contents.isPending && (
        <p role="status" className="text-sm">
          正在加载目录
        </p>
      )}
      {contents.isError && (
        <p role="alert" className="text-sm">
          目录不可用，文档可能已更新或权限已变化。
        </p>
      )}
      {contents.data && !contents.isError && (
        <>
          {contents.data.sections.length === 0 ? (
            <p className="text-sm text-muted-foreground">此页没有目录项。</p>
          ) : (
            <ul className="space-y-2">
              {contents.data.sections.map((item) => (
                <li
                  key={item.id}
                  style={{
                    paddingInlineStart: `${Math.min(item.level, 5) * 0.5}rem`,
                  }}
                >
                  <Button
                    type="button"
                    size="xs"
                    variant="ghost"
                    className="h-auto justify-start whitespace-normal break-words px-1 py-0.5 text-left font-normal underline"
                    aria-label={`查看 ${item.heading || "未命名节"}`}
                    onClick={() => setSectionId(item.id)}
                  >
                    {item.heading || "未命名节"}
                  </Button>
                  {item.pages && (
                    <span className="ml-2 text-xs text-muted-foreground">
                      第 {item.pages[0]}—{item.pages[1]} 页
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
          <nav className="flex flex-wrap gap-3" aria-label="目录分页">
            {cursor > 0 && (
              <Button
                type="button"
                variant="outline"
                onClick={() => setCursor(0)}
              >
                目录首页
              </Button>
            )}
            {contents.data.next_cursor !== null && (
              <Button
                type="button"
                variant="outline"
                onClick={() => setCursor(contents.data.next_cursor ?? 0)}
              >
                目录下一页
              </Button>
            )}
          </nav>
        </>
      )}
      {sectionId !== null && section.isFetching && (
        <p role="status" className="text-sm">
          正在加载正文
        </p>
      )}
      {sectionId !== null && section.isError && (
        <p role="alert" className="text-sm">
          正文不可用，文档可能已更新或权限已变化。
        </p>
      )}
      {showContext && previous.isFetching && (
        <p role="status" className="text-sm">
          正在加载引用上文
        </p>
      )}
      {showContext && previous.isError && (
        <p role="alert" className="text-sm">
          引用上文不可用，文档可能已更新或权限已变化。
        </p>
      )}
      {sectionId !== null &&
        contents.data &&
        !contents.isError &&
        !contents.isFetching &&
        section.data &&
        !section.isError &&
        !section.isFetching &&
        (!showContext ||
          (!previous.isError && !previous.isPending && !previous.isFetching)) &&
        (showContext && previous.data
          ? [previous.data, section.data]
          : [section.data]
        ).map((content) => (
          <article key={content.section.id} className="space-y-2 border-t pt-4">
            <h4 className="break-words font-medium">
              {content.section.id !== sectionId && "上文 · "}
              {content.section.heading || "未命名节"}
            </h4>
            <p className="text-xs text-muted-foreground">
              {content.section.heading_path.join(" › ")}
              {content.section.pages &&
                ` · 第 ${content.section.pages[0]}—${content.section.pages[1]} 页`}
            </p>
            <div className="space-y-3 break-words text-sm">
              <Markdown
                skipHtml
                disallowedElements={["img"]}
                remarkPlugins={[remarkGfm]}
                components={{
                  a: ({ href, children }) =>
                    href ? (
                      <a
                        href={href}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="underline"
                      >
                        {children}
                      </a>
                    ) : (
                      <span>{children}</span>
                    ),
                  table: ({ children }) => (
                    <div className="overflow-x-auto">
                      <table className="min-w-full border-collapse text-left">
                        {children}
                      </table>
                    </div>
                  ),
                  th: ({ children }) => (
                    <th className="border px-3 py-2">{children}</th>
                  ),
                  td: ({ children }) => (
                    <td className="border px-3 py-2">{children}</td>
                  ),
                  pre: ({ children }) => (
                    <pre className="overflow-x-auto whitespace-pre-wrap">
                      {children}
                    </pre>
                  ),
                }}
              >
                {content.markdown || "本节无正文。"}
              </Markdown>
            </div>
          </article>
        ))}
    </section>
  );
}
