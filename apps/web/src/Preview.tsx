import type { Identity } from "@tuskrag/api-client";
import { Dialog } from "@tuskrag/ui/dialog";
import { Component, lazy, type ReactNode, Suspense } from "react";
import type { PreviewTarget } from "./DocumentPreview";

const DocumentPreview = lazy(() =>
  import("./DocumentPreview").then((module) => ({
    default: module.DocumentPreview,
  })),
);
class PreviewBoundary extends Component<
  { children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    if (this.state.failed)
      return (
        <p role="alert" className="text-sm text-danger">
          预览加载失败，请刷新页面后重试。
        </p>
      );
    return this.props.children;
  }
}

/** Reads a document's table of contents and sections in a modal dialog. */
export function Preview({
  identity,
  target,
  stale = false,
  onClose,
}: {
  identity: Identity;
  target: PreviewTarget;
  /** The listed version changed after the dialog was opened. */
  stale?: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      title={target.title}
      className={stale ? "" : "h-[86dvh] w-[min(72rem,94vw)]"}
      bodyClassName={stale ? "" : "md:overflow-hidden"}
    >
      {stale ? (
        <p role="status" className="text-sm">
          文档已更新或不再可见，请重新打开目录。
        </p>
      ) : (
        <PreviewBoundary
          key={`${target.id}:${target.versionId}:${target.initialSection ?? 0}`}
        >
          <Suspense
            fallback={
              <p role="status" className="text-sm text-muted-foreground">
                正在加载预览
              </p>
            }
          >
            <DocumentPreview identity={identity} target={target} />
          </Suspense>
        </PreviewBoundary>
      )}
    </Dialog>
  );
}
