"use client";

import type React from "react";
import { useState, useRef, useEffect, useMemo } from "react";
import { ArrowBigUp, ArrowDown, Paperclip, X, Loader2, Mic, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useSpeechRecognition } from "@/hooks/useSpeechRecognition";
import { Textarea } from "@/components/ui/textarea";
import { ScrollArea } from "@/components/ui/scroll-area";
import ChatMessage from "@/components/chat/ChatMessage";
import ChatModelSwitcher from "@/components/chat/ChatModelSwitcher";
import ChatToolsPanel from "@/components/chat/ChatToolsPanel";
import AttachmentChip from "@/components/chat/AttachmentChip";
import ArtifactsPanel from "@/components/chat/ArtifactsPanel";
import ChatContextPicker from "@/components/chat/ChatContextPicker";
import ChatContextChip from "@/components/chat/ChatContextChip";
import { getClusterResourceContext, getJenkinsResourceContext, type ClusterResourceContext, type ClusterResourceItem } from "@/app/actions/cluster";
import StreamingMessage from "./StreamingMessage";
import SessionTokenStatsDisplay from "@/components/chat/TokenStats";
import type { TokenStats, Session, ChatStatus, ToolDecision } from "@/types";
import StatusDisplay from "./StatusDisplay";
import { createSession, getSessionTasks, checkSessionExists } from "@/app/actions/sessions";
import { deriveSessionTitle, isPlaceholderSessionTitle } from "@/lib/sessionTitle";
import { normalizeSessionTimestamps } from "@/lib/sessionTimestamps";
import { getAgentWithResolvedKind, waitForSandboxAgentReady } from "@/app/actions/agents";
import { getUiRuntimeConfig } from "@/app/actions/config";
import { DEFAULT_STREAM_TIMEOUT_MS } from "@/lib/constants";
import { toast } from "sonner";
import { useRouter } from "next/navigation";
import { createMessageHandlers, extractMessagesFromTasks, extractApprovalMessagesFromTasks, extractTokenStatsFromTasks, extractFileArtifactsFromTasks, createMessage, countSendGuardComparableMessages, countBackendBackedComparableMessages, ADKMetadata, FileArtifact, ProcessedToolCallData } from "@/lib/messageHandlers";
import { kagentA2AClient } from "@/lib/a2aClient";
import { formatA2AClientError } from "@/lib/a2aErrors";
import { useChatAgentDescription, useChatRunInSandbox, useChatSubstrateSandbox } from "@/components/chat/ChatAgentContext";
import { v4 as uuidv4 } from "uuid";
import { getStatusPlaceholder, mapA2AStateToStatus } from "@/lib/statusUtils";
import { Message, DataPart, FilePart, Task, TaskState } from "@a2a-js/sdk";

// Task states where the agent is actively processing — resubscribe to live stream.
const RESUBSCRIBE_TASK_STATES: TaskState[] = ["submitted", "working"];
// Task states that mean the session is busy (used by the cross-tab send guard).
const ACTIVE_TASK_STATES: TaskState[] = ["submitted", "working", "input-required"];

// Attachment limits: individual file size and count per message.
const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024;
const MAX_ATTACHMENTS = 5;
// Cluster-context injections per message.
const MAX_CONTEXTS = 5;

// Builds the A2A text part for one injected cluster context. The metadata
// marker lets the UI render a chip instead of the raw text; runtimes pass
// text parts straight to the model.
const contextToTextPart = (context: ClusterResourceContext) => ({
  kind: "text" as const,
  text: context.text,
  metadata: {
    kagent_context: {
      provider: context.provider,
      kind: context.kind,
      namespace: context.namespace,
      scope: context.scope,
      name: context.name,
    },
  },
});

interface PendingAttachment {
  name: string;
  mimeType: string;
  /** base64-encoded content (without the data: URI prefix). */
  bytes: string;
  size: number;
}

const readFileAsBase64 = (file: File): Promise<string> =>
  new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result as string;
      resolve(result.split(",", 2)[1] ?? "");
    };
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

const attachmentToFilePart = (attachment: PendingAttachment): FilePart => ({
  kind: "file",
  file: {
    bytes: attachment.bytes,
    mimeType: attachment.mimeType || "application/octet-stream",
    name: attachment.name,
  },
});

// Display-only file part: the chat transcript only renders the file name, so
// the copy kept in React state omits the base64 bytes (which can be tens of
// MB) to avoid retaining them for the session lifetime.
const attachmentToDisplayPart = (attachment: PendingAttachment): FilePart => ({
  kind: "file",
  file: {
    bytes: "",
    mimeType: attachment.mimeType || "application/octet-stream",
    name: attachment.name,
  },
});


interface ChatInterfaceProps {
  selectedAgentName: string;
  selectedNamespace: string;
  selectedSession?: Session | null;
  sessionId?: string;
}

