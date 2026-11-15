import type { ReactNode } from "react";

import { cn } from "../lib/utils";

export interface SectionHeaderProps {
  eyebrow?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
  className?: string;
  level?: "h1" | "h2";
}

export function SectionHeader({
  eyebrow,
  title,
  description,
  actions,
  className,
  level = "h1",
}: SectionHeaderProps) {
  const Heading = level;
  return (
    <div
      className={cn(
        "flex flex-wrap items-start justify-between gap-4",
        className,
      )}
    >
      <div className="min-w-0">
        {eyebrow ? (
          <p className="text-xs font-medium text-muted-foreground">{eyebrow}</p>
        ) : null}
        <Heading
          className={cn(
            "font-semibold tracking-tight text-foreground",
            level === "h1" ? "mt-1 text-2xl" : "text-base",
          )}
        >
          {title}
        </Heading>
        {description ? (
          <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex max-w-full flex-wrap items-center gap-2">
          {actions}
        </div>
      ) : null}
    </div>
  );
}
