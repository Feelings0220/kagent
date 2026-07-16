"use client";

import { useMemo, useState } from "react";
import { ChevronDown, Download, Eye, FileText } from "lucide-react";
import { Button } from "@/components/ui/button";
import DocumentPreviewDialog, { PreviewKind } from "@/components/chat/DocumentPreviewDialog";
import type { FileArtifact } from "@/lib/messageHandlers";

interface ArtifactsPanelProps {
  artifacts: FileArtifact[];
}

/** Preview kind for an artifact, or null when only download makes sense. */
const previewKindFor = (artifact: FileArtifact): PreviewKind | null => {
  if (!artifact.bytes) return null;
  const name = artifact.name.toLowerCase();
  const mime = artifact.mimeType.toLowerCase();
  if (mime.startsWith("image/svg") || name.endsWith(".svg")) return "svg";
  if (mime.startsWith("image/")) return "image";
  if (mime.includes("html") || name.endsWith(".html") || name.endsWith(".htm")) return "html";
  if (mime.includes("markdown") || name.endsWith(".md") || name.endsWith(".markdown")) return "markdown";
  return null;
};

const decodeBase64 = (bytes: string): Uint8Array => {
  const binary = atob(bytes);
  const buffer = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    buffer[i] = binary.charCodeAt(i);
  }
  return buffer;
};

/** Text content for text-like previews (markdown/html/svg render from text). */
const decodeText = (bytes: string): string => {
  try {
    return new TextDecoder().decode(decodeBase64(bytes));
  } catch {
    return "";
  }
};

const formatSize = (size: number): string => {
  if (size >= 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)}MB`;
  if (size >= 1024) return `${Math.round(size / 1024)}KB`;
  return `${size}B`;
};

/**
 * Collapsible bar listing the files the agent wrote to the session outputs/
 * directory, with preview and download for each.
 */
export default function ArtifactsPanel({ artifacts }: ArtifactsPanelProps) {
  const [isExpanded, setIsExpanded] = useState(true);
  const [previewArtifact, setPreviewArtifact] = useState<FileArtifact | null>(null);

  const previewKind = useMemo(
    () => (previewArtifact ? previewKindFor(previewArtifact) : null),
    [previewArtifact]
  );
  const previewContent = useMemo(() => {
    if (!previewArtifact?.bytes || !previewKind) return "";
    // Image previews consume raw base64; text-like previews need decoded text.
    return previewKind === "image" ? previewArtifact.bytes : decodeText(previewArtifact.bytes);
  }, [previewArtifact, previewKind]);

  if (artifacts.length === 0) {
    return null;
  }

  const handleDownload = (artifact: FileArtifact) => {
    if (!artifact.bytes) return;
    const blob = new Blob([decodeBase64(artifact.bytes) as BlobPart], {
      type: artifact.mimeType || "application/octet-stream",
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    // Artifact names may contain subdirectories; download as a flat name.
    anchor.download = artifact.name.split("/").pop() || artifact.name;
    document.body.appendChild(anchor);
    anchor.click();
    document.body.removeChild(anchor);
    URL.revokeObjectURL(url);
  };

  return (
    <div className="w-full max-w-3xl mx-auto mb-1 rounded-md border bg-card/50 text-sm">
      <button
        type="button"
        onClick={() => setIsExpanded(!isExpanded)}
        className="flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-muted/50 rounded-md"
        aria-expanded={isExpanded}
      >
        <FileText className="h-3.5 w-3.5 text-muted-foreground shrink-0" aria-hidden />
        <span className="text-xs font-medium">Artifacts ({artifacts.length})</span>
        <ChevronDown
          className={`ml-auto h-4 w-4 text-muted-foreground shrink-0 transition-transform ${isExpanded ? "rotate-180" : ""}`}
          aria-hidden
        />
      </button>

      {isExpanded && (
        <div className="flex flex-wrap gap-1.5 border-t px-3 py-2">
          {artifacts.map(artifact => {
            const kind = previewKindFor(artifact);
            return (
              <span
                key={artifact.name}
                className="inline-flex items-center gap-1 rounded-md border bg-background px-2 py-0.5 text-xs"
                title={artifact.note || artifact.path}
              >
                <FileText className="h-3 w-3 text-muted-foreground" aria-hidden />
                <span className="max-w-48 truncate">{artifact.name}</span>
                <span className="text-muted-foreground">{formatSize(artifact.size)}</span>
                {kind && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-5 w-5 p-0"
                    onClick={() => setPreviewArtifact(artifact)}
                    aria-label={`Preview ${artifact.name}`}
                    title="Preview"
                  >
                    <Eye className="h-3 w-3" aria-hidden />
                  </Button>
                )}
                {artifact.bytes && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-5 w-5 p-0"
                    onClick={() => handleDownload(artifact)}
                    aria-label={`Download ${artifact.name}`}
                    title="Download"
                  >
                    <Download className="h-3 w-3" aria-hidden />
                  </Button>
                )}
              </span>
            );
          })}
        </div>
      )}

      {previewArtifact && previewKind && (
        <DocumentPreviewDialog
          content={previewContent}
          kind={previewKind}
          mimeType={previewArtifact.mimeType}
          open={!!previewArtifact}
          onOpenChange={(open) => !open && setPreviewArtifact(null)}
        />
      )}
    </div>
  );
}
