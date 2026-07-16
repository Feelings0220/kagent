import { DataPart, FilePart, Message, Task, TextPart } from "@a2a-js/sdk";
import { extractFileArtifactsFromTasks, extractMessagesFromTasks, getMetadataValue } from "@/lib/messageHandlers";

/** Options for sessionToMarkdown. */
export interface SessionExportOptions {
  sessionName?: string;
  agentRef?: string;
}

const textOf = (message: Message): string =>
  (message.parts ?? [])
    .filter(part => part.kind === "text")
    .map(part => (part as TextPart).text)
    .join("")
    .trim();

const attachmentNames = (message: Message): string[] =>
  (message.parts ?? [])
    .filter(part => part.kind === "file")
    .map(part => (part as FilePart).file?.name || "attachment");

/** Names of tools invoked in a message's function_call data parts. */
const toolCallNames = (message: Message): string[] => {
  const names: string[] = [];
  for (const part of message.parts ?? []) {
    if (part.kind !== "data") continue;
    const dataPart = part as DataPart;
    const partType = getMetadataValue<string>(dataPart.metadata as Record<string, unknown>, "type");
    if (partType === "function_call") {
      const name = (dataPart.data as { name?: string } | undefined)?.name;
      if (name) names.push(name);
    }
  }
  // Streaming-shaped messages carry tool calls in message metadata instead
  // (stored unprefixed by createMessage's additionalMetadata).
  const metadataCalls = (message.metadata as { toolCallData?: Array<{ name?: string }> } | undefined)
    ?.toolCallData;
  for (const call of metadataCalls ?? []) {
    if (call?.name) names.push(call.name);
  }
  return names;
};

/**
 * Render a session's stored tasks as a readable Markdown transcript:
 * role-labelled sections, tool-call bullets, attachment names, and a final
 * artifacts section.
 */
export function sessionToMarkdown(tasks: Task[], options: SessionExportOptions = {}): string {
  const lines: string[] = [];
  lines.push(`# ${options.sessionName || "Conversation export"}`);
  const meta: string[] = [];
  if (options.agentRef) meta.push(`Agent: ${options.agentRef}`);
  meta.push(`Exported: ${new Date().toISOString()}`);
  lines.push("");
  lines.push(`> ${meta.join(" · ")}`);

  for (const message of extractMessagesFromTasks(tasks)) {
    const role = message.role === "user" ? "User" : "Agent";
    const text = textOf(message);
    const tools = toolCallNames(message);
    const attachments = attachmentNames(message);
    if (!text && tools.length === 0 && attachments.length === 0) continue;

    lines.push("");
    lines.push(`## ${role}`);
    if (attachments.length > 0) {
      lines.push("");
      lines.push(attachments.map(name => `📎 ${name}`).join("  "));
    }
    if (tools.length > 0) {
      lines.push("");
      for (const tool of tools) {
        lines.push(`- 🔧 called \`${tool}\``);
      }
    }
    if (text) {
      lines.push("");
      lines.push(text);
    }
  }

  const artifacts = extractFileArtifactsFromTasks(tasks);
  if (artifacts.length > 0) {
    lines.push("");
    lines.push("## Artifacts");
    lines.push("");
    for (const artifact of artifacts) {
      lines.push(`- \`${artifact.path}\` (${artifact.mimeType}, ${artifact.size} bytes)`);
    }
  }

  lines.push("");
  return lines.join("\n");
}
