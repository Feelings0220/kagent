"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Check, ChevronDown, Cpu, Loader2, Plus, Settings2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { getModelConfigs } from "@/app/actions/modelConfigs";
import { updateAgentModelConfig } from "@/app/actions/agents";
import { useChatAgentType, useChatModelInfo, useChatRunInSandbox } from "@/components/chat/ChatAgentContext";
import { k8sRefUtils } from "@/lib/k8sUtils";
import type { ModelConfig } from "@/types";

interface ChatModelSwitcherProps {
  agentName: string;
  namespace: string;
}

/** Human-readable endpoint for a ModelConfig, when one is configured. */
const getBaseUrl = (config: ModelConfig): string | undefined => {
  const spec = config.spec;
  return (
    spec.openAI?.baseUrl ||
    spec.anthropic?.baseUrl ||
    spec.gemini?.baseUrl ||
    spec.ollama?.host ||
    spec.azureOpenAI?.azureEndpoint ||
    undefined
  );
};

/**
 * Compact model selector rendered next to the chat composer. Lists the
 * ModelConfigs in the agent's namespace (each carries its own provider,
 * model, and base URL) and switches the agent to the selected one.
 */
export default function ChatModelSwitcher({ agentName, namespace }: ChatModelSwitcherProps) {
  const router = useRouter();
  const agentType = useChatAgentType();
  const runInSandbox = useChatRunInSandbox();
  const modelInfo = useChatModelInfo();

  const [open, setOpen] = useState(false);
  const [configs, setConfigs] = useState<ModelConfig[] | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [switchingRef, setSwitchingRef] = useState<string | null>(null);

  // Model switching updates spec.declarative.modelConfig; only regular
  // Declarative agents are supported (BYO agents embed their own model and
  // sandbox agents use a different update path).
  if (agentType !== "Declarative" || runInSandbox || !modelInfo) {
    return null;
  }

  const loadConfigs = async () => {
    setIsLoading(true);
    try {
      const response = await getModelConfigs();
      if (response.error || !response.data) {
        toast.error(response.message || "Failed to load model configs");
        setConfigs([]);
        return;
      }
      // A Declarative agent can only reference ModelConfigs in its own namespace.
      setConfigs(
        response.data.filter(
          (c) => k8sRefUtils.isValidRef(c.ref) && k8sRefUtils.fromRef(c.ref).namespace === namespace
        )
      );
    } catch (error) {
      console.error("Failed to load model configs:", error);
      toast.error("Failed to load model configs");
      setConfigs([]);
    } finally {
      setIsLoading(false);
    }
  };

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (nextOpen && configs === null) {
      loadConfigs();
    }
  };

  const handleSelect = async (config: ModelConfig) => {
    if (config.ref === modelInfo.modelConfigRef || switchingRef) return;
    setSwitchingRef(config.ref);
    try {
      const { name } = k8sRefUtils.fromRef(config.ref);
      const response = await updateAgentModelConfig(agentName, namespace, name);
      if (response.error) {
        toast.error(response.message || "Failed to switch model");
        return;
      }
      toast.success(`Model switched to ${config.spec.model}`);
      setOpen(false);
      // Reload server components so the provider picks up the new model info.
      router.refresh();
    } finally {
      setSwitchingRef(null);
    }
  };

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-9 max-w-48 gap-1 px-2 text-xs text-muted-foreground"
          aria-label="Switch model"
          title={`${modelInfo.modelProvider} · ${modelInfo.model}`}
        >
          <Cpu className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span className="truncate">{modelInfo.model}</span>
          <ChevronDown className="h-3 w-3 shrink-0" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" side="top" className="w-80 p-0">
        <div className="border-b px-3 py-2 text-xs font-medium text-muted-foreground">
          Model for this agent
        </div>
        <div className="max-h-64 overflow-y-auto p-1">
          {isLoading && (
            <div className="flex items-center gap-2 px-3 py-3 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> Loading model configs…
            </div>
          )}
          {!isLoading && configs !== null && configs.length === 0 && (
            <div className="px-3 py-3 text-sm text-muted-foreground">
              No model configs in namespace {namespace}.
            </div>
          )}
          {!isLoading &&
            configs?.map((config) => {
              const isCurrent = config.ref === modelInfo.modelConfigRef;
              const baseUrl = getBaseUrl(config);
              return (
                <button
                  key={config.ref}
                  type="button"
                  onClick={() => handleSelect(config)}
                  disabled={!!switchingRef}
                  className="flex w-full items-start gap-2 rounded-md px-3 py-2 text-left text-sm hover:bg-muted disabled:opacity-60"
                >
                  <span className="mt-0.5 w-4 shrink-0">
                    {switchingRef === config.ref ? (
                      <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
                    ) : isCurrent ? (
                      <Check className="h-4 w-4 text-green-600" aria-label="Current model" />
                    ) : null}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-medium">
                      {k8sRefUtils.fromRef(config.ref).name}
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {config.spec.provider} · {config.spec.model}
                      {baseUrl ? ` · ${baseUrl}` : ""}
                    </span>
                  </span>
                </button>
              );
            })}
        </div>
        <div className="flex items-center justify-between border-t px-2 py-1.5">
          <Button asChild variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs">
            <Link href="/models/new">
              <Plus className="h-3.5 w-3.5" aria-hidden /> New model
            </Link>
          </Button>
          <Button asChild variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs">
            <Link href="/models">
              <Settings2 className="h-3.5 w-3.5" aria-hidden /> Manage
            </Link>
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
