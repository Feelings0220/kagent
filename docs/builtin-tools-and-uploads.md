# Built-in Workspace Tools & Session File Uploads

This document covers two related capabilities:

1. **Built-in workspace tools** — give a Declarative agent command execution
   (`bash`) and file tools without configuring skills or an MCP server.
2. **Session file uploads** — attach files in the chat UI; they are saved into
   the agent's per-session workspace where those tools can read them.

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

[skills]: ./architecture/crds-and-types.md
