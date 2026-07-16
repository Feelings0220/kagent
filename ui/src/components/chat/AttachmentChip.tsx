import { FileText, X } from "lucide-react";

interface AttachmentChipProps {
  name: string;
  /** Size in bytes; when provided a rounded KB value is shown. */
  size?: number;
  /** When provided, a remove (×) button is rendered. */
  onRemove?: () => void;
  /** Background style: "muted" for sent messages, "solid" for the composer. */
  variant?: "muted" | "solid";
}

/** File attachment pill shared by the composer draft list and sent messages. */
export default function AttachmentChip({ name, size, onRemove, variant = "muted" }: AttachmentChipProps) {
  const bg = variant === "solid" ? "bg-background" : "bg-muted/50";
  return (
    <span className={`inline-flex items-center gap-1 rounded-md border ${bg} px-2 py-0.5 text-xs`}>
      <FileText className="h-3 w-3 text-muted-foreground" aria-hidden />
      <span className="max-w-48 truncate" title={name}>{name}</span>
      {size !== undefined && (
        <span className="text-muted-foreground">{Math.max(1, Math.round(size / 1024))}KB</span>
      )}
      {onRemove && (
        <button
          type="button"
          onClick={onRemove}
          className="ml-0.5 rounded-full p-0.5 hover:bg-muted"
          aria-label={`Remove ${name}`}
        >
          <X className="h-3 w-3" aria-hidden />
        </button>
      )}
    </span>
  );
}
