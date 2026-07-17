import { FilePart, Message, TextPart } from "@a2a-js/sdk";
import { TruncatableText } from "@/components/chat/TruncatableText";
import AttachmentChip from "@/components/chat/AttachmentChip";
import ChatContextChip from "@/components/chat/ChatContextChip";
import ToolCallDisplay from "@/components/chat/ToolCallDisplay";
import AskUserDisplay, { AskUserQuestion } from "@/components/chat/AskUserDisplay";
import KagentLogo from "../kagent-logo";
import { ThumbsUp, ThumbsDown } from "lucide-react";
import TokenStatsTooltip from "@/components/chat/TokenStatsTooltip";
import type { TokenStats } from "@/types";
import { useState } from "react";
import { FeedbackDialog } from "./FeedbackDialog";
import { toast } from "sonner";
import { convertToUserFriendlyName } from "@/lib/utils";
import { ADKMetadata, getMetadataValue } from "@/lib/messageHandlers";
import { ToolDecision } from "@/types";

interface ChatMessageProps {
  message: Message;
  allMessages: Message[];
  agentContext?: {
    namespace: string;
    agentName: string;
  };
  onApprove?: (toolCallId: string) => void;
  onReject?: (toolCallId: string, reason?: string) => void;
  onAskUserSubmit?: (answers: Array<{ answer: string[] }>) => void;
  pendingDecisions?: Record<string, ToolDecision>;
}

