import * as Dialog from "@radix-ui/react-alert-dialog";
import type { ReactElement } from "react";
import { Button } from "./button";

export function ConfirmAction({
  trigger,
  title,
  description,
  confirmLabel = "确认删除",
  onConfirm,
}: {
  trigger: ReactElement;
  title: string;
  description: string;
  confirmLabel?: string;
  onConfirm: () => void;
}) {
  return (
    <Dialog.Root>
      <Dialog.Trigger asChild>{trigger}</Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/40" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-[min(26rem,90vw)] -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-xl border bg-surface p-5 shadow-lg">
          <Dialog.Title className="font-semibold">{title}</Dialog.Title>
          <Dialog.Description className="text-sm text-muted-foreground">
            {description}
          </Dialog.Description>
          <div className="flex justify-end gap-2">
            <Dialog.Cancel asChild>
              <Button variant="outline">取消</Button>
            </Dialog.Cancel>
            <Dialog.Action asChild>
              <Button variant="destructive" onClick={onConfirm}>
                {confirmLabel}
              </Button>
            </Dialog.Action>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
