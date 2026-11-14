import * as SelectPrimitive from "@radix-ui/react-select";
import { Check, ChevronDown } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "../lib/utils";

interface SelectProps
  extends Pick<
    ComponentProps<typeof SelectPrimitive.Root>,
    "children" | "disabled" | "required" | "name" | "onValueChange"
  > {
  value?: number | string;
  defaultValue?: number | string;
  placeholder?: string;
  id?: string;
  className?: string;
  "aria-label"?: string;
}

export function Select({
  children,
  value,
  defaultValue,
  placeholder,
  id,
  className,
  "aria-label": label,
  ...props
}: SelectProps) {
  return (
    <SelectPrimitive.Root
      {...props}
      value={value === undefined ? undefined : String(value)}
      defaultValue={
        defaultValue === undefined ? undefined : String(defaultValue)
      }
    >
      <SelectPrimitive.Trigger
        id={id}
        aria-label={label}
        className={cn(
          "flex h-9 w-full min-w-0 items-center justify-between gap-2 rounded-md border bg-surface px-3 text-sm shadow-xs outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 [&>span:first-child]:truncate",
          className,
        )}
      >
        <SelectPrimitive.Value placeholder={placeholder} />
        <SelectPrimitive.Icon className="text-muted-foreground">
          <ChevronDown aria-hidden="true" className="size-4 shrink-0" />
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content
          position="popper"
          sideOffset={4}
          className="z-50 max-h-[var(--radix-select-content-available-height)] min-w-[var(--radix-select-trigger-width)] overflow-y-auto rounded-md border bg-surface text-foreground shadow-md"
        >
          <SelectPrimitive.Viewport className="p-1">
            {children}
          </SelectPrimitive.Viewport>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
}

export function SelectItem({
  value,
  children,
}: {
  value: number | string;
  children: ReactNode;
}) {
  return (
    <SelectPrimitive.Item
      value={String(value)}
      data-value={value}
      className="relative flex cursor-default items-center rounded-sm py-1.5 pl-2 pr-8 text-sm outline-none data-[highlighted]:bg-accent"
    >
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
      <SelectPrimitive.ItemIndicator className="absolute right-2">
        <Check aria-hidden="true" className="size-4" />
      </SelectPrimitive.ItemIndicator>
    </SelectPrimitive.Item>
  );
}
