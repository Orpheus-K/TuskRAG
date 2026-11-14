import { type ReactNode, useEffect, useRef, useState } from "react";

import { cn } from "../lib/utils";
import { Button } from "./button";

export interface IdeShellProps {
  brand: ReactNode;
  sidebarNav: ReactNode;
  sidebarFooter?: ReactNode;
  topbarLeft?: ReactNode;
  topbarRight?: ReactNode;
  storageKey?: string;
  children: ReactNode;
}

export function IdeShell({
  brand,
  sidebarNav,
  sidebarFooter,
  topbarLeft,
  topbarRight,
  storageKey = "tuskrag.ide.sidebarCollapsed",
  children,
}: IdeShellProps) {
  const shellRef = useRef<HTMLDivElement>(null);
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return window.localStorage.getItem(storageKey) === "true";
    } catch {
      return false;
    }
  });
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  useEffect(() => {
    try {
      window.localStorage.setItem(storageKey, String(collapsed));
    } catch {
      // ignore storage quota / restricted env
    }
  }, [collapsed, storageKey]);

  return (
    <div
      ref={shellRef}
      className="tusk-ide-shell bg-background text-foreground"
      data-sidebar-collapsed={collapsed ? "true" : "false"}
    >
      <aside
        aria-label="主导航栏"
        className={cn(
          "border-r border-border bg-surface-subtle/80 md:sticky md:top-0 md:flex md:h-screen md:flex-col",
          mobileMenuOpen ? "flex flex-col" : "hidden md:flex",
        )}
      >
        <div className="flex h-12 shrink-0 items-center justify-between gap-2 border-b px-3">
          <div className="min-w-0 truncate">{brand}</div>
          <Button
            aria-label={collapsed ? "展开侧边栏" : "折叠侧边栏"}
            className="hidden shrink-0 md:inline-flex"
            size="icon"
            variant="ghost"
            onClick={() => setCollapsed((v) => !v)}
            title={collapsed ? "展开侧边栏" : "折叠侧边栏"}
          >
            <span aria-hidden="true" className="text-xs font-mono">
              {collapsed ? "»" : "«"}
            </span>
          </Button>
        </div>

        <nav className="flex-1 space-y-1 overflow-y-auto px-2 py-3">
          {sidebarNav}
        </nav>

        {sidebarFooter ? (
          <div className="shrink-0 border-t px-2 py-2.5">{sidebarFooter}</div>
        ) : null}
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-10 flex h-12 shrink-0 items-center justify-between gap-3 border-b bg-surface/90 px-4 backdrop-blur-xs sm:px-6">
          <div className="flex min-w-0 items-center gap-2">
            <Button
              aria-label={mobileMenuOpen ? "收起菜单" : "展开菜单"}
              className="md:hidden"
              size="xs"
              variant="outline"
              onClick={() => setMobileMenuOpen((v) => !v)}
            >
              菜单
            </Button>
            {topbarLeft}
          </div>
          <div className="flex shrink-0 items-center gap-2">{topbarRight}</div>
        </header>

        <main className="min-h-0 min-w-0 flex-1 px-4 py-5 sm:px-6">
          {children}
        </main>
      </div>
    </div>
  );
}

export function IdeSidebarGroup({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="space-y-1 pt-2 first:pt-0">
      <p className="tusk-sidebar-label px-2.5 pb-1 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
        {title}
      </p>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}
