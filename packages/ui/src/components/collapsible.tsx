import * as Primitive from "@radix-ui/react-collapsible";
import { ChevronRight } from "lucide-react";
import type { ComponentProps } from "react";
import { cn } from "../lib/utils";

export const Collapsible = Primitive.Root;
export function CollapsibleTrigger({
  className,
  ...props
}: ComponentProps<typeof Primitive.Trigger>) {
  return (
    <Primitive.Trigger
      {...props}
      className={cn(
        "group flex w-full items-center gap-2 rounded text-left font-medium focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
    >
      <ChevronRight
        aria-hidden="true"
        className="size-4 shrink-0 transition-transform group-data-[state=open]:rotate-90"
      />
      {props.children}
    </Primitive.Trigger>
  );
}
export function CollapsibleContent(
  props: ComponentProps<typeof Primitive.Content>,
) {
  return (
    <Primitive.Content
      data-slot="collapsible-content"
      forceMount
      {...props}
      className={cn("data-[state=closed]:hidden", props.className)}
    />
  );
}