export default function ChatMessage({ message, allMessages, agentContext, onApprove, onReject, onAskUserSubmit, pendingDecisions }: ChatMessageProps) {
  const [feedbackDialogOpen, setFeedbackDialogOpen] = useState(false);
  const [isPositiveFeedback, setIsPositiveFeedback] = useState(true);

  if (!message) return null;

  // Injected cluster-context parts are rendered as chips, not inline text.
  const isContextPart = (part: { kind: string; metadata?: Record<string, unknown> }) =>
    part.kind === "text" && !!part.metadata?.kagent_context;

  const textParts = (message.parts?.filter(part => part.kind === "text" && !isContextPart(part)) || []) as TextPart[];
  const content = textParts.map(part => part.text).join("");
  const contextParts = (message.parts?.filter(part => isContextPart(part)) || []) as TextPart[];
  const fileParts = (message.parts?.filter(part => part.kind === "file") || []) as FilePart[];

  const source = message.role === "user" ? "user" : "assistant";
  const tokenStats = (message.metadata as Record<string, unknown> | undefined)?.tokenStats as TokenStats | undefined;
  const messageId = message.messageId;

  // Extract agent name from metadata for display
  const getDisplayName = () => {
    if (source === "user") {
      return "user";
    }

    const msgMetadata = message.metadata as ADKMetadata;
    const displaySource = msgMetadata?.displaySource;

    if (displaySource && displaySource !== "assistant") {
      return displaySource;
    }

    // For stored messages from Task history, try to get app_name from metadata
    const adkAppName = getMetadataValue<string>(message.metadata as Record<string, unknown>, "app_name");

    if (adkAppName) {
      return convertToUserFriendlyName(adkAppName);
    }

    // Use agent context as fallback for stored messages
    if (agentContext) {
      return `${agentContext.namespace}/${agentContext.agentName.replace(/_/g, "-")}`;
    }

    return "assistant"; // final fallback
  };

  const displayName = getDisplayName();
  const numericMessageId = messageId ? Math.abs(messageId.split('').reduce((a, b) => {
    a = ((a << 5) - a) + b.charCodeAt(0);
    return a & a;
  }, 0)) : 0;

  const metadata = message.metadata as ADKMetadata;
  const originalType = metadata?.originalType;

  // Check for tool call parts (works for both stored and streaming messages)
  const hasToolCallParts = message.parts?.some(part => {
    if (part.kind === "data" && part.metadata) {
      const partType = getMetadataValue<string>(part.metadata as Record<string, unknown>, "type");
      return partType === "function_call" || partType === "function_response";
    }
    return false;
  });

  // Also check for streaming tool calls via originalType (fallback for streaming messages)
  const isStreamingToolCall = originalType === "ToolCallRequestEvent" || originalType === "ToolCallExecutionEvent";

  // Ask-user requests get their own dedicated display component
  if (originalType === "AskUserRequest") {
    const askUserData = metadata?.askUserData as { id: string; questions: AskUserQuestion[] } | undefined;
    const resolvedAnswers = metadata?.askUserAnswers as Array<{ answer: string[] }> | null | undefined;
    const isResolved = !!metadata?.approvalDecision;
    const questions: AskUserQuestion[] = askUserData?.questions ?? [];
    const askUserSubagentName = metadata?.subagentName as string | undefined;
    return (
      <AskUserDisplay
        questions={questions}
        isResolved={isResolved}
        resolvedAnswers={resolvedAnswers ?? null}
        onSubmit={(answers) => onAskUserSubmit?.(answers)}
        subagentName={askUserSubagentName}
      />
    );
  }

  // Tool approval requests get routed to ToolCallDisplay with approval callbacks
  if (originalType === "ToolApprovalRequest") {
    return <ToolCallDisplay
      currentMessage={message}
      allMessages={allMessages}
      onApprove={onApprove}
      onReject={onReject}
      pendingDecisions={pendingDecisions}
    />;
  }

  if (hasToolCallParts || isStreamingToolCall) {
    return <ToolCallDisplay
      currentMessage={message}
      allMessages={allMessages}
      onApprove={onApprove}
      onReject={onReject}
      pendingDecisions={pendingDecisions}
    />;
  }

  if (originalType === "ToolCallSummaryMessage") {
    const hasToolCalls = allMessages.some(msg => {
      return msg.parts?.some(part => {
        if (part.kind === "data" && part.metadata) {
          const partType = getMetadataValue<string>(part.metadata as Record<string, unknown>, "type");
          return partType === "function_call" || partType === "function_response";
        }
        return false;
      });
    });

    if (hasToolCalls) {
      return <ToolCallDisplay currentMessage={message} allMessages={allMessages} />;
    }
    return null;
  }

  // Skip empty messages (attachment/context-only messages still render chips)
  if (!content && fileParts.length === 0 && contextParts.length === 0) {
    return null;
  }


  const handleFeedback = (isPositive: boolean) => {
    if (!messageId) {
      console.error("Message ID is undefined, cannot submit feedback.");
      toast.error("Cannot submit feedback: Message ID not found.");
      return;
    }
    setIsPositiveFeedback(isPositive);
    setFeedbackDialogOpen(true);
  };

  const messageBorderColor = source === "user" ? "border-l-blue-500" : "border-l-violet-500";

  return <div className={`flex items-center gap-2 text-sm border-l-2 py-2 px-4 ${messageBorderColor}`}>
    <div className="flex flex-col gap-1 w-full">
      {source !== "user" ? <div className="flex items-center gap-1">
        <KagentLogo className="w-4 h-4" />
        <div className="text-xs font-bold">{displayName}</div>
      </div> : <div className="text-xs font-bold">{displayName}</div>}
      {contextParts.length > 0 && (
        <div className="flex flex-wrap gap-1.5 mb-1">
          {contextParts.map((part, index) => {
            const contextMeta = (part.metadata as Record<string, unknown> | undefined)?.kagent_context as
              | { kind?: string; namespace?: string; scope?: string; name?: string }
              | undefined;
            return (
              <ChatContextChip
                key={index}
                kind={contextMeta?.kind || "resource"}
                namespace={contextMeta?.namespace}
                scope={contextMeta?.scope}
                name={contextMeta?.name || "unknown"}
                text={part.text}
              />
            );
          })}
        </div>
      )}
      {content && <TruncatableText content={String(content)} className="break-words text-primary-foreground" />}
      {fileParts.length > 0 && (
        <div className="flex flex-wrap gap-1.5 mt-1">
          {fileParts.map((part, index) => (
            <AttachmentChip key={index} name={part.file?.name || "attachment"} />
          ))}
        </div>
      )}
      {source !== "user" && (
        <div className="flex mt-2 justify-end items-center gap-2">
          {tokenStats && <TokenStatsTooltip stats={tokenStats} />}
          {messageId !== undefined && (
            <>
              <button
                onClick={() => handleFeedback(true)}
                className="p-1 rounded-full hover:bg-gray-200 dark:hover:bg-gray-700 transition-colors"
                aria-label="Thumbs up"
              >
                <ThumbsUp className="w-4 h-4" />
              </button>
              <button
                onClick={() => handleFeedback(false)}
                className="p-1 rounded-full hover:bg-gray-200 dark:hover:bg-gray-700 transition-colors"
                aria-label="Thumbs down"
              >
                <ThumbsDown className="w-4 h-4" />
              </button>
            </>
          )}
        </div>
      )}
    </div>

    {messageId && (
      <FeedbackDialog
        isOpen={feedbackDialogOpen}
        onClose={() => setFeedbackDialogOpen(false)}
        isPositive={isPositiveFeedback}
        messageId={numericMessageId}
      />
    )}
  </div>
}
