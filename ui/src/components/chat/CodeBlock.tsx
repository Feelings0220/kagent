"use client";
import React, { useMemo, useState } from "react";
import { Check, Copy, Download, Eye } from "lucide-react";
import { Button } from "../ui/button";
import DocumentPreviewDialog, { PreviewKind } from "./DocumentPreviewDialog";

const hasChildren = (props: unknown): props is { children: React.ReactNode } => {
  return typeof props === 'object' && props !== null && 'children' in props;
};

const extractTextFromReactNode = (node: React.ReactNode): string => {
  if (typeof node === "string") {
    return node;
  }
  if (Array.isArray(node)) {
    return node.map(extractTextFromReactNode).join("");
  }
  if (React.isValidElement(node) && node.props && hasChildren(node.props)) {
    return extractTextFromReactNode(node.props.children);
  }
  return String(node || "");
};

const LANGUAGE_EXTENSIONS: Record<string, string> = {
  javascript: "js",
  typescript: "ts",
  jsx: "jsx",
  tsx: "tsx",
  python: "py",
  markdown: "md",
  md: "md",
  html: "html",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  bash: "sh",
  shell: "sh",
  sh: "sh",
  css: "css",
  svg: "svg",
  xml: "xml",
  csv: "csv",
  go: "go",
  rust: "rs",
  java: "java",
  sql: "sql",
  mermaid: "mmd",
  text: "txt",
  plaintext: "txt",
};

const PREVIEW_KINDS: Record<string, PreviewKind> = {
  html: "html",
  markdown: "markdown",
  md: "markdown",
  svg: "svg",
};

const getLanguage = (className: string): string => {
  const match = /language-([\w-]+)/.exec(className || "");
  return match ? match[1].toLowerCase() : "";
};

const CodeBlock = ({ children, className }: { children: React.ReactNode[]; className: string }) => {
  const [copied, setCopied] = useState(false);
  const [showPreview, setShowPreview] = useState(false);

  const language = getLanguage(className);
  const previewKind = PREVIEW_KINDS[language];

  // Extraction walks the whole node tree; memoize it so streaming re-renders
  // of the transcript don't re-extract every code block on every chunk.
  const codeContent = useMemo(
    () => (children && children.length > 0 ? extractTextFromReactNode(children[0]) : ""),
    [children],
  );

  const handleCopy = async () => {
    if (codeContent) {
      await navigator.clipboard.writeText(codeContent);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const handleDownload = () => {
    if (!codeContent) return;
    const extension = LANGUAGE_EXTENSIONS[language] || "txt";
    const timestamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
    const blob = new Blob([codeContent], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `kagent-${language || "output"}-${timestamp}.${extension}`;
    document.body.appendChild(anchor);
    anchor.click();
    document.body.removeChild(anchor);
    URL.revokeObjectURL(url);
  };

  const toolbarButtonClass =
    "p-2 h-8 w-8 rounded-md bg-background/80 hover:bg-background/90";

  return (
    <div className="relative group">
      <pre className={className}>
        <code className={className}>{children}</code>
      </pre>
      <div className="absolute top-2 right-2 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
        {previewKind && (
          <Button
            variant="link"
            onClick={() => setShowPreview(true)}
            className={toolbarButtonClass}
            aria-label="Preview"
            title="Preview"
          >
            <Eye size={16} />
          </Button>
        )}
        <Button
          variant="link"
          onClick={handleDownload}
          className={toolbarButtonClass}
          aria-label="Download"
          title="Download"
        >
          <Download size={16} />
        </Button>
        <Button
          variant="link"
          onClick={handleCopy}
          className={toolbarButtonClass}
          aria-label="Copy to clipboard"
          title={copied ? "Copied!" : "Copy to clipboard"}
        >
          {copied ? <Check size={16} /> : <Copy size={16} />}
        </Button>
      </div>
      {previewKind && showPreview && (
        <DocumentPreviewDialog
          content={codeContent}
          kind={previewKind}
          open={showPreview}
          onOpenChange={setShowPreview}
        />
      )}
    </div>
  );
};

export default CodeBlock;
