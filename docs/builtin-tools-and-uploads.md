# Built-in Workspace Tools, Session File Uploads & Artifacts

This document covers three related capabilities:

1. **Built-in workspace tools** — give a Declarative agent command execution
   (`bash`) and file tools without configuring skills or an MCP server.
2. **Session file uploads** — attach files in the chat UI; they are saved into
   the agent's per-session workspace where those tools can read them.
3. **Session artifacts** — files the agent writes to `outputs/` surface in the
   chat UI with preview and download.

## Built-in workspace tools

### What they are

| Tool | Purpose |
|------|---------|
| `bash` | Execute shell commands inside the runtime sandbox (srt) |
| `read_file` | Read files from the session workspace with line numbers |
| `write_file` | Create/overwrite files in the session workspace |
| `edit_file` | Precise string-replacement edits |

These are the same tool implementations used by [skills], but they no longer
require a skills image or git repo to be mounted.

### Enabling via the Agent CRD

```yaml
apiVersion: kagent.dev/v1alpha2
kind: Agent
metadata:
  name: ops-agent
spec:
  type: Declarative
  declarative:
    modelConfig: default-model-config
    systemMessage: |
      You are a Kubernetes operations assistant.
    tools:
      - type: Builtin
        builtin:
          names: [bash, read_file, write_file, edit_file]
```

Notes:

- `names` accepts any subset of `bash`, `read_file`, `write_file`, `edit_file`.
- Duplicate names across multiple `Builtin` entries are deduplicated.
- When the agent **also** has skills configured, the skills-provided tools take
  precedence and builtin entries do not create duplicates.
- Including `bash` enables the same pod isolation settings as skills (the srt
  sandbox needs either `privileged` or unprivileged user namespaces; see the
  skills security notes). File-only selections do not change pod security.
- Supported by both the Go (default) and Python declarative runtimes.

### Enabling from the chat UI

Open the **wrench icon** next to the chat composer → *Add tools* → pick from
the *Built-in* row. Saving updates the Agent resource (applies to all sessions
of that agent).

### Where the tools operate

Each session gets a workspace directory:

```
<workspace>/<session-id>/
├── uploads/   # files uploaded by the user
├── outputs/   # files produced by the agent
└── skills/    # read-only symlink (only when skills are configured)
```

Without skills, the workspace root defaults to `/tmp/kagent-workspace`
(created on demand).

## Session file uploads

### From the chat UI

Use the **paperclip button** (or paste files from the clipboard) to attach up
to 5 files (10 MB each) to a message. Attachments are sent as A2A `FilePart`s.

### What the runtime does

When the agent has a workspace (skills or builtin tools configured):

- Every uploaded file is written to the session's `uploads/` directory.
  File names are sanitized and collisions get `-1`, `-2`, … suffixes.
- Non-image files are replaced in the model context with a note such as
  `[File uploaded: data.csv (text/csv, 1234 bytes). It is saved in the session
  workspace at uploads/data.csv — use the file or bash tools to read it.]`
- Images are saved **and** kept inline, so vision-capable models still see
  them.

Without a workspace, file parts are passed through unchanged (inline), which
works for images on vision models but may be rejected by providers for other
content types.

### Typical flow

1. Attach `data.csv` and send: "Summarize this file".
2. The runtime saves it to `uploads/data.csv` and tells the model where it is.
3. The agent runs e.g. `read_file uploads/data.csv` or
   `bash("head -5 uploads/data.csv")` and answers.

## Session artifacts

Files the agent writes to the session `outputs/` directory (via `write_file`
or `bash`) are emitted after each turn as A2A task artifacts (Go runtime):

- Each new or modified file becomes an artifact named after its
  `outputs/`-relative path, with the content inlined as base64 (up to 2 MB per
  file, at most 10 files per turn; larger files get a placeholder note).
- Artifacts persist with the task, so they survive reloads and appear when an
  old session is reopened.
- The chat UI shows an **Artifacts** bar above the composer listing the files,
  with preview (markdown, HTML in a sandboxed iframe, SVG, images) and
  download. Re-generated files replace their earlier version in the panel.

Typical flow:

1. Ask: "Analyze uploads/data.csv and write a report to outputs/report.md".
2. The agent writes the file with its workspace tools; when the turn
   completes, `report.md` appears in the Artifacts bar.
3. Click the eye icon to preview the rendered markdown, or download it.

## Cluster context injection (@-mention)

Reference live cluster resources directly in a chat message: click the **@**
button next to the composer (or type `@` at the start of a word), pick a
resource kind (pod, deployment, service, configmap, node, …), optionally a
namespace, and search by name.

- The picked resource appears as a context chip on the draft; on send, its
  sanitized YAML (managedFields and last-applied stripped, ConfigMap values
  truncated) plus its recent events are injected ahead of your question.
- Works with every agent — Declarative (Go/Python) and BYO — because the
  context travels as a plain text part; no tools are required on the agent.
- In the transcript the injection renders as a chip; click it to see exactly
  what the model received.
- Secrets are deliberately not mentionable. The backend only exposes kinds
  covered by the controller's read-only RBAC (core, apps, batch).

When the **Jenkins provider** is configured (helm `contextProviders.jenkins`:
url + a Secret with `username`/`token` keys), the @ picker gains a *jenkins*
tab: mention a `job` (status, health, recent builds) or a `build` (result,
cause, and the console log tail with credential-looking values masked) —
e.g. "@build my-app #123 why did this fail?".

Typical flow: `@pod` → pick `prod/nginx-7d4b...` (status `CrashLoopBackOff`)
→ ask "why is this pod crash-looping?" — the agent answers from the injected
spec, status, and events without needing a tool round-trip.

[skills]: ./architecture/crds-and-types.md
