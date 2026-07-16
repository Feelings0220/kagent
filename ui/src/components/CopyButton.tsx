"use client";
import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";

interface CopyButtonProps {
  /** Text to copy to the clipboard. */
  content: string;
  className?: string;
  /** Icon size in px. */
  size?: number;
  /** Button variant. */
  variant?: "ghost" | "link";
}

/** Copy-to-clipboard button with a 2s "copied" checkmark, shared across chat UI. */
export default function CopyButton({ content, className, size = 16, variant = "ghost" }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(content);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error("Failed to copy text:", err);
    }
  };

  return (
    <Button
      variant={variant}
      size="sm"
      onClick={handleCopy}
      className={className}
      aria-label="Copy to clipboard"
      title={copied ? "Copied!" : "Copy to clipboard"}
    >
      {copied ? <Check size={size} /> : <Copy size={size} />}
    </Button>
  );
}
