import { useState } from "react";
import { FunctionCall, TokenStats } from "@/types";
import { ScrollArea } from "@radix-ui/react-scroll-area";
import { FunctionSquare, CheckCircle, Clock, ChevronUp, ChevronDown, Loader2, AlertCircle, ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import CopyButton from "@/components/CopyButton";
import TokenStatsTooltip from "@/components/chat/TokenStatsTooltip";
import { convertToUserFriendlyName } from "@/lib/utils";

export type ToolCallStatus = "requested" | "executing" | "completed" | "pending_approval" | "approved" | "rejected";

interface ToolDisplayProps {
  call: FunctionCall;
  result?: {
    content: string;
    is_error?: boolean;
  };
  status?: ToolCallStatus;
  isError?: boolean;
  /** When true, the card is in a "decided but not yet submitted" state (batch flow). */
  isDecided?: boolean;
  /** For subagent HITL: the name of the subagent that made this tool call. */
  subagentName?: string;
  onApprove?: () => void;
  onReject?: (reason?: string) => void;
  tokenStats?: TokenStats;
}

/** Compact one-line JSON-ish summary of the tool arguments. */
const summarizeArgs = (args: unknown): string => {
  if (args === undefined || args === null) return "";
  try {
    const values = typeof args === "object" && !Array.isArray(args)
      ? Object.values(args as Record<string, unknown>)
      : [args];
    const summary = values
      .map(v => (typeof v === "string" ? v : JSON.stringify(v)))
      .filter(Boolean)
      .join("  ");
    return summary.length > 120 ? `${summary.slice(0, 120)}…` : summary;
  } catch {
    return "";
  }
};

const statusIcon = (status: ToolCallStatus, isError: boolean) => {
  if (isError) return <AlertCircle className="w-3.5 h-3.5 text-red-500 shrink-0" aria-label="Error" />;
  switch (status) {
    case "requested":
      return <Clock className="w-3.5 h-3.5 text-blue-500 shrink-0" aria-label="Call requested" />;
    case "executing":
      return <Loader2 className="w-3.5 h-3.5 text-yellow-500 animate-spin shrink-0" aria-label="Executing" />;
    case "approved":
      return <CheckCircle className="w-3.5 h-3.5 text-green-500 shrink-0" aria-label="Approved" />;
    case "rejected":
      return <AlertCircle className="w-3.5 h-3.5 text-red-500 shrink-0" aria-label="Rejected" />;
    case "completed":
      return <CheckCircle className="w-3.5 h-3.5 text-green-500 shrink-0" aria-label="Completed" />;
    default:
      return <FunctionSquare className="w-3.5 h-3.5 shrink-0" aria-hidden />;
  }
};

const CornerCopyButton = ({ content }: { content: string }) => (
  <CopyButton content={content} size={16} className="absolute top-1 right-1 p-2" />
);

/**
 * Compact single-row rendering used for all non-approval states. Click the
 * row to expand full arguments and results.
 */
const CompactToolRow = ({ call, result, status, isError, subagentName, tokenStats }: ToolDisplayProps & { status: ToolCallStatus }) => {
  const [isExpanded, setIsExpanded] = useState(false);
  const hasResult = result !== undefined;
  const argsSummary = summarizeArgs(call.args);

  return (
    <div className={`w-full rounded-md border bg-card/50 text-sm ${isError ? "border-red-300 dark:border-red-800" : ""}`}>
      {/* TokenStatsTooltip renders its own button, so it must live outside the
          row's expand/collapse button to avoid nested <button> elements. */}
      <div className="flex w-full items-center gap-2 rounded-md hover:bg-muted/50">
        <button
          type="button"
          onClick={() => setIsExpanded(!isExpanded)}
          className="flex flex-1 min-w-0 items-center gap-2 px-3 py-1.5 text-left"
          aria-expanded={isExpanded}
        >
          {statusIcon(status, !!isError)}
          <span className="font-medium text-xs shrink-0">{call.name}</span>
          {subagentName && (
            <span className="text-xs text-muted-foreground shrink-0">
              via {convertToUserFriendlyName(subagentName)}
            </span>
          )}
          <span className="flex-1 truncate text-xs text-muted-foreground font-mono">{argsSummary}</span>
          {isExpanded
            ? <ChevronUp className="w-4 h-4 text-muted-foreground shrink-0" aria-hidden />
            : <ChevronDown className="w-4 h-4 text-muted-foreground shrink-0" aria-hidden />}
        </button>
        {tokenStats && <span className="pr-3 shrink-0"><TokenStatsTooltip stats={tokenStats} /></span>}
      </div>

      {isExpanded && (
        <div className="border-t px-3 py-2 space-y-3">
          <div>
            <div className="text-xs font-medium text-muted-foreground mb-1">Arguments</div>
            <div className="relative">
              <ScrollArea className="max-h-96 overflow-y-auto p-3 w-full bg-muted/50 rounded-md">
                <pre className="text-xs whitespace-pre-wrap break-words">
                  {JSON.stringify(call.args, null, 2)}
                </pre>
              </ScrollArea>
              <CornerCopyButton content={JSON.stringify(call.args, null, 2)} />
            </div>
          </div>

          {status === "executing" && !hasResult && (
            <div className="flex items-center gap-2 py-1">
              <Loader2 className="h-4 w-4 animate-spin" />
              <span className="text-xs">Executing...</span>
            </div>
          )}

          {hasResult && (
            <div>
              <div className="text-xs font-medium text-muted-foreground mb-1">{isError ? "Error" : "Results"}</div>
              <div className="relative">
                <ScrollArea className={`max-h-96 overflow-y-auto p-3 w-full rounded-md ${isError ? "bg-red-50 dark:bg-red-950/10" : "bg-muted/50"}`}>
                  <pre className={`text-xs whitespace-pre-wrap break-words ${isError ? "text-red-600 dark:text-red-400" : ""}`}>
                    {result.content}
                  </pre>
                </ScrollArea>
                <CornerCopyButton content={result.content} />
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
};

const ToolDisplay = ({ call, result, status = "requested", isError = false, isDecided = false, subagentName, onApprove, onReject, tokenStats }: ToolDisplayProps) => {
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [showRejectForm, setShowRejectForm] = useState(false);
  const [rejectionReason, setRejectionReason] = useState("");

  const handleApprove = async () => {
    if (!onApprove) {
      return;
    }
    setIsSubmitting(true);
    onApprove();
  };

  /** Confirm rejection — submits with optional reason. */
  const handleRejectConfirm = async () => {
    if (!onReject) {
      return;
    }
    setShowRejectForm(false);
    setIsSubmitting(true);
    onReject(rejectionReason.trim() || undefined);
  };

  // Everything except a pending approval renders as a compact row; the
  // approval flow keeps the prominent card since it demands user action.
  if (status !== "pending_approval") {
    return (
      <CompactToolRow
        call={call}
        result={result}
        status={status}
        isError={isError}
        subagentName={subagentName}
        tokenStats={tokenStats}
      />
    );
  }

  return (
    <Card className="w-full mx-auto my-1 min-w-full border-amber-300 dark:border-amber-700">
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-xs flex space-x-5">
          <div className="flex items-center font-medium">
            <FunctionSquare className="w-4 h-4 mr-2" />
            {call.name}
          </div>
          {subagentName && (
            <div className="flex items-center text-muted-foreground font-normal">
              via {convertToUserFriendlyName(subagentName)} subagent
            </div>
          )}
          <div className="font-light">{call.id}</div>
        </CardTitle>
        <div className="flex items-center gap-2 text-xs">
          {tokenStats && <TokenStatsTooltip stats={tokenStats} />}
          <ShieldAlert className="w-3 h-3 inline-block mr-2 text-amber-500" />
          Approval required
        </div>
      </CardHeader>
      <CardContent>
        <div className="mt-2 relative">
          <ScrollArea className="max-h-96 overflow-y-auto p-4 w-full bg-muted/50">
            <pre className="text-sm whitespace-pre-wrap break-words">
              {JSON.stringify(call.args, null, 2)}
            </pre>
          </ScrollArea>
        </div>

        {/* Approval buttons — hidden when decided (batch) or submitting */}
        {!isSubmitting && !isDecided && !showRejectForm && (
          <div className="mt-4 space-y-2">
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="default"
                onClick={handleApprove}
              >
                Approve
              </Button>
              <Button
                size="sm"
                variant="destructive"
                onClick={() => setShowRejectForm(true)}
              >
                Reject
              </Button>
            </div>
          </div>
        )}

        {/* Rejection reason form — shown after clicking Reject */}
        {!isSubmitting && !isDecided && showRejectForm && (
          <div className="mt-4 space-y-2">
            <Textarea
              value={rejectionReason}
              onChange={(e) => setRejectionReason(e.target.value)}
              placeholder="Why are you rejecting this? (optional)"
              className="min-h-[60px] resize-none text-sm"
              autoFocus
            />
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="destructive"
                onClick={handleRejectConfirm}
              >
                Reject
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setShowRejectForm(false);
                  setRejectionReason("");
                }}
              >
                Cancel
              </Button>
            </div>
          </div>
        )}

        {(isSubmitting || isDecided) && (
          <div className="flex items-center gap-2 py-2 mt-4">
            <Loader2 className="h-4 w-4 animate-spin" />
            <span className="text-sm text-muted-foreground">
              {isDecided ? "Waiting..." : "Submitting decision..."}
            </span>
          </div>
        )}
      </CardContent>
    </Card>
  );
};

export default ToolDisplay;
