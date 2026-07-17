"use client";

import { useEffect, useRef, useState } from "react";
import { AtSign, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { listClusterResources, type ClusterResourceItem } from "@/app/actions/cluster";
import { listNamespaces } from "@/app/actions/namespaces";

/** Mentionable resource kinds; must stay in sync with the backend allowlist. */
const RESOURCE_KINDS = [
  "pod",
  "deployment",
  "statefulset",
  "daemonset",
  "replicaset",
  "job",
  "cronjob",
  "service",
  "configmap",
  "node",
  "namespace",
] as const;

/** Kinds without a namespace scope. */
const CLUSTER_SCOPED = new Set(["node", "namespace"]);

interface ChatContextPickerProps {
  /** Called with the picked resource; the parent fetches and stores its context. */
  onPick: (item: ClusterResourceItem) => void;
  /** Ref-based open trigger so typing "@" in the composer can open the picker. */
  openRef?: React.MutableRefObject<(() => void) | null>;
  disabled?: boolean;
}

/**
 * "@" cluster-resource picker: choose a kind, optionally a namespace, search
 * by name, and pick a resource to inject as chat context.
 */
export default function ChatContextPicker({ onPick, openRef, disabled }: ChatContextPickerProps) {
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<string>("pod");
  const [namespace, setNamespace] = useState<string>("");
  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<ClusterResourceItem[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const searchSeq = useRef(0);

  useEffect(() => {
    if (openRef) {
      openRef.current = () => setOpen(true);
      return () => {
        openRef.current = null;
      };
    }
  }, [openRef]);

  // Load namespaces once when first opened.
  useEffect(() => {
    if (!open || namespaces.length > 0) return;
    listNamespaces().then(response => {
      if (response.data) {
        setNamespaces(response.data.map(ns => ns.name));
      }
    });
  }, [open, namespaces.length]);

  // Fetch matching resources whenever the filters change while open.
  useEffect(() => {
    if (!open) return;
    const seq = ++searchSeq.current;
    const handle = setTimeout(async () => {
      setIsLoading(true);
      const response = await listClusterResources(kind, {
        namespace: CLUSTER_SCOPED.has(kind) ? undefined : namespace || undefined,
        query: query || undefined,
      });
      if (seq !== searchSeq.current) return; // stale response
      if (response.error) {
        toast.error(response.message || "Failed to list resources");
        setItems([]);
      } else {
        setItems(response.data ?? []);
      }
      setIsLoading(false);
    }, 200);
    return () => clearTimeout(handle);
  }, [open, kind, namespace, query]);

  const handlePick = (item: ClusterResourceItem) => {
    setOpen(false);
    setQuery("");
    onPick(item);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={disabled}
          className="h-9 gap-1 px-2 text-xs text-muted-foreground"
          aria-label="Add cluster context"
          title="Add cluster resource as context (@)"
        >
          <AtSign className="h-3.5 w-3.5" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" side="top" className="w-96 p-0">
        <div className="border-b px-3 py-2 text-xs font-medium text-muted-foreground">
          Add Kubernetes context
        </div>

        <div className="flex gap-2 p-2">
          <select
            className="w-32 rounded-md border bg-transparent px-2 py-1 text-sm"
            value={kind}
            onChange={e => setKind(e.target.value)}
            aria-label="Resource kind"
          >
            {RESOURCE_KINDS.map(k => (
              <option key={k} value={k}>{k}</option>
            ))}
          </select>
          {!CLUSTER_SCOPED.has(kind) && (
            <select
              className="flex-1 rounded-md border bg-transparent px-2 py-1 text-sm"
              value={namespace}
              onChange={e => setNamespace(e.target.value)}
              aria-label="Namespace"
            >
              <option value="">All namespaces</option>
              {namespaces.map(ns => (
                <option key={ns} value={ns}>{ns}</option>
              ))}
            </select>
          )}
        </div>

        <div className="px-2 pb-2">
          <Input
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder="Search by name..."
            className="h-8 text-sm"
            aria-label="Search resources"
            autoFocus
          />
        </div>

        <div className="max-h-64 overflow-y-auto border-t p-1">
          {isLoading && (
            <div className="flex items-center gap-2 px-3 py-3 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden /> Loading…
            </div>
          )}
          {!isLoading && items.length === 0 && (
            <div className="px-3 py-3 text-sm text-muted-foreground">No matching resources.</div>
          )}
          {!isLoading &&
            items.map(item => (
              <button
                key={`${item.namespace ?? ""}/${item.name}`}
                type="button"
                onClick={() => handlePick(item)}
                className="flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-left text-sm hover:bg-muted"
              >
                <span className="min-w-0 flex-1 truncate">
                  {item.namespace ? `${item.namespace}/` : ""}
                  <span className="font-medium">{item.name}</span>
                </span>
                {item.status && <span className="shrink-0 text-xs text-muted-foreground">{item.status}</span>}
              </button>
            ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}
