import * as React from "react";

import { cn } from "../lib/utils";

export interface InputProps
  extends React.InputHTMLAttributes<HTMLInputElement> {}

const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ className, type = "text", ...props }, ref) => (
    <input
      ref={ref}
      type={type}
      className={cn(
        "flex h-9 w-full rounded-md border border-border bg-surface px-3 py-1 text-sm text-foreground shadow-xs transition-colors file:mr-3 file:rounded file:border-0 file:bg-muted file:px-2.5 file:py-1 file:text-xs file:font-medium placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        type === "checkbox" || type === "radio"
          ? "h-4 w-4 rounded border-border p-0 shadow-none"
          : "",
        className,
      )}
      {...props}
    />
  ),
);
Input.displayName = "Input";

export { Input };
