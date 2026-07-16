import { Task } from "@a2a-js/sdk";
import { sessionToMarkdown } from "@/lib/sessionExport";

const makeTask = (id: string, history: unknown[], artifacts: unknown[] = []): Task =>
  ({
    id,
    kind: "task",
    contextId: "ctx",
    status: { state: "completed" },
    history,
    artifacts,
  }) as unknown as Task;

const userMessage = (text: string) => ({
  kind: "message",
  messageId: `m-${text}`,
  role: "user",
  parts: [{ kind: "text", text }],
});

const agentMessage = (text: string) => ({
  kind: "message",
  messageId: `m-${text}`,
  role: "agent",
  parts: [{ kind: "text", text }],
});

describe("sessionToMarkdown", () => {
  it("renders roles, text, and header metadata", () => {
    const tasks = [makeTask("t1", [userMessage("hello k8s"), agentMessage("hi there")])];

    const markdown = sessionToMarkdown(tasks, { sessionName: "Debug pods", agentRef: "kagent/ops" });

    expect(markdown).toContain("# Debug pods");
    expect(markdown).toContain("Agent: kagent/ops");
    expect(markdown).toContain("## User");
    expect(markdown).toContain("hello k8s");
    expect(markdown).toContain("## Agent");
    expect(markdown).toContain("hi there");
  });

  it("lists artifacts when present", () => {
    const artifact = {
      artifactId: "a1",
      name: "report.md",
      metadata: {
        kagent_type: "file_artifact",
        kagent_path: "outputs/report.md",
        kagent_size: 12,
        kagent_mime_type: "text/markdown",
      },
      parts: [{ kind: "file", file: { bytes: "IyBoaQ==", mimeType: "text/markdown", name: "report.md" } }],
    };
    const tasks = [makeTask("t1", [userMessage("write a report")], [artifact])];

    const markdown = sessionToMarkdown(tasks);

    expect(markdown).toContain("## Artifacts");
    expect(markdown).toContain("`outputs/report.md` (text/markdown, 12 bytes)");
  });

  it("falls back to the default title", () => {
    const markdown = sessionToMarkdown([]);
    expect(markdown).toContain("# Conversation export");
  });
});
