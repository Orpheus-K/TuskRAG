import {
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";

import { cn } from "../lib/utils";
import { Button } from "./button";

export interface SplitPaneConfig {
  id: string;
  title: string;
  defaultWidth?: number; // px for fixed/resizable panes; omit for fluid 1fr main pane
  minWidth?: number;
  maxWidth?: number;
  collapsible?: boolean;
  defaultCollapsed?: boolean;
  content: ReactNode;
  actions?: ReactNode;
}

export interface WorkspaceSplitProps {
  panes: SplitPaneConfig[];
  storageKey?: string;
  className?: string;
}

function buildGridTemplate(
  panes: SplitPaneConfig[],
  widths: Record<string, number>,
  collapsed: Record<string, boolean>,
): string {
  const tracks: string[] = [];
  const fixedCount = panes.filter(
    (p) => p.defaultWidth !== undefined && !collapsed[p.id],
  ).length;
  const maxShare = Math.floor(66 / Math.max(1, fixedCount));
  panes.forEach((pane, idx) => {
    const isCollapsed = Boolean(collapsed[pane.id]);
    if (idx > 0) {
      tracks.push(isCollapsed ? "0px" : "6px");
    }
    if (isCollapsed) {
      tracks.push("0px");
    } else if (pane.defaultWidth !== undefined) {
      const w = widths[pane.id] ?? pane.defaultWidth;
      tracks.push(`min(var(--pane-${pane.id}, ${w}px), ${maxShare}%)`);
    } else {
      tracks.push("minmax(0, 1fr)");
    }
  });
  return tracks.join(" ");
}

export function WorkspaceSplit({
  panes,
  storageKey,
  className,
}: WorkspaceSplitProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const widthsRef = useRef<Record<string, number>>({});

  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(() => {
    const init: Record<string, boolean> = {};
    for (const p of panes) {
      init[p.id] = Boolean(p.defaultCollapsed);
    }
    if (storageKey) {
      try {
        const raw = window.localStorage.getItem(`${storageKey}.collapsed`);
        if (raw) {
          const parsed: unknown = JSON.parse(raw);
          if (parsed && typeof parsed === "object") {
            for (const [k, v] of Object.entries(parsed)) {
              if (typeof v === "boolean" && k in init) init[k] = v;
            }
          }
        }
      } catch {
        // ignore storage read failure
      }
    }
    return init;
  });

  // Load persisted widths once into ref (not React state, to keep dragging 0-re-render)
  if (Object.keys(widthsRef.current).length === 0) {
    const initW: Record<string, number> = {};
    for (const p of panes) {
      if (p.defaultWidth !== undefined) initW[p.id] = p.defaultWidth;
    }
    if (storageKey) {
      try {
        const raw = window.localStorage.getItem(`${storageKey}.widths`);
        if (raw) {
          const parsed: unknown = JSON.parse(raw);
          if (parsed && typeof parsed === "object") {
            for (const [k, v] of Object.entries(parsed)) {
              if (typeof v === "number" && Number.isFinite(v) && k in initW) {
                initW[k] = v;
              }
            }
          }
        }
      } catch {
        // ignore storage read failure
      }
    }
    widthsRef.current = initW;
  }

  const syncTemplate = useCallback(
    (nextCollapsed: Record<string, boolean>) => {
      const el = containerRef.current;
      if (!el) return;
      el.style.setProperty(
        "--split-template",
        buildGridTemplate(panes, widthsRef.current, nextCollapsed),
      );
    },
    [panes],
  );

  useEffect(() => {
    syncTemplate(collapsed);
  }, [collapsed, syncTemplate]);

  const toggleCollapse = (paneId: string) => {
    setCollapsed((prev) => {
      const next = { ...prev, [paneId]: !prev[paneId] };
      if (storageKey) {
        try {
          window.localStorage.setItem(
            `${storageKey}.collapsed`,
            JSON.stringify(next),
          );
        } catch {
          // ignore
        }
      }
      return next;
    });
  };

  const startResize = (
    event: ReactPointerEvent<HTMLButtonElement>,
    paneIndex: number,
  ) => {
    const container = containerRef.current;
    if (!container) return;

    // Determine which fixed-width pane this handle resizes (prefer right pane if fixed, else left pane)
    const leftPane = panes[paneIndex - 1];
    const rightPane = panes[paneIndex];
    const targetPane =
      rightPane?.defaultWidth !== undefined
        ? rightPane
        : leftPane?.defaultWidth !== undefined
          ? leftPane
          : null;
    if (!targetPane) return;

    const isRightTarget = targetPane.id === rightPane?.id;
    const startX = event.clientX;
    const startWidth =
      (isRightTarget
        ? event.currentTarget.nextElementSibling
        : event.currentTarget.previousElementSibling
      )?.getBoundingClientRect().width ??
      targetPane.defaultWidth ??
      320;
    const minW = targetPane.minWidth ?? 220;
    const maxW = targetPane.maxWidth ?? 680;

    event.currentTarget.setPointerCapture(event.pointerId);
    container.dataset.resizing = "true";
    event.currentTarget.dataset.active = "true";
    const handleEl = event.currentTarget;

    const onMove = (moveEvent: PointerEvent) => {
      const delta = moveEvent.clientX - startX;
      const raw = isRightTarget ? startWidth - delta : startWidth + delta;
      const clamped = Math.max(minW, Math.min(maxW, Math.round(raw)));
      widthsRef.current[targetPane.id] = clamped;
      container.style.setProperty(`--pane-${targetPane.id}`, `${clamped}px`);
    };

    const onUp = () => {
      delete container.dataset.resizing;
      delete handleEl.dataset.active;
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
      if (storageKey) {
        try {
          window.localStorage.setItem(
            `${storageKey}.widths`,
            JSON.stringify(widthsRef.current),
          );
        } catch {
          // ignore
        }
      }
    };

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
  };

  const collapsiblePanes = panes.filter(
    (p) => p.collapsible && collapsed[p.id],
  );

  return (
    <div className={cn("flex min-h-0 flex-col gap-3", className)}>
      {collapsiblePanes.length > 0 ? (
        <div className="flex flex-wrap items-center justify-end gap-2">
          {collapsiblePanes.map((pane) => (
            <Button
              key={pane.id}
              size="xs"
              variant="outline"
              onClick={() => toggleCollapse(pane.id)}
            >
              展开{pane.title}
            </Button>
          ))}
        </div>
      ) : null}

      <div
        ref={containerRef}
        className="tusk-split-group min-h-0 flex-1"
        style={
          {
            "--split-template": buildGridTemplate(
              panes,
              widthsRef.current,
              collapsed,
            ),
          } as React.CSSProperties
        }
      >
        {panes.map((pane, idx) => {
          const isCollapsed = Boolean(collapsed[pane.id]);
          const handleEl =
            idx > 0 ? (
              <button
                key={`handle-${pane.id}`}
                aria-label={`调整${pane.title}宽度`}
                className="tusk-split-handle"
                data-collapsed={isCollapsed ? "true" : "false"}
                type="button"
                onPointerDown={(e) => startResize(e, idx)}
                title="拖拽调整栏宽"
              />
            ) : null;

          return [
            handleEl,
            <section
              key={pane.id}
              aria-hidden={isCollapsed}
              inert={isCollapsed}
              aria-label={pane.title}
              className="tusk-split-pane rounded-xl border border-border bg-surface p-4"
              data-collapsed={isCollapsed ? "true" : "false"}
            >
              {(pane.collapsible || pane.actions) && (
                <div className="mb-3 flex items-center justify-between gap-2 border-b pb-2.5">
                  <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                    {pane.title}
                  </h2>
                  <div className="flex items-center gap-1.5">
                    {pane.actions}
                    {pane.collapsible ? (
                      <Button
                        aria-label={`收起${pane.title}`}
                        size="xs"
                        variant="ghost"
                        onClick={() => toggleCollapse(pane.id)}
                      >
                        收起
                      </Button>
                    ) : null}
                  </div>
                </div>
              )}
              {pane.content}
            </section>,
          ];
        })}
      </div>
    </div>
  );
}
