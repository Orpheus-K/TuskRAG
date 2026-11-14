import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes, ReactNode } from "react";

import { cn } from "../lib/utils";

const alertVariants = cva(
  "rounded-lg border px-3.5 py-2.5 text-sm leading-relaxed",
  {
    variants: {
      variant: {
        info: "border-border bg-muted/50 text-foreground",
        warning:
          "border-amber-500/30 bg-amber-500/10 text-amber-800 dark:text-amber-200",
        danger: "border-danger/30 bg-danger/10 text-danger",
      },
    },
    defaultVariants: {
      variant: "info",
    },
  },
);

export interface AlertProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof alertVariants> {
  children: ReactNode;
}

export function Alert({
  className,
  variant = "info",
  role,
  children,
  ...props
}: AlertProps) {
  const effectiveRole = role ?? (variant === "danger" ? "alert" : "status");
  return (
    <div
      className={cn(alertVariants({ variant, className }))}
      role={effectiveRole}
      {...props}
    >
      {children}
    </div>
  );
}
