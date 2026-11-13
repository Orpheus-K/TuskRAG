import type { Identity } from "@tuskrag/api-client";
import { Button } from "@tuskrag/ui/button";
import { Component, lazy, type ReactNode, Suspense } from "react";
import type { PreviewTarget } from "./DocumentPreview";

const DocumentPreview = lazy(() =>
  import("./DocumentPreview").then((module) => ({
    default: module.DocumentPreview,
  })),
);
class PreviewBoundary extends Component<
  { children: ReactNode; onClose: () => void },
  { failed: boolean }
> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    if (this.state.failed)
      return (
        <section className="mt-6 rounded-xl border p-5">
          <p role="alert" className="text-sm">
            预览加载失败，请刷新页面后重试。
          </p>
          <Button
            className="mt-3"
            type="button"
            variant="outline"
            onClick={this.props.onClose}
          >
            关闭预览
          </Button>
        </section>
      );
    return this.props.children;
  }
}
export function Preview({
  identity,
  target,
  onClose,
}: {
  identity: Identity;
  target: PreviewTarget;
  onClose: () => void;
}) {
  return (
    <PreviewBoundary
      key={`${target.id}:${target.versionId}:${target.initialSection ?? 0}`}
      onClose={onClose}
    >
      <Suspense
        fallback={
          <p role="status" className="mt-6 text-sm">
            正在加载预览
          </p>
        }
      >
        <DocumentPreview
          identity={identity}
          target={target}
          onClose={onClose}
        />
      </Suspense>
    </PreviewBoundary>
  );
}
