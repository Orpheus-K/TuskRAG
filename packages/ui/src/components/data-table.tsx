import type { ReactNode } from "react";

import { cn } from "../lib/utils";

export interface DataTableProps {
  headers: ReactNode[];
  children: ReactNode;
  className?: string;
}

export function DataTable({ headers, children, className }: DataTableProps) {
  return (
    <div
      className={cn(
        "overflow-x-auto rounded-xl border border-border bg-surface",
        className,
      )}
    >
      <table className="w-full border-collapse text-left text-sm">
        <thead className="border-b bg-surface-subtle text-xs font-medium text-muted-foreground">
          <tr>
            {headers.map((header, index) => (
              <th key={index} className="px-4 py-2.5 font-medium">
                {header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-border">{children}</tbody>
      </table>
    </div>
  );
}

export function DataTableRow({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <tr className={cn("transition-colors hover:bg-muted/40", className)}>
      {children}
    </tr>
  );
}

export function DataTableCell({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return <td className={cn("px-4 py-2.5 align-top", className)}>{children}</td>;
}
