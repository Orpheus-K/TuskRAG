import * as Primitive from "@radix-ui/react-checkbox";
import { Check } from "lucide-react";
import type { ComponentProps } from "react";
import { cn } from "../lib/utils";

export function Checkbox({
  className,
  onCheckedChange,
  ...props
}: Omit<ComponentProps<typeof Primitive.Root>, "onCheckedChange"> & {
  onCheckedChange?: (checked: boolean) => void;
}) {
  return (
    <Primitive.Root
      {...props}
      onCheckedChange={(value) => onCheckedChange?.(value === true)}
      className={cn(
        "inline-flex size-4 shrink-0 items-center justify-center rounded border border-ring bg-surface text-xs shadow-xs outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground",
        className,
      )}
    >
      <Primitive.Indicator className="flex">
        <Check aria-hidden="true" className="size-3.5" />
      </Primitive.Indicator>
    </Primitive.Root>
  );
}
