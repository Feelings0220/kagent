"use client";

import { useState } from "react";
import { AtSign, X } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";

interface ChatContextChipProps {
  kind: string;
  name: string;
  namespace?: string;
  /** Injected context text; when present the chip opens a viewer dialog. */
  text?: string;
  onRemove?: () => void;
}

/** Chip for an injected cluster-resource context (composer draft + history). */
export default function ChatContextChip({ kind, name, namespace, text, onRemove }: ChatContextChipProps) {
  const [showText, setShowText] = useState(false);
  const label = `${kind} ${namespace ? `${namespace}/` : ""}${name}`;

  return (
    <span className="inline-flex items-center gap-1 rounded-md border border-blue-300 dark:border-blue-800 bg-blue-50 dark:bg-blue-950/30 px-2 py-0.5 text-xs">
      <AtSign className="h-3 w-3 text-blue-500" aria-hidden />
      <button
        type="button"
        onClick={() => text && setShowText(true)}
        className={`max-w-56 truncate ${text ? "hover:underline" : "cursor-default"}`}
        title={text ? `View injected context for ${label}` : label}
      >
        {label}
      </button>
      {onRemove && (
        <button
          type="button"
          onClick={onRemove}
          className="ml-0.5 rounded-full p-0.5 hover:bg-muted"
          aria-label={`Remove context ${label}`}
        >
          <X className="h-3 w-3" aria-hidden />
        </button>
      )}

      {text && (
        <Dialog open={showText} onOpenChange={setShowText}>
          <DialogContent className="max-w-3xl max-h-[80vh]">
            <DialogHeader>
              <DialogTitle>Injected context: {label}</DialogTitle>
            </DialogHeader>
            <pre className="mt-2 h-[60vh] overflow-auto rounded-md border bg-muted/50 p-3 text-xs whitespace-pre-wrap break-words">
              {text}
            </pre>
          </DialogContent>
        </Dialog>
      )}
    </span>
  );
}
