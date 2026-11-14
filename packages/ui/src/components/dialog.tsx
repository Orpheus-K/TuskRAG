import * as Primitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactElement, ReactNode } from "react";

import { cn } from "../lib/utils";
import { Button } from "./button";

export function Dialog({
  trigger,
  title,
  description,
  open,
  onOpenChange,
  className,
  children,
}: {
  trigger: ReactElement;
  title: string;
  description?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  className?: string;
  children: ReactNode;
}) {
  return (
    <Primitive.Root open={open} onOpenChange={onOpenChange}>
      <Primitive.Trigger asChild>{trigger}</Primitive.Trigger>
      <Primitive.Portal>
        <Primitive.Overlay className="fixed inset-0 z-40 bg-black/40" />
        <Primitive.Content
          className={cn(
            "fixed left-1/2 top-1/2 z-50 flex max-h-[90dvh] w-[min(32rem,92vw)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-xl border bg-surface shadow-lg",
            className,
          )}
          {...(description ? {} : { "aria-describedby": undefined })}
        >
          <div className="flex shrink-0 items-start justify-between gap-3 border-b px-5 py-3.5">
            <div className="min-w-0">
              <Primitive.Title className="font-semibold">
                {title}
              </Primitive.Title>
              {description ? (
                <Primitive.Description className="mt-1 text-sm text-muted-foreground">
                  {description}
                </Primitive.Description>
              ) : null}
            </div>
            <Primitive.Close asChild>
              <Button aria-label="关闭" size="icon" variant="ghost">
                <X aria-hidden="true" className="h-4 w-4" />
              </Button>
            </Primitive.Close>
          </div>
          <div className="min-h-0 overflow-y-auto px-5 py-4">{children}</div>
        </Primitive.Content>
      </Primitive.Portal>
    </Primitive.Root>
  );
}
