import { Task } from "@a2a-js/sdk";
import { extractFileArtifactsFromTasks, isFileArtifact, toFileArtifact } from "@/lib/messageHandlers";

const fileArtifact = (name: string, overrides: Record<string, unknown> = {}) => ({
  artifactId: `art-${name}`,
  name,
  metadata: {
    kagent_type: "file_artifact",
    kagent_path: `outputs/${name}`,
    kagent_size: 42,
    kagent_mime_type: "text/markdown",
    ...overrides,
  },
  parts: [
    {
      kind: "file" as const,
      file: { bytes: "IyBoaQ==", mimeType: "text/markdown", name },
    },
  ],
});

const textArtifact = () => ({
  artifactId: "art-text",
  parts: [{ kind: "text" as const, text: "final answer" }],
});

const makeTask = (id: string, artifacts: unknown[]): Task =>
  ({
    id,
    kind: "task",
    contextId: "ctx",
    status: { state: "completed" },
    artifacts,
  }) as unknown as Task;

describe("file artifact extraction", () => {
  it("identifies kagent file artifacts and ignores text artifacts", () => {
    expect(isFileArtifact(fileArtifact("a.md") as never)).toBe(true);
    expect(isFileArtifact(textArtifact() as never)).toBe(false);
    expect(toFileArtifact(textArtifact() as never)).toBeNull();
  });

  it("converts artifact fields", () => {
    const converted = toFileArtifact(fileArtifact("report.md") as never, "task-1");
    expect(converted).toEqual({
      name: "report.md",
      path: "outputs/report.md",
      mimeType: "text/markdown",
      size: 42,
      bytes: "IyBoaQ==",
      note: undefined,
      taskId: "task-1",
    });
  });

  it("collects across tasks and dedups by name keeping the latest", () => {
    const tasks = [
      makeTask("t1", [fileArtifact("report.md", { kagent_size: 1 }), textArtifact()]),
      makeTask("t2", [fileArtifact("report.md", { kagent_size: 2 }), fileArtifact("data.csv")]),
    ];

    const artifacts = extractFileArtifactsFromTasks(tasks);
    expect(artifacts).toHaveLength(2);
    const report = artifacts.find(a => a.name === "report.md");
    expect(report?.size).toBe(2);
    expect(report?.taskId).toBe("t2");
  });

  it("handles oversized placeholder artifacts without bytes", () => {
    const oversized = {
      artifactId: "art-big",
      name: "huge.bin",
      metadata: {
        kagent_type: "file_artifact",
        kagent_path: "outputs/huge.bin",
        kagent_size: 99999999,
        kagent_mime_type: "application/octet-stream",
      },
      parts: [{ kind: "text" as const, text: "File huge.bin exceeds the limit" }],
    };

    const converted = toFileArtifact(oversized as never);
    expect(converted?.bytes).toBeUndefined();
    expect(converted?.note).toContain("exceeds");
  });
});
