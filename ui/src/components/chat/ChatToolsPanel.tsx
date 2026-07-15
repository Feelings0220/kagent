"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ChevronDown, Loader2, Plus, Settings2, ShieldAlert, ShieldCheck, Wrench, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { getAgent, updateAgentTools } from "@/app/actions/agents";
import { getServers } from "@/app/actions/servers";
import { useChatAgentType, useChatRunInSandbox } from "@/components/chat/ChatAgentContext";
import { isAgentTool, isMcpTool, groupMcpToolsByServer, parseGroupKind } from "@/lib/toolUtils";
import { k8sRefUtils } from "@/lib/k8sUtils";
import type { Tool, ToolServerResponse } from "@/types";

interface ChatToolsPanelProps {
  agentName: string;
  namespace: string;
}

/** Server ref ("ns/name") for an MCP tool entry, defaulting to the agent namespace. */
const mcpServerRef = (tool: Tool, agentNamespace: string): string => {
  const mcp = tool.mcpServer;
  if (!mcp) return "";
  if (k8sRefUtils.isValidRef(mcp.name)) return mcp.name;
  return k8sRefUtils.toRef(mcp.namespace || agentNamespace, mcp.name);
};

/**
 * In-chat tools & permissions panel. Lists the agent's tools with a per-tool
 * approval toggle (spec requireApproval), supports removing tools and adding
 * tools from discovered MCP servers. Changes are saved to the Agent resource,
 * so they apply to every session of this agent.
 */
