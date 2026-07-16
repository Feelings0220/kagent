"use client";

import ReactMarkdown from "react-markdown";
import gfm from "remark-gfm";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";

export type PreviewKind = "html" | "markdown" | "svg" | "image";

interface DocumentPreviewDialogProps {
  content: string;
  kind: PreviewKind;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** MIME type for kind "image" (content is base64-encoded). */
  mimeType?: string;
}

const sanitizeHTML = (html: string) => {
  const temp = document.createElement("div");
  temp.innerHTML = html;

  const scripts = temp.getElementsByTagName("script");
  while (scripts.length > 0) {
    scripts[0].parentNode?.removeChild(scripts[0]);
  }

  const elements = temp.getElementsByTagName("*");
  for (let i = 0; i < elements.length; i++) {
    const element = elements[i];
    const attributes = element.attributes;
    for (let j = attributes.length - 1; j >= 0; j--) {
      const attr = attributes[j];
      // Strip event handlers, navigation, and remote-loading attributes.
      // `style` is removed too: CSS url() in an allow-same-origin iframe can
      // beacon to remote hosts and spoof the preview chrome.
      if (
        attr.name.startsWith("on") ||
        attr.name === "href" ||
        attr.name === "src" ||
        attr.name === "style"
      ) {
        element.removeAttribute(attr.name);
      }
    }
  }

  return temp.innerHTML;
};

const DocumentPreviewDialog = ({ content, kind, open, onOpenChange, mimeType }: DocumentPreviewDialogProps) => {
  const title =
    kind === "html" ? "HTML Preview" : kind === "svg" ? "SVG Preview" : kind === "image" ? "Image Preview" : "Markdown Preview";

  const renderBody = () => {
    if (kind === "markdown") {
      return (
        <div className="prose prose-sm dark:prose-invert max-w-none h-[60vh] overflow-y-auto border rounded-md p-4">
          <ReactMarkdown remarkPlugins={[gfm]}>{content}</ReactMarkdown>
        </div>
      );
    }

    if (kind === "image") {
      // content is base64; a data URI never executes scripts.
      const dataUri = `data:${mimeType || "image/png"};base64,${content}`;
      return (
        <div className="flex h-[60vh] items-center justify-center overflow-auto border rounded-md p-4 bg-white">
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src={dataUri} alt="Image preview" className="max-w-full max-h-full" />
        </div>
      );
    }

    if (kind === "svg") {
      // Render via an <img> data URI: styles and shapes are preserved while
      // scripts and external references never execute inside an image.
      const dataUri = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(content)}`;
      return (
        <div className="flex h-[60vh] items-center justify-center overflow-auto border rounded-md p-4 bg-white">
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src={dataUri} alt="SVG preview" className="max-w-full max-h-full" />
        </div>
      );
    }

    const safeHTML = `
      <!DOCTYPE html>
      <html>
      <head>
        <meta charset="UTF-8">
        <meta name="viewport" content="width=device-width, initial-scale=1.0">
        <style>
          body { margin: 0; padding: 16px; font-family: system-ui, sans-serif; }
          * { max-width: 100%; }
        </style>
      </head>
      <body>
        ${sanitizeHTML(content)}
      </body>
      </html>
    `;
    return (
      <iframe
        srcDoc={safeHTML}
        className="w-full h-[60vh] border rounded-md bg-white"
        title="HTML Preview"
        sandbox="allow-same-origin"
        referrerPolicy="no-referrer"
      />
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-4xl max-h-[80vh]">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <div className="mt-2">{renderBody()}</div>
      </DialogContent>
    </Dialog>
  );
};

export default DocumentPreviewDialog;