export default function ChatInterface({ selectedAgentName, selectedNamespace, selectedSession, sessionId }: ChatInterfaceProps) {
  const runInSandbox = useChatRunInSandbox();
  const substrateSandbox = useChatSubstrateSandbox();
  const agentDescription = useChatAgentDescription();
  const router = useRouter();
  const containerRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  // Tracks whether the user is scrolled near the bottom of the message list.
  // Auto-scroll only follows new content while this is true, so reading
  // history isn't interrupted by streaming updates.
  const isNearBottomRef = useRef(true);
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  const [currentInputMessage, setCurrentInputMessage] = useState("");
  const [pendingAttachments, setPendingAttachments] = useState<PendingAttachment[]>([]);
  const [pendingContexts, setPendingContexts] = useState<ClusterResourceContext[]>([]);
  const [sessionArtifacts, setSessionArtifacts] = useState<FileArtifact[]>([]);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const openContextPickerRef = useRef<(() => void) | null>(null);

  const [chatStatus, setChatStatus] = useState<ChatStatus>("ready");

  const [session, setSession] = useState<Session | null>(selectedSession || null);
  const [storedMessages, setStoredMessages] = useState<Message[]>([]);
  const [streamingMessages, setStreamingMessages] = useState<Message[]>([]);
  const [streamingContent, setStreamingContent] = useState<string>("");
  const [isStreaming, setIsStreaming] = useState<boolean>(false);
  const abortControllerRef = useRef<AbortController | null>(null);
  const isFirstAssistantChunkRef = useRef(true);
  const [isLoading, setIsLoading] = useState<boolean>(false);
  const [sessionNotFound, setSessionNotFound] = useState<boolean>(false);
  const isCreatingSessionRef = useRef<boolean>(false);
  const [isFirstMessage, setIsFirstMessage] = useState<boolean>(!sessionId);
  const [sessionStats, setSessionStats] = useState<TokenStats>({ total: 0, prompt: 0, completion: 0 });
  // Mutable ref so pendingTurnStats survives re-renders between A2A stream events
  const pendingTurnStatsRef = useRef<TokenStats | undefined>(undefined);
  const [pendingDecisions, setPendingDecisions] = useState<Record<string, ToolDecision>>({});
  const pendingDecisionsRef = useRef<Record<string, ToolDecision>>({});
  /** Per-tool rejection reasons collected as the user rejects individual tools. */
  const pendingRejectionReasonsRef = useRef<Record<string, string>>({});
  // Stream inactivity timeout (ms), configurable via Helm (ui.streamTimeoutSeconds).
  const streamTimeoutMsRef = useRef<number>(DEFAULT_STREAM_TIMEOUT_MS);

  useEffect(() => {
    let cancelled = false;
    getUiRuntimeConfig()
      .then((config) => {
        if (!cancelled) streamTimeoutMsRef.current = config.streamTimeoutMs;
      })
      .catch(() => {
        /* keep default on failure */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const {
    isListening,
    isSupported: isVoiceSupported,
    startListening,
    stopListening,
    error: voiceError,
  } = useSpeechRecognition({
    onResult(transcriptText) {
      setCurrentInputMessage(transcriptText);
    },
    onError(msg) {
      toast.error(msg);
    },
  });

  const agentContext = useMemo(() => ({
    namespace: selectedNamespace,
    agentName: selectedAgentName
  }), [selectedNamespace, selectedAgentName]);

  const allMessages = useMemo(() => [...storedMessages, ...streamingMessages], [storedMessages, streamingMessages]);

  // Prompt tokens of the latest turn — the closest available approximation of
  // the current context size (used for the context-window meter).
  const contextTokens = useMemo(() => {
    for (let i = allMessages.length - 1; i >= 0; i--) {
      const tokenStats = (allMessages[i].metadata as Record<string, unknown> | undefined)?.tokenStats as
        | TokenStats
        | undefined;
      if (tokenStats?.prompt) return tokenStats.prompt;
    }
    return undefined;
  }, [allMessages]);

  const { handleMessageEvent } = useMemo(() => createMessageHandlers({
    setMessages: setStreamingMessages,
    setIsStreaming,
    setStreamingContent,
    setChatStatus,
    setSessionStats,
    pendingTurnStats: pendingTurnStatsRef,
    onFileArtifact: (artifact) => {
      setSessionArtifacts(prev => [...prev.filter(a => a.name !== artifact.name), artifact]);
      toast.info(`Agent generated ${artifact.name}`);
    },
    agentContext: {
      namespace: selectedNamespace,
      agentName: selectedAgentName
    }
  }), [selectedNamespace, selectedAgentName]);

  useEffect(() => {
    async function initializeChat() {
      setSessionStats({ total: 0, prompt: 0, completion: 0 });
      setStreamingMessages([]);
      setPendingDecisions({});
      pendingDecisionsRef.current = {};
      pendingRejectionReasonsRef.current = {};
      pendingTurnStatsRef.current = undefined;
      // The ChatInterface instance is reused across session switches; re-arm
      // follow-scrolling so a new session opens pinned to its latest message.
      isNearBottomRef.current = true;
      setShowJumpToLatest(false);
      setSessionArtifacts([]);
      setPendingContexts([]);

      // Skip completely if this is a first message session creation flow
      if (isFirstMessage || isCreatingSessionRef.current) {
        return;
      }

      // Skip loading state for empty sessionId (new chat)
      if (!sessionId) {
        setIsLoading(false);
        setStoredMessages([]);
        return;
      }

      setIsLoading(true);
      setSessionNotFound(false);

      let activeTask: Task | undefined;

      try {
        const sessionExistsResponse = await checkSessionExists(sessionId);
        if (sessionExistsResponse.error || !sessionExistsResponse.data) {
          setSessionNotFound(true);
          setIsLoading(false);
          return;
        }

        const messagesResponse = await getSessionTasks(sessionId);
        if (messagesResponse.error) {
          toast.error("Failed to load messages");
          setIsLoading(false);
          return;
        }
        if (!messagesResponse.data || messagesResponse?.data?.length === 0) {
          setStoredMessages([]);
          setSessionStats({ total: 0, prompt: 0, completion: 0 });
        }
        else {
          const extractedMessages = extractMessagesFromTasks(messagesResponse.data);
          setSessionStats(extractTokenStatsFromTasks(messagesResponse.data));
          setSessionArtifacts(extractFileArtifactsFromTasks(messagesResponse.data));

          // Resolved approvals are already inline in extractedMessages (with
          // approved/rejected badges). Only pending approvals need appending.
          const { messages: pendingApprovalMessages, hasPendingApproval } = extractApprovalMessagesFromTasks(messagesResponse.data);

          setStoredMessages(
            hasPendingApproval
              ? [...extractedMessages, ...pendingApprovalMessages]
              : extractedMessages
          );

          if (hasPendingApproval) {
            setChatStatus("input_required");
          } else {
            // Check for a task still actively running (not input-required, not terminal).
            // input-required is excluded: it needs the approval UI, not a stream.
            activeTask = messagesResponse.data.findLast(
              task => RESUBSCRIBE_TASK_STATES.includes(task.status?.state as TaskState)
            );
          }
        }
      } catch (error) {
        console.error("Error loading messages:", error);
        toast.error("Error loading messages");
        setSessionNotFound(true);
        setIsLoading(false);
        return;
      }

      setIsLoading(false);

      if (activeTask) {
        setChatStatus(mapA2AStateToStatus(activeTask.status?.state as TaskState));
        await streamResubscribedTask(activeTask.id);
      }
    }

    initializeChat();
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, selectedAgentName, selectedNamespace, isFirstMessage]);

  const getScrollViewport = () =>
    containerRef.current?.querySelector('[data-radix-scroll-area-viewport]') as HTMLElement | null;

  const scrollToBottom = () => {
    const viewport = getScrollViewport();
    if (viewport) {
      viewport.scrollTop = viewport.scrollHeight;
    }
    isNearBottomRef.current = true;
    setShowJumpToLatest(false);
  };

  // Track scroll position so auto-scroll only engages when the user is
  // already at (or near) the bottom of the conversation.
  useEffect(() => {
    const viewport = getScrollViewport();
    if (!viewport) return;
    const onScroll = () => {
      const nearBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 80;
      isNearBottomRef.current = nearBottom;
      setShowJumpToLatest(!nearBottom);
    };
    viewport.addEventListener("scroll", onScroll, { passive: true });
    return () => viewport.removeEventListener("scroll", onScroll);
  }, []);

  useEffect(() => {
    if (!isNearBottomRef.current) return;
    const viewport = getScrollViewport();
    if (viewport) {
      viewport.scrollTop = viewport.scrollHeight;
    }
  }, [storedMessages, streamingMessages, streamingContent]);

  // Auto-grow the composer textarea with its content, capped so long drafts
  // scroll internally instead of pushing the messages out of view.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  }, [currentInputMessage]);

  /** Validate and queue files as pending attachments for the next message. */
  const addAttachments = async (files: File[]) => {
    if (files.length === 0) return;
    const accepted: PendingAttachment[] = [];
    for (const file of files) {
      if (pendingAttachments.length + accepted.length >= MAX_ATTACHMENTS) {
        toast.error(`At most ${MAX_ATTACHMENTS} files per message`);
        break;
      }
      if (file.size > MAX_ATTACHMENT_BYTES) {
        toast.error(`${file.name} exceeds the ${Math.round(MAX_ATTACHMENT_BYTES / 1024 / 1024)}MB limit`);
        continue;
      }
      try {
        const bytes = await readFileAsBase64(file);
        accepted.push({ name: file.name, mimeType: file.type, bytes, size: file.size });
      } catch (error) {
        console.error("Failed to read file:", error);
        toast.error(`Failed to read ${file.name}`);
      }
    }
    if (accepted.length > 0) {
      setPendingAttachments(prev => [...prev, ...accepted]);
    }
  };

  const handleFileInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    addAttachments(Array.from(e.target.files ?? []));
    // Reset so selecting the same file again re-triggers the event.
    e.target.value = "";
  };

  const handlePaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData?.files ?? []);
    if (files.length > 0) {
      e.preventDefault();
      addAttachments(files);
    }
  };

  const removeAttachment = (index: number) => {
    setPendingAttachments(prev => prev.filter((_, i) => i !== index));
  };

  /** Fetch and queue a cluster resource's context for the next message. */
  const handlePickContext = async (item: ClusterResourceItem) => {
    if (pendingContexts.length >= MAX_CONTEXTS) {
      toast.error(`At most ${MAX_CONTEXTS} context items per message`);
      return;
    }
    const exists = pendingContexts.some(
      c =>
        c.provider === (item.provider || "kubernetes") &&
        c.kind === item.kind &&
        c.namespace === (item.namespace || undefined) &&
        c.scope === (item.scope || undefined) &&
        c.name === item.name
    );
    if (exists) return;

    const response =
      item.provider === "jenkins"
        ? await getJenkinsResourceContext(item.kind, item.name, item.scope)
        : await getClusterResourceContext(item.kind, item.name, item.namespace);
    if (response.error || !response.data) {
      toast.error(response.message || "Failed to fetch resource context");
      return;
    }
    const context = response.data;
    setPendingContexts(prev => [...prev, context]);
  };

  const removeContext = (index: number) => {
    setPendingContexts(prev => prev.filter((_, i) => i !== index));
  };

  const handleSendMessage = async (e: React.FormEvent) => {
    e.preventDefault();
    const hasContent = currentInputMessage.trim() || pendingAttachments.length > 0 || pendingContexts.length > 0;
    if (!hasContent || !selectedAgentName || !selectedNamespace) {
      return;
    }

    // Stop voice recording if active before sending
    if (isListening) {
      stopListening();
    }

    const userMessageText = currentInputMessage;
    const attachments = pendingAttachments;
    const contexts = pendingContexts;
    const restoreDraft = () => {
      setCurrentInputMessage(userMessageText);
      setPendingAttachments(attachments);
      setPendingContexts(contexts);
    };

    // Cross-tab guard: fetch the latest session state before mutating anything.
    // Two cases: (1) another tab is still streaming — reconnect instead of sending;
    // (2) another tab completed a turn we haven't loaded — reload so the user sees
    // the full context before their next message goes out.
    const guardSessionId = session?.id || sessionId;
    if (guardSessionId) {
      // Compare visible messages against the backend snapshot. Completed
      // same-tab stream messages remain in streamingMessages until the next
      // send promotes them, but only backend-backed matches count as local.
      const guardResult = await checkAndSyncSessionBeforeAction(guardSessionId, {
        localMessages: allMessages,
        messages: {
          inFlight: "This session is already being processed — reconnecting to live updates",
          inputRequired: "Session is awaiting your input — please review before sending",
          staleOrChanged: "New messages loaded — please review before sending",
        },
      });
      if (guardResult === "blocked") return;
    }

    setCurrentInputMessage("");
    setPendingAttachments([]);
    setPendingContexts([]);
    setChatStatus("thinking");
    // Sending a message always re-engages follow-scrolling.
    scrollToBottom();
    setStoredMessages(prev => [...prev, ...streamingMessages]);
    setStreamingMessages([]);
    setStreamingContent(""); // Reset streaming content for new message
    setPendingDecisions({});
    pendingDecisionsRef.current = {};
    pendingRejectionReasonsRef.current = {};
    pendingTurnStatsRef.current = undefined;

    const messageId = uuidv4();

    // For new sessions or when no stored messages exist, show the user message immediately
    const userMessage: Message = {
      kind: "message",
      messageId,
      role: "user",
      // Only include a text part when there is text — some providers reject
      // empty text content blocks (attachment-only sends).
      parts: [
        ...contexts.map(contextToTextPart),
        ...(userMessageText ? [{ kind: "text" as const, text: userMessageText }] : []),
        ...attachments.map(attachmentToDisplayPart),
      ],
      contextId: guardSessionId,
      metadata: {
        timestamp: Date.now()
      }
    };

    // Add user message to streaming messages to show immediately
    // (will be replaced by server response that includes the user message)
    setStreamingMessages([userMessage]);

    isFirstAssistantChunkRef.current = true;

    try {
      let currentSessionId = session?.id || sessionId;
      // Track whether we just created a session in this invocation. If so, the
      // rename block below must be skipped: the title was already set at creation
      // time, and session React state hasn't yet re-rendered (so session?.name
      // is still null, which would make isPlaceholderSessionTitle return true
      // incorrectly and queue a redundant — potentially hanging — POST /sessions).
      let justCreatedSession = false;

      // If there's no session, create one
      if (!currentSessionId) {
        try {
          // Set flags to prevent loading screens during first message
          isCreatingSessionRef.current = true;
          setIsFirstMessage(true);

          const newSessionResponse = await createSession({
            agent_ref: `${selectedNamespace}/${selectedAgentName}`,
            name: deriveSessionTitle(userMessageText),
          });

          if (newSessionResponse.error || !newSessionResponse.data) {
            toast.error("Failed to create session");
            setChatStatus("error");
            restoreDraft();
            isCreatingSessionRef.current = false;
            return;
          }

          currentSessionId = newSessionResponse.data.id;
          setSession(normalizeSessionTimestamps(newSessionResponse.data));

          // Update URL without triggering navigation or component reload
          const newUrl = `/agents/${selectedNamespace}/${selectedAgentName}/chat/${currentSessionId}`;
          window.history.replaceState({}, '', newUrl);

          // Dispatch a custom event to notify that a new session was created
          // Include the full session object to avoid needing a DB reload
          const newSessionEvent = new CustomEvent('new-session-created', {
            detail: {
              agentRef: `${selectedNamespace}/${selectedAgentName}`,
              session: normalizeSessionTimestamps(newSessionResponse.data),
            }
          });
          window.dispatchEvent(newSessionEvent);
          justCreatedSession = true;
        } catch (error) {
          console.error("Error creating session:", error);
          toast.error("Error creating session");
          setChatStatus("error");
          restoreDraft();
          isCreatingSessionRef.current = false;
          return;
        }
      }

      if (
        !justCreatedSession &&
        currentSessionId &&
        storedMessages.length === 0 &&
        isPlaceholderSessionTitle(session?.name)
      ) {
        const title = deriveSessionTitle(userMessageText);
        if (title) {
          try {
            const renameResponse = await createSession({
              id: currentSessionId,
              agent_ref: `${selectedNamespace}/${selectedAgentName}`,
              name: title,
            });
            if (!renameResponse.error && renameResponse.data) {
              const updatedSession = normalizeSessionTimestamps(renameResponse.data, new Date());
              setSession(updatedSession);
              window.dispatchEvent(
                new CustomEvent("new-session-created", {
                  detail: {
                    agentRef: `${selectedNamespace}/${selectedAgentName}`,
                    session: updatedSession,
                  },
                }),
              );
            }
          } catch (error) {
            console.error("Error updating session title:", error);
          }
        }
      }

      const a2aMessage = createMessage(userMessageText, "user", {
        messageId,
        contextId: currentSessionId,
      });
      if (attachments.length > 0 || contexts.length > 0) {
        // Drop an empty text part so attachment/context-only sends don't ship
        // an empty text content block (rejected by some providers).
        if (!userMessageText) {
          a2aMessage.parts = a2aMessage.parts.filter(
            part => part.kind !== "text" || (part as { text: string }).text !== "",
          );
        }
        // Cluster context goes ahead of the user's question.
        a2aMessage.parts.unshift(...contexts.map(contextToTextPart));
        a2aMessage.parts.push(...attachments.map(attachmentToFilePart));
      }

      await streamA2AMessage(a2aMessage, {
        errorLabel: "Streaming failed",
        onError: restoreDraft,
        sessionIdForWait: currentSessionId,
      });
    } catch (error) {
      console.error("Error sending message or creating session:", error);
      toast.error("Error sending message or creating session");
      setChatStatus("error");
      restoreDraft();
    }
  };

  const consumeStream = async (stream: AsyncIterable<unknown>) => {
    let timeoutTimer: NodeJS.Timeout | null = null;
    let streamActive = true;

    const formatTimeout = (ms: number): string => {
      const mins = ms / 60000;
      return mins >= 1 ? `${Math.ceil(mins)} minutes` : `${Math.round(ms / 1000)} seconds`;
    };

    const startTimeout = () => {
      if (timeoutTimer) clearTimeout(timeoutTimer);
      const streamTimeoutMs = streamTimeoutMsRef.current;
      timeoutTimer = setTimeout(() => {
        if (streamActive) {
          const label = formatTimeout(streamTimeoutMs);
          console.error(`⏰ Stream timeout - no events received for ${label}`);
          toast.error(`⏰ Stream timed out - no events received for ${label}`);
          streamActive = false;
          abortControllerRef.current?.abort();
        }
      }, streamTimeoutMs);
    };
    startTimeout();

    try {
      for await (const event of stream) {
        startTimeout();
        try {
          handleMessageEvent(event as Message);
        } catch (err) {
          console.error("Error handling stream event:", err);
        }
        if (abortControllerRef.current?.signal.aborted) {
          streamActive = false;
          break;
        }
      }
    } finally {
      streamActive = false;
      if (timeoutTimer) clearTimeout(timeoutTimer);
    }
  };

  const reloadSessionFromDB = async () => {
    try {
      const currentSessionId = session?.id || sessionId;
      if (!currentSessionId) return;
      const latest = await getSessionTasks(currentSessionId);
      if (latest.data && latest.data.length > 0) {
        const extractedMessages = extractMessagesFromTasks(latest.data);
        setSessionArtifacts(extractFileArtifactsFromTasks(latest.data));
        const { messages: pendingApprovalMessages, hasPendingApproval } = extractApprovalMessagesFromTasks(latest.data);
        setStoredMessages(
          hasPendingApproval
            ? [...extractedMessages, ...pendingApprovalMessages]
            : extractedMessages
        );
        setSessionStats(extractTokenStatsFromTasks(latest.data));
        setStreamingMessages([]);
        if (hasPendingApproval) {
          setChatStatus("input_required");
        }
      }
    } catch {
      // Best-effort reload.
    }
  };

  /**
   * Shared streaming helper used by both handleSendMessage and
   * sendApprovalDecision.  Handles the abort controller, timeout, event loop,
   * and base cleanup.
   */
  const streamA2AMessage = async (
    a2aMessage: Message,
    opts?: {
      errorLabel?: string;
      onError?: () => void;
      onFinally?: () => void;
      /** Session id for readiness polling when React state may lag. */
      sessionIdForWait?: string;
    },
  ) => {
    abortControllerRef.current = new AbortController();
    isFirstAssistantChunkRef.current = true;

    try {
      const sid = opts?.sessionIdForWait ?? session?.id ?? sessionId;
      if (runInSandbox && !sid) {
        throw new Error("Session is required before messaging a Sandbox agent");
      }
      if (runInSandbox && sid) {
        let loadingToast: string | number | undefined;
        const slowToast = setTimeout(() => {
          loadingToast = toast.loading(
            substrateSandbox ? "Starting chat session…" : "Starting sandbox workload…",
          );
        }, 600);
        try {
          if (substrateSandbox) {
            // ActorTemplate readiness only; per-session actors resume on the A2A request.
            const agentRes = await getAgentWithResolvedKind(selectedAgentName, selectedNamespace);
            if (!agentRes.data?.deploymentReady) {
              throw new Error("Sandbox agent is still starting. Wait a moment and try again.");
            }
          } else {
            const ready = await waitForSandboxAgentReady(selectedAgentName, selectedNamespace);
            if (!ready.ok) {
              throw new Error(ready.error ?? "Sandbox workload not ready");
            }
          }
          clearTimeout(slowToast);
          if (loadingToast !== undefined) toast.dismiss(loadingToast);
        } catch (waitErr) {
          clearTimeout(slowToast);
          if (loadingToast !== undefined) toast.dismiss(loadingToast);
          throw waitErr;
        }
      }
      isCreatingSessionRef.current = false;
      const sendParams = { message: a2aMessage, metadata: {} };
      const stream = await kagentA2AClient.sendMessageStream(
        selectedNamespace,
        selectedAgentName,
        sendParams,
        abortControllerRef.current?.signal,
        runInSandbox
      );

      await consumeStream(stream);
    } catch (error: unknown) {
      if (error instanceof Error && error.name === "AbortError") {
        setChatStatus("ready");
      } else {
        toast.error(`${opts?.errorLabel || "Request failed"}: ${formatA2AClientError(error instanceof Error ? error.message : "Unknown error")}`);
        setChatStatus("error");
        opts?.onError?.();
      }
      setIsStreaming(false);
      setStreamingContent("");
    } finally {
      abortControllerRef.current = null;
      opts?.onFinally?.();
    }
  };

  const streamResubscribedTask = async (taskId: string) => {
    const isTerminalError = (err: unknown) => {
      if (!(err instanceof Error)) return false;
      const msg = err.message.toLowerCase();
      return msg.includes("terminal state") || msg.includes("task not found") || msg.includes("404");
    };

    abortControllerRef.current = new AbortController();
    isFirstAssistantChunkRef.current = true;

    try {
      const stream = await kagentA2AClient.resubscribeStream(
        selectedNamespace,
        selectedAgentName,
        taskId,
        abortControllerRef.current.signal,
        runInSandbox,
      );

      await consumeStream(stream);

      // Stream ended cleanly — reload final state from DB and settle.
      await reloadSessionFromDB();
    } catch (error: unknown) {
      if (error instanceof Error && error.name !== "AbortError" && !isTerminalError(error)) {
        console.error("Resubscribe failed:", error);
      }
      // Terminal, AbortError, or unexpected error — reload whatever state we have.
      if (!(error instanceof Error && error.name === "AbortError")) {
        await reloadSessionFromDB();
      }
    } finally {
      abortControllerRef.current = null;
      // Don't override input_required that reloadSessionFromDB() may have set.
      setChatStatus(prev => prev === "input_required" ? prev : "ready");
      setIsStreaming(false);
      setStreamingContent("");
    }
  };

  /**
   * Cross-tab guard: fetch the latest session state and sync before any action
   * that would mutate the session. Returns "proceed" if safe, "blocked" if the
   * action was superseded and the handler should return early.
   *
   * HITL mode (expectedTaskId provided): verifies the specific task is still
   * input-required; resubscribes or reloads if another tab already responded.
   *
   * Send-guard mode (no expectedTaskId): checks for any active task and for
   * stale local messages; blocks and syncs if either is detected.
   */
  const checkAndSyncSessionBeforeAction = async (
    guardSessionId: string,
    opts: {
      expectedTaskId?: string;
      localMessages?: Message[];
      localMessageCount?: number;
      messages: {
        inFlight: string;
        inputRequired?: string;
        staleOrChanged: string;
      };
    }
  ): Promise<"proceed" | "blocked"> => {
    let tasksCheck: Awaited<ReturnType<typeof getSessionTasks>>;
    try {
      tasksCheck = await getSessionTasks(guardSessionId);
    } catch {
      // Guard is best-effort: if the check fails, let the action proceed.
      return "proceed";
    }
    if (!tasksCheck.data) return "proceed";

    if (opts.expectedTaskId) {
      const expectedTask = tasksCheck.data.findLast(task => task.id === opts.expectedTaskId);
      if ((expectedTask?.status?.state as TaskState | undefined) !== "input-required") {
        const inFlightTask = tasksCheck.data.findLast(
          task => RESUBSCRIBE_TASK_STATES.includes(task.status?.state as TaskState)
        );
        if (inFlightTask) {
          toast.info(opts.messages.inFlight);
          setChatStatus(mapA2AStateToStatus(inFlightTask.status?.state as TaskState));
          await streamResubscribedTask(inFlightTask.id);
        } else {
          await reloadSessionFromDB();
          toast.info(opts.messages.staleOrChanged);
        }
        return "blocked";
      }
      return "proceed";
    }

    const inFlightTask = tasksCheck.data.findLast(
      task => ACTIVE_TASK_STATES.includes(task.status?.state as TaskState)
    );
    if (inFlightTask) {
      if ((inFlightTask.status?.state as TaskState) === "input-required") {
        await reloadSessionFromDB();
        toast.info(opts.messages.inputRequired ?? opts.messages.staleOrChanged);
      } else {
        toast.info(opts.messages.inFlight);
        setChatStatus(mapA2AStateToStatus(inFlightTask.status?.state as TaskState));
        await streamResubscribedTask(inFlightTask.id);
      }
      return "blocked";
    }

    if (opts.localMessages !== undefined || opts.localMessageCount !== undefined) {
      const dbMessages = extractMessagesFromTasks(tasksCheck.data);
      const dbMessageCount = countSendGuardComparableMessages(dbMessages);
      const localMessageCount = opts.localMessages !== undefined
        ? countBackendBackedComparableMessages(opts.localMessages, dbMessages)
        : opts.localMessageCount;
      if (localMessageCount !== undefined && dbMessageCount > localMessageCount) {
        await reloadSessionFromDB();
        toast.info(opts.messages.staleOrChanged);
        return "blocked";
      }
    }

    return "proceed";
  };

  const handleCancel = (e: React.FormEvent) => {
    e.preventDefault();

    if (abortControllerRef.current) {
      abortControllerRef.current.abort();
    }

    setIsStreaming(false);
    setStreamingContent("");
    setChatStatus("ready");
    toast.error("Request cancelled");
  };

  // Collect all pending tool call IDs from ToolApprovalRequest messages
  const getPendingApprovalToolIds = (): { toolIds: string[]; taskId: string | undefined } => {
    const toolIds: string[] = [];
    let taskId: string | undefined;
    const allCurrentMessages = [...storedMessages, ...streamingMessages];
    for (const msg of allCurrentMessages) {
      const meta = msg.metadata as ADKMetadata | undefined;
      if (meta?.originalType !== "ToolApprovalRequest") continue;
      // Skip approval messages that already have a decision (from previous cycles)
      if (meta?.approvalDecision) continue;
      if (!taskId) taskId = msg.taskId;
      const toolCallData = meta.toolCallData as ProcessedToolCallData[] | undefined;
      if (toolCallData) {
        for (const tc of toolCallData) {
          if (tc.id) toolIds.push(tc.id);
        }
      }
    }
    return { toolIds, taskId };
  };

  const sendApprovalDecision = async (
    decisionData: Record<string, unknown>,
    displayText: string,
  ) => {
    const currentSessionId = session?.id || sessionId;

    // Find the taskId first so the guard can verify the task is still input-required.
    const { taskId: approvalTaskId } = getPendingApprovalToolIds();

    // Cross-tab guard: another tab may have already submitted this approval.
    if (currentSessionId && approvalTaskId) {
      const guardResult = await checkAndSyncSessionBeforeAction(currentSessionId, {
        expectedTaskId: approvalTaskId,
        messages: {
          inFlight: "Another tab already responded — reconnecting to live updates",
          staleOrChanged: "Session state changed — please review",
        },
      });
      if (guardResult === "blocked") return;
    }

    setChatStatus("thinking");
    setStreamingContent("");

    // Stamp approvalDecision on the current pending approval messages so they
    // are excluded from getPendingApprovalToolIds on future HITL cycles.
    // approvalDecision is either a uniform ToolDecision or a per-tool map
    // (Record<string, ToolDecision>) for batch decisions.
    const stampDecision = (msgs: Message[]) => msgs.map(m => {
      const meta = m.metadata as Record<string, unknown> | undefined;
      if (meta?.originalType === "ToolApprovalRequest" && !meta.approvalDecision) {
        const dt = decisionData.decision_type as string;
        if (dt === "batch") {
          // Store the per-tool decisions map so ToolCallDisplay can resolve
          // each inner tool independently.
          const decisions = decisionData.decisions as Record<string, ToolDecision>;
          return { ...m, metadata: { ...meta, approvalDecision: decisions } };
        } else {
          return { ...m, metadata: { ...meta, approvalDecision: dt as ToolDecision } };
        }
      }
      return m;
    });
    setStreamingMessages(stampDecision);
    setStoredMessages(stampDecision);

    const messageId = uuidv4();
    const a2aMessage: Message = {
      kind: "message",
      messageId,
      role: "user",
      parts: [
        { kind: "data", data: decisionData, metadata: {} } as DataPart,
        { kind: "text", text: displayText },
      ],
      contextId: currentSessionId,
      taskId: approvalTaskId,
      metadata: {
        timestamp: Date.now(),
      },
    };

    await streamA2AMessage(a2aMessage, {
      errorLabel: "Approval failed",
      sessionIdForWait: currentSessionId,
      onFinally: () => {
        // Ensure chat state resets after approval stream ends
        setIsStreaming(false);
        setStreamingContent("");
        setPendingDecisions({});
        pendingDecisionsRef.current = {};
        pendingRejectionReasonsRef.current = {};
        // Only reset "thinking" → "ready".  Do NOT reset "input_required" —
        // handleMessageEvent may have already set it for the next HITL cycle
        // during this same stream.
        setChatStatus(prev => prev === "thinking" ? "ready" : prev);
      },
    });
  };

  // Submit all collected decisions to the backend. Called when every pending
  // tool has a decision recorded in `pendingDecisions`, or immediately for
  // "approve all" / uniform decisions.
  const submitDecisions = (decisions: Record<string, ToolDecision>) => {
    const values = Object.values(decisions);
    const allApprove = values.every(v => v === "approve");
    const allReject = values.every(v => v !== "approve");
    const reasons = pendingRejectionReasonsRef.current;

    if (allApprove) {
      // Uniform approve — no need for batch
      sendApprovalDecision(
        { decision_type: "approve" },
        "Approved",
      );
    } else if (allReject && Object.values(reasons).length === 0) {
      // Uniform reject without reason, otherwise fall through to batch
      sendApprovalDecision(
        { decision_type: "reject" },
        "Rejected",
      );
    } else {
      // Mixed decisions — use batch mode with per-tool decisions.
      // For subagent HITL the keys are inner subagent tool IDs; the backend
      // detects this via hitl_parts in the pending confirmation payload and
      // forwards the batch to the subagent.
      const decisionData: Record<string, unknown> = { decision_type: "batch", decisions };
      // Include per-tool rejection reasons for denied tools (if any)
      const rejectedReasons: Record<string, string> = {};
      for (const [toolId, decision] of Object.entries(decisions)) {
        if (decision === "reject" && reasons[toolId]) {
          rejectedReasons[toolId] = reasons[toolId];
        }
      }
      if (Object.keys(rejectedReasons).length > 0) {
        decisionData.rejection_reasons = rejectedReasons;
      }
      sendApprovalDecision(
        decisionData,
        `Batch decision: ${values.filter(v => v === "approve").length} approved, ${values.filter(v => v !== "approve").length} rejected`,
      );
    }
  };

  const recordDecision = (toolCallId: string, decision: ToolDecision, reason?: string) => {
    const updated = { ...pendingDecisionsRef.current, [toolCallId]: decision };
    pendingDecisionsRef.current = updated;
    setPendingDecisions(updated);

    // Track rejection reason (if any)
    if (decision === "reject" && reason) {
      const updatedReasons = { ...pendingRejectionReasonsRef.current, [toolCallId]: reason };
      pendingRejectionReasonsRef.current = updatedReasons;
    }

    // Check if all pending tools now have a decision
    const { toolIds } = getPendingApprovalToolIds();
    if (toolIds.length > 0 && toolIds.every(id => id in updated)) {
      submitDecisions(updated);
    } else if (toolIds.length === 0) {
      submitDecisions(updated);
    }
  };

  const handleApprove = (toolCallId: string) => {
    recordDecision(toolCallId, "approve");
  };

  /**
   * "Always allow (session)": approves every pending tool and asks the
   * runtime to remember them in session state, so subsequent calls of the
   * same tools skip the approval round-trip for the rest of the session.
   */
  const handleApproveAlways = (_toolCallId: string) => {
    sendApprovalDecision(
      { decision_type: "approve", always_allow: true },
      "Approved (always allow for this session)",
    );
  };

  const handleReject = (toolCallId: string, reason?: string) => {
    recordDecision(toolCallId, "reject", reason);
  };

  /**
   * Handle ask_user answers submitted by the user. Sends an "approve" decision
   * with the answers payload attached, routed to the pending ask_user task.
   */
  const handleAskUserSubmit = async (answers: Array<{ answer: string[] }>) => {
    const currentSessionId = session?.id || sessionId;

    // Find the taskId from the pending AskUserRequest message
    let askUserTaskId: string | undefined;
    const allCurrentMessages = [...storedMessages, ...streamingMessages];
    for (const msg of allCurrentMessages) {
      const meta = msg.metadata as ADKMetadata | undefined;
      if (meta?.originalType === "AskUserRequest" && !meta?.approvalDecision) {
        askUserTaskId = msg.taskId;
        break;
      }
    }

    // Cross-tab guard: another tab may have already answered this question.
    if (currentSessionId && askUserTaskId) {
      const guardResult = await checkAndSyncSessionBeforeAction(currentSessionId, {
        expectedTaskId: askUserTaskId,
        messages: {
          inFlight: "Another tab already responded — reconnecting to live updates",
          staleOrChanged: "Session state changed — please review",
        },
      });
      if (guardResult === "blocked") return;
    }

    setChatStatus("thinking");
    setStreamingContent("");

    // Stamp the ask-user message as resolved so we don't show the form again
    const stampAskUser = (msgs: Message[]) => msgs.map(m => {
      const meta = m.metadata as Record<string, unknown> | undefined;
      if (meta?.originalType === "AskUserRequest" && !meta.approvalDecision) {
        return { ...m, metadata: { ...meta, approvalDecision: "approve", askUserAnswers: answers } };
      }
      return m;
    });
    setStreamingMessages(stampAskUser);
    setStoredMessages(stampAskUser);

    const messageId = uuidv4();
    const a2aMessage: Message = {
      kind: "message",
      messageId,
      role: "user",
      parts: [
        {
          kind: "data",
          data: { decision_type: "approve", ask_user_answers: answers },
          metadata: {},
        } as DataPart,
        { kind: "text", text: "Answered questions" },
      ],
      contextId: currentSessionId,
      taskId: askUserTaskId,
      metadata: { timestamp: Date.now() },
    };

    await streamA2AMessage(a2aMessage, {
      errorLabel: "Ask user response failed",
      sessionIdForWait: currentSessionId,
      onFinally: () => {
        setIsStreaming(false);
        setStreamingContent("");
        setChatStatus(prev => prev === "thinking" ? "ready" : prev);
      },
    });
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Typing "@" at the start of a word opens the cluster context picker.
    if (e.key === "@") {
      const value = textareaRef.current?.value ?? "";
      const cursor = textareaRef.current?.selectionStart ?? value.length;
      const previous = cursor > 0 ? value[cursor - 1] : "";
      if (previous === "" || /\s/.test(previous)) {
        e.preventDefault();
        openContextPickerRef.current?.();
        return;
      }
    }
    if (e.key !== "Enter") return;
    // Don't send while an IME composition is in progress (e.g. Chinese/Japanese
    // input) — Enter is confirming the composition, not submitting. keyCode 229
    // is the legacy signal browsers (notably Safari) emit for the composition
    // commit keydown, where isComposing has already flipped back to false.
    if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
    // Shift+Enter inserts a newline.
    if (e.shiftKey) return;
    e.preventDefault();
    if ((currentInputMessage.trim() || pendingAttachments.length > 0 || pendingContexts.length > 0) && selectedAgentName && selectedNamespace && chatStatus === "ready") {
      handleSendMessage(e);
    }
  };

  if (sessionNotFound) {
    return (
      <div className="flex h-full w-full flex-col items-center justify-center p-4 text-center">
        <h2 className="mb-4 text-xl font-semibold">Session not found</h2>
        <p className="mb-6 text-muted-foreground">This chat session may have been deleted or does not exist.</p>
        <Button
          type="button"
          onClick={() => router.push(`/agents/${selectedNamespace}/${selectedAgentName}/chat`)}
        >
          Start a new chat
        </Button>
      </div>
    );
  }
  return (
    <div className="flex h-full w-full min-w-0 flex-col items-center justify-center transition-all duration-300 ease-in-out">
      <div className="flex-1 w-full overflow-hidden relative">
        <ScrollArea ref={containerRef} className="w-full h-full pt-2 pb-6">
          <div className="mx-auto w-full max-w-3xl flex flex-col space-y-4 px-4">
            {/* Never show loading for first message/new session */}
            {isLoading && sessionId && !isFirstMessage && !isCreatingSessionRef.current ? (
              <div
                className="flex h-full min-h-[50vh] items-center justify-center"
                role="status"
                aria-live="polite"
                aria-busy="true"
              >
                <div className="flex flex-col items-center gap-2">
                  <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" aria-hidden />
                  <p className="text-sm text-muted-foreground">Loading your chat session…</p>
                </div>
              </div>
            ) : storedMessages.length === 0 && streamingMessages.length === 0 && !isStreaming ? (
              <div className="flex items-center justify-center h-full min-h-[50vh]">
                <div className="max-w-lg text-center px-6">
                  <h2 className="mb-2 text-xl font-semibold">{selectedAgentName}</h2>
                  {agentDescription && (
                    <p className="mb-6 text-sm text-muted-foreground">{agentDescription}</p>
                  )}
                  <div className="flex flex-wrap items-center justify-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                    <span><kbd className="rounded border px-1">Enter</kbd> to send</span>
                    <span><kbd className="rounded border px-1">Shift+Enter</kbd> for a new line</span>
                    <span className="inline-flex items-center gap-1"><Paperclip className="h-3 w-3" aria-hidden /> attach files</span>
                  </div>
                </div>
              </div>
            ) : (
              <>
                {/* Display stored messages from session */}
                {storedMessages.map((message, index) => {
                  return <ChatMessage
                    key={`stored-${index}`}
                    message={message}
                    allMessages={allMessages}
                    agentContext={agentContext}
                    onApprove={handleApprove}
                    onApproveAlways={handleApproveAlways}
                    onReject={handleReject}
                    onAskUserSubmit={handleAskUserSubmit}
                    pendingDecisions={pendingDecisions}
                  />
                })}

                {/* Display streaming messages */}
                {streamingMessages.map((message, index) => {
                  return <ChatMessage
                    key={`stream-${index}`}
                    message={message}
                    allMessages={allMessages}
                    agentContext={agentContext}
                    onApprove={handleApprove}
                    onApproveAlways={handleApproveAlways}
                    onReject={handleReject}
                    onAskUserSubmit={handleAskUserSubmit}
                    pendingDecisions={pendingDecisions}
                  />
                })}

                {isStreaming && (
                  <StreamingMessage
                    content={streamingContent}
                  />
                )}
              </>
            )}
          </div>
        </ScrollArea>

        {showJumpToLatest && (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={scrollToBottom}
            className="absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full shadow-md border"
            aria-label="Jump to latest messages"
          >
            <ArrowDown className="h-4 w-4 mr-1" aria-hidden /> Latest
          </Button>
        )}
      </div>

      <ArtifactsPanel artifacts={sessionArtifacts} />

      <div className="w-full max-w-3xl mx-auto sticky bg-secondary bottom-0 md:bottom-2 rounded-none md:rounded-lg px-3 py-2 border overflow-hidden transition-all duration-300 ease-in-out">
        {(chatStatus !== "ready" || sessionStats.total > 0) && (
          <div className="flex items-center justify-between mb-1 min-h-5">
            {chatStatus !== "ready" ? <StatusDisplay chatStatus={chatStatus} /> : <span />}
            {sessionStats.total > 0 && <SessionTokenStatsDisplay stats={sessionStats} contextTokens={contextTokens} />}
          </div>
        )}

        {(pendingAttachments.length > 0 || pendingContexts.length > 0) && (
          <div className="flex flex-wrap gap-1.5 pb-2">
            {pendingContexts.map((context, index) => (
              <ChatContextChip
                key={`${context.provider}-${context.kind}-${context.scope ?? ""}-${context.namespace ?? ""}-${context.name}`}
                kind={context.kind}
                namespace={context.namespace}
                scope={context.scope}
                name={context.name}
                text={context.text}
                onRemove={() => removeContext(index)}
              />
            ))}
            {pendingAttachments.map((attachment, index) => (
              <AttachmentChip
                key={`${attachment.name}-${index}`}
                name={attachment.name}
                size={attachment.size}
                variant="solid"
                onRemove={() => removeAttachment(index)}
              />
            ))}
          </div>
        )}

        <form onSubmit={handleSendMessage} className="flex items-end gap-2">
          <div className="flex items-center pb-0.5">
            <ChatToolsPanel agentName={selectedAgentName} namespace={selectedNamespace} />
            <ChatModelSwitcher agentName={selectedAgentName} namespace={selectedNamespace} />
            <ChatContextPicker
              onPick={handlePickContext}
              openRef={openContextPickerRef}
              disabled={chatStatus !== "ready"}
            />
          </div>
          <Textarea
            ref={textareaRef}
            rows={1}
            value={currentInputMessage}
            onChange={(e) => setCurrentInputMessage(e.target.value)}
            placeholder={getStatusPlaceholder(chatStatus)}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
            className={`flex-1 min-h-[36px] max-h-[200px] overflow-y-auto border-0 shadow-none p-1 focus-visible:ring-0 resize-none ${chatStatus !== "ready" ? "opacity-50 cursor-not-allowed" : ""}`}
            disabled={chatStatus !== "ready"}
          />

          <div className="flex items-center gap-1.5 pb-0.5">
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={handleFileInputChange}
              aria-hidden
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => fileInputRef.current?.click()}
              disabled={chatStatus !== "ready"}
              aria-label="Attach files"
              title="Attach files (saved to the session workspace uploads/ folder)"
            >
              <Paperclip className="h-4 w-4" aria-hidden />
            </Button>
            {isVoiceSupported && (
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      type="button"
                      variant={isListening ? "destructive" : "ghost"}
                      size="icon"
                      onClick={isListening ? stopListening : startListening}
                      disabled={chatStatus !== "ready"}
                      className={isListening ? "animate-pulse" : ""}
                      aria-label={isListening ? "Stop listening" : "Voice input"}
                    >
                      {isListening ? (
                        <Square className="h-4 w-4" aria-hidden />
                      ) : (
                        <Mic className="h-4 w-4" aria-hidden />
                      )}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent side="top">
                    {voiceError
                      ? voiceError
                      : isListening
                        ? "Stop listening"
                        : "Voice input — click and speak"}
                  </TooltipContent>
                </Tooltip>
              </TooltipProvider>
            )}
            {chatStatus !== "ready" && chatStatus !== "error" ? (
              <Button
                type="button"
                variant="outline"
                size="icon"
                onClick={handleCancel}
                aria-label="Cancel"
              >
                <X className="h-4 w-4" aria-hidden />
              </Button>
            ) : (
              <Button
                type="submit"
                size="icon"
                disabled={(!currentInputMessage.trim() && pendingAttachments.length === 0 && pendingContexts.length === 0) || chatStatus !== "ready"}
                aria-label="Send message"
              >
                <ArrowBigUp className="h-4 w-4" aria-hidden />
              </Button>
            )}
          </div>
        </form>
      </div>
    </div>
  );
}