export default function ChatToolsPanel({ agentName, namespace }: ChatToolsPanelProps) {
  const router = useRouter();
  const agentType = useChatAgentType();
  const runInSandbox = useChatRunInSandbox();

  const [open, setOpen] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [originalTools, setOriginalTools] = useState<Tool[]>([]);
  const [draftTools, setDraftTools] = useState<Tool[]>([]);
  const [servers, setServers] = useState<ToolServerResponse[]>([]);
  const [addFromServer, setAddFromServer] = useState<string | null>(null);

  const isDirty = useMemo(
    () => JSON.stringify(draftTools) !== JSON.stringify(originalTools),
    [draftTools, originalTools]
  );

  if (agentType !== "Declarative" || runInSandbox) {
    return null;
  }

  const loadData = async () => {
    setIsLoading(true);
    try {
      const [agentRes, serversRes] = await Promise.all([
        getAgent(agentName, namespace),
        getServers(),
      ]);
      if (agentRes.error || !agentRes.data?.agent) {
        toast.error(agentRes.message || "Failed to load agent tools");
        return;
      }
      const tools = agentRes.data.agent.spec.declarative?.tools ?? [];
      // Group per server so toggling/removal has one entry per server.
      const { groupedTools } = groupMcpToolsByServer(tools);
      setOriginalTools(groupedTools);
      setDraftTools(groupedTools);
      setServers(serversRes.data ?? []);
      setAddFromServer(null);
    } finally {
      setIsLoading(false);
    }
  };

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (nextOpen) {
      loadData();
    }
  };

  /** Toggle whether a tool name requires human approval before execution. */
  const toggleApproval = (serverRef: string, toolName: string) => {
    setDraftTools(prev =>
      prev.map(tool => {
        if (!isMcpTool(tool) || mcpServerRef(tool, namespace) !== serverRef || !tool.mcpServer) return tool;
        const current = tool.mcpServer.requireApproval ?? [];
        const requireApproval = current.includes(toolName)
          ? current.filter(n => n !== toolName)
          : [...current, toolName];
        return {
          ...tool,
          mcpServer: {
            ...tool.mcpServer,
            ...(requireApproval.length > 0 ? { requireApproval } : { requireApproval: undefined }),
          },
        };
      })
    );
  };

  const removeToolName = (serverRef: string, toolName: string) => {
    setDraftTools(prev =>
      prev.flatMap(tool => {
        if (!isMcpTool(tool) || mcpServerRef(tool, namespace) !== serverRef || !tool.mcpServer) return [tool];
        const toolNames = tool.mcpServer.toolNames.filter(n => n !== toolName);
        if (toolNames.length === 0) return [];
        const requireApproval = (tool.mcpServer.requireApproval ?? []).filter(n => toolNames.includes(n));
        return [{
          ...tool,
          mcpServer: {
            ...tool.mcpServer,
            toolNames,
            ...(requireApproval.length > 0 ? { requireApproval } : { requireApproval: undefined }),
          },
        }];
      })
    );
  };

  const removeAgentTool = (agentRefName: string) => {
    setDraftTools(prev => prev.filter(tool => !(isAgentTool(tool) && tool.agent?.name === agentRefName)));
  };

  const addToolName = (server: ToolServerResponse, toolName: string) => {
    setDraftTools(prev => {
      const existing = prev.find(tool => isMcpTool(tool) && mcpServerRef(tool, namespace) === server.ref);
      if (existing && existing.mcpServer) {
        if (existing.mcpServer.toolNames.includes(toolName)) return prev;
        return prev.map(tool =>
          tool === existing && tool.mcpServer
            ? { ...tool, mcpServer: { ...tool.mcpServer, toolNames: [...tool.mcpServer.toolNames, toolName] } }
            : tool
        );
      }
      const { apiGroup, kind } = parseGroupKind(server.groupKind);
      const parsed = k8sRefUtils.isValidRef(server.ref)
        ? k8sRefUtils.fromRef(server.ref)
        : { namespace, name: server.ref };
      const newTool: Tool = {
        type: "McpServer",
        mcpServer: {
          name: parsed.name,
          namespace: parsed.namespace,
          apiGroup,
          kind,
          toolNames: [toolName],
        },
      };
      return [...prev, newTool];
    });
  };

  const handleSave = async () => {
    setIsSaving(true);
    try {
      const response = await updateAgentTools(agentName, namespace, draftTools);
      if (response.error) {
        toast.error(response.message || "Failed to update tools");
        return;
      }
      toast.success("Agent tools updated");
      setOpen(false);
      router.refresh();
    } finally {
      setIsSaving(false);
    }
  };

  const mcpEntries = draftTools.filter(isMcpTool);
  const agentEntries = draftTools.filter(isAgentTool);
  const selectedServer = servers.find(s => s.ref === addFromServer);
  const alreadyAdded = new Set(
    mcpEntries.flatMap(t =>
      (t.mcpServer?.toolNames ?? []).map(n => `${mcpServerRef(t as Tool, namespace)}::${n}`)
    )
  );

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-9 gap-1 px-2 text-xs text-muted-foreground"
          aria-label="Tools and permissions"
          title="Tools & permissions"
        >
          <Wrench className="h-3.5 w-3.5" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" side="top" className="w-96 p-0">
        <div className="border-b px-3 py-2 text-xs font-medium text-muted-foreground">
          Tools & permissions
        </div>

        <div className="max-h-80 overflow-y-auto p-2 space-y-3">
          {isLoading && (
            <div className="flex items-center gap-2 px-2 py-3 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> Loading tools…
            </div>
          )}

          {!isLoading && mcpEntries.length === 0 && agentEntries.length === 0 && (
            <div className="px-2 py-3 text-sm text-muted-foreground">
              This agent has no tools yet. Add some below.
            </div>
          )}

          {!isLoading && mcpEntries.map(tool => {
            const serverRef = mcpServerRef(tool as Tool, namespace);
            const approvals = new Set(tool.mcpServer?.requireApproval ?? []);
            return (
              <div key={serverRef}>
                <div className="px-2 pb-1 text-xs font-semibold">{serverRef}</div>
                <TooltipProvider>
                  {(tool.mcpServer?.toolNames ?? []).map(toolName => {
                    const requiresApproval = approvals.has(toolName);
                    return (
                      <div key={toolName} className="group flex items-center gap-2 rounded-md px-2 py-1 hover:bg-muted">
                        <span className="flex-1 truncate text-sm">{toolName}</span>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              className="h-6 px-1.5 gap-1 text-xs"
                              onClick={() => toggleApproval(serverRef, toolName)}
                              aria-label={requiresApproval ? "Requires approval — click to auto-run" : "Auto-run — click to require approval"}
                            >
                              {requiresApproval ? (
                                <>
                                  <ShieldAlert className="h-3.5 w-3.5 text-amber-500" aria-hidden /> Ask
                                </>
                              ) : (
                                <>
                                  <ShieldCheck className="h-3.5 w-3.5 text-green-600" aria-hidden /> Auto
                                </>
                              )}
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent side="top">
                            {requiresApproval
                              ? "Human approval required before each call"
                              : "Runs without approval"}
                          </TooltipContent>
                        </Tooltip>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          className="h-6 w-6 p-0 opacity-0 group-hover:opacity-100"
                          onClick={() => removeToolName(serverRef, toolName)}
                          aria-label={`Remove ${toolName}`}
                        >
                          <X className="h-3.5 w-3.5" aria-hidden />
                        </Button>
                      </div>
                    );
                  })}
                </TooltipProvider>
              </div>
            );
          })}

          {!isLoading && agentEntries.length > 0 && (
            <div>
              <div className="px-2 pb-1 text-xs font-semibold">Agent tools</div>
              {agentEntries.map(tool => (
                <div key={tool.agent!.name} className="group flex items-center gap-2 rounded-md px-2 py-1 hover:bg-muted">
                  <span className="flex-1 truncate text-sm">
                    {k8sRefUtils.toRef(tool.agent!.namespace || namespace, tool.agent!.name)}
                  </span>
                  <span className="text-xs text-muted-foreground">agent</span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-6 w-6 p-0 opacity-0 group-hover:opacity-100"
                    onClick={() => removeAgentTool(tool.agent!.name)}
                    aria-label={`Remove agent tool ${tool.agent!.name}`}
                  >
                    <X className="h-3.5 w-3.5" aria-hidden />
                  </Button>
                </div>
              ))}
            </div>
          )}

          {!isLoading && servers.length > 0 && (
            <div className="border-t pt-2">
              <button
                type="button"
                className="flex w-full items-center gap-1 px-2 py-1 text-xs font-medium text-muted-foreground hover:text-foreground"
                onClick={() => setAddFromServer(addFromServer === null ? (servers[0]?.ref ?? null) : null)}
                aria-expanded={addFromServer !== null}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden /> Add tools
                <ChevronDown className={`h-3 w-3 transition-transform ${addFromServer !== null ? "rotate-180" : ""}`} aria-hidden />
              </button>

              {addFromServer !== null && (
                <div className="mt-1 space-y-1 px-2">
                  <select
                    className="w-full rounded-md border bg-transparent px-2 py-1 text-sm"
                    value={addFromServer}
                    onChange={e => setAddFromServer(e.target.value)}
                    aria-label="Tool server"
                  >
                    {servers.map(server => (
                      <option key={server.ref} value={server.ref}>
                        {server.ref}
                      </option>
                    ))}
                  </select>
                  <div className="max-h-40 overflow-y-auto">
                    {selectedServer?.discoveredTools?.length ? (
                      selectedServer.discoveredTools.map(discovered => {
                        const added = alreadyAdded.has(`${selectedServer.ref}::${discovered.name}`);
                        return (
                          <button
                            key={discovered.name}
                            type="button"
                            disabled={added}
                            onClick={() => addToolName(selectedServer, discovered.name)}
                            className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm hover:bg-muted disabled:opacity-40"
                            title={discovered.description}
                          >
                            <Plus className="h-3.5 w-3.5 shrink-0" aria-hidden />
                            <span className="truncate">{discovered.name}</span>
                            {added && <span className="ml-auto text-xs text-muted-foreground">added</span>}
                          </button>
                        );
                      })
                    ) : (
                      <div className="px-2 py-1 text-xs text-muted-foreground">No tools discovered on this server.</div>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        <div className="flex items-center justify-between gap-2 border-t px-3 py-2">
          <div className="flex items-center gap-1">
            <span className="text-[11px] text-muted-foreground">Applies to all sessions of this agent</span>
            <Button asChild variant="ghost" size="sm" className="h-6 w-6 p-0" aria-label="Manage MCP servers">
              <Link href="/mcp">
                <Settings2 className="h-3.5 w-3.5" aria-hidden />
              </Link>
            </Button>
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={() => setDraftTools(originalTools)}
              disabled={!isDirty || isSaving}
            >
              Reset
            </Button>
            <Button
              type="button"
              size="sm"
              className="h-7 px-3 text-xs"
              onClick={handleSave}
              disabled={!isDirty || isSaving || isLoading}
            >
              {isSaving ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden /> : "Save"}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
