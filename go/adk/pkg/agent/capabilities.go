package agent

import (
	"fmt"
	"strings"

	"github.com/kagent-dev/kagent/go/adk/pkg/tools"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// GrantedCapabilitiesStateKey holds the session-granted capability names. The
// key has no app:/user: prefix, so a grant is scoped to exactly one session.
const GrantedCapabilitiesStateKey = "kagent_granted_capabilities"

// RequestCapabilityToolName is the builtin tool the model calls to proactively
// ask the user for a higher-privilege capability.
const RequestCapabilityToolName = "request_capability"

// capabilityRequestInstruction is appended to the system prompt when the
// request_capability tool is available, so the model knows to ask instead of
// giving up when it lacks a privilege.
const capabilityRequestInstruction = "\n\nSome actions need a higher-privilege capability (e.g. making changes to the cluster) that you don't have by default. When a task genuinely requires one, call request_capability with the capability name and a specific reason; the user decides whether to grant it for this session. Never assume you have write access — request it."

// hasToolNamed reports whether the tool list contains a tool with this name.
func hasToolNamed(list []adktool.Tool, name string) bool {
	for _, t := range list {
		if t.Name() == name {
			return true
		}
	}
	return false
}

// hasAnyToolNamed reports whether the tool list contains any of the names.
func hasAnyToolNamed(list []adktool.Tool, names []string) bool {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	for _, t := range list {
		if want[t.Name()] {
			return true
		}
	}
	return false
}

// Capability is a named group of builtin tools with a grant policy. Granting a
// capability unlocks every tool in the group for the session at once.
type Capability struct {
	Name           string
	Description    string
	Tools          []string
	DefaultGranted bool
}

// capabilityRegistry is the fixed set of builtin capabilities. Read groups are
// granted by default; destructive groups must be granted (by the user, via a
// model request or by directly approving a gated tool call).
var capabilityRegistry = []Capability{
	{
		Name:           "cluster-read",
		Description:    "Read-only Kubernetes queries (resources, pod logs, events).",
		Tools:          tools.K8sReadToolNames,
		DefaultGranted: true,
	},
	{
		Name:           "ci-read",
		Description:    "Read-only CI queries (Jenkins console logs, job status, builds).",
		Tools:          tools.JenkinsToolNames,
		DefaultGranted: true,
	},
	{
		Name:           "cluster-write",
		Description:    "Destructive Kubernetes changes: apply/create, delete, scale, rollout restart.",
		Tools:          tools.K8sWriteToolNames,
		DefaultGranted: false,
	},
}

// toolCapabilityIndex maps a tool name to its capability, built once.
var toolCapabilityIndex = func() map[string]Capability {
	idx := map[string]Capability{}
	for _, c := range capabilityRegistry {
		for _, t := range c.Tools {
			idx[t] = c
		}
	}
	return idx
}()

// grantableCapabilityForTool returns the capability gating a tool and whether
// that capability must be explicitly granted (i.e. is not default-granted).
func grantableCapabilityForTool(toolName string) (Capability, bool) {
	c, ok := toolCapabilityIndex[toolName]
	if !ok || c.DefaultGranted {
		return Capability{}, false
	}
	return c, true
}

func capabilityByName(name string) (Capability, bool) {
	for _, c := range capabilityRegistry {
		if c.Name == name {
			return c, true
		}
	}
	return Capability{}, false
}

// grantableCapabilityNames lists the capabilities the model may request.
func grantableCapabilityNames() []string {
	var names []string
	for _, c := range capabilityRegistry {
		if !c.DefaultGranted {
			names = append(names, c.Name)
		}
	}
	return names
}

// grantedCapabilities reads the session's granted capability names.
func grantedCapabilities(ctx adkagent.ToolContext) map[string]bool {
	granted := map[string]bool{}
	value, err := ctx.ReadonlyState().Get(GrantedCapabilitiesStateKey)
	if err != nil || value == nil {
		return granted
	}
	switch list := value.(type) {
	case []string:
		for _, name := range list {
			granted[name] = true
		}
	case []any: // JSON round-trip shape
		for _, item := range list {
			if name, ok := item.(string); ok {
				granted[name] = true
			}
		}
	}
	return granted
}

// grantCapability appends a capability to the session grant list.
func grantCapability(ctx adkagent.ToolContext, name string) error {
	granted := grantedCapabilities(ctx)
	if granted[name] {
		return nil
	}
	names := make([]string, 0, len(granted)+1)
	for n := range granted {
		names = append(names, n)
	}
	names = append(names, name)
	return ctx.State().Set(GrantedCapabilitiesStateKey, names)
}

// capabilityGrantHint is the human-readable prompt shown on the approval card.
func capabilityGrantHint(c Capability, reason string) string {
	hint := fmt.Sprintf("Grant the %q capability for this session? It enables: %s — %s",
		c.Name, strings.Join(c.Tools, ", "), c.Description)
	if strings.TrimSpace(reason) != "" {
		hint += " Reason: " + strings.TrimSpace(reason)
	}
	return hint
}

type requestCapabilityInput struct {
	Capability string `json:"capability"`
	Reason     string `json:"reason,omitempty"`
}

// NewRequestCapabilityTool builds the request_capability tool. Its execution is
// fully handled by MakeCapabilityCallback (the HITL grant), so the function
// body is only a safety fallback that runs when the callback isn't wired.
func NewRequestCapabilityTool() (adktool.Tool, error) {
	grantable := strings.Join(grantableCapabilityNames(), ", ")
	return functiontool.New(functiontool.Config{
		Name: RequestCapabilityToolName,
		Description: fmt.Sprintf(`Request permission to use a higher-privilege capability for this session.

Call this when a task needs a capability you don't have yet. The user is asked to approve; if they do, the whole capability (all of its tools) is granted for the rest of this session, so you won't be prompted again for those tools.
Grantable capabilities: %s.
- capability: the capability name to request (exactly as listed above)
- reason: a short, specific explanation of why you need it (shown to the user)`, grantable),
	}, func(_ adkagent.ToolContext, _ requestCapabilityInput) (string, error) {
		return "Capability requests are not enabled for this agent.", nil
	})
}

// MakeCapabilityCallback gates builtin tools behind session-scoped capability
// grants and handles the request_capability tool. Approving once grants the
// whole capability group for the session, so the model isn't re-prompted for
// the group's other tools.
func MakeCapabilityCallback() llmagent.BeforeToolCallback {
	return func(ctx adkagent.ToolContext, t adktool.Tool, args map[string]any) (map[string]any, error) {
		toolName := t.Name()

		// The explicit "please grant me X" request from the model.
		if toolName == RequestCapabilityToolName {
			return handleRequestCapability(ctx, args)
		}

		// Gate tools that belong to a grantable (non-default) capability.
		capability, gated := grantableCapabilityForTool(toolName)
		if !gated {
			return nil, nil
		}
		if grantedCapabilities(ctx)[capability.Name] {
			return nil, nil // already granted this session
		}

		// On re-invocation after the grant prompt, ADK populates ToolConfirmation.
		if confirmation := ctx.ToolConfirmation(); confirmation != nil {
			if confirmation.Confirmed {
				// Approving a gated tool grants its whole capability group.
				if err := grantCapability(ctx, capability.Name); err != nil {
					return nil, fmt.Errorf("failed to record capability grant: %w", err)
				}
				return nil, nil // proceed with the tool
			}
			payload, _ := confirmation.Payload.(map[string]any)
			if reason, _ := payload["rejection_reason"].(string); reason != "" {
				return map[string]any{"result": fmt.Sprintf("Tool call was rejected by user. Reason: %s", reason)}, nil
			}
			return map[string]any{"result": "Tool call was rejected by user."}, nil
		}

		// First invocation — ask the user to grant the capability.
		if err := ctx.RequestConfirmation(capabilityGrantHint(capability, ""), map[string]any{"capability": capability.Name}); err != nil {
			return nil, fmt.Errorf("failed to request capability %s: %w", capability.Name, err)
		}
		return map[string]any{"status": "confirmation_requested", "capability": capability.Name}, nil
	}
}

func handleRequestCapability(ctx adkagent.ToolContext, args map[string]any) (map[string]any, error) {
	requested, _ := args["capability"].(string)
	requested = strings.TrimSpace(requested)
	grantable := strings.Join(grantableCapabilityNames(), ", ")
	if requested == "" {
		return map[string]any{"result": "No capability specified. Grantable capabilities: " + grantable}, nil
	}
	capability, ok := capabilityByName(requested)
	if !ok || capability.DefaultGranted {
		return map[string]any{"result": fmt.Sprintf("Unknown or already-available capability %q. Grantable: %s", requested, grantable)}, nil
	}

	if confirmation := ctx.ToolConfirmation(); confirmation != nil {
		if confirmation.Confirmed {
			if err := grantCapability(ctx, requested); err != nil {
				return nil, fmt.Errorf("failed to record capability grant: %w", err)
			}
			return map[string]any{"result": fmt.Sprintf("Capability %q granted for this session.", requested)}, nil
		}
		return map[string]any{"result": fmt.Sprintf("The user denied the %q capability request.", requested)}, nil
	}

	reason, _ := args["reason"].(string)
	if err := ctx.RequestConfirmation(capabilityGrantHint(capability, reason), map[string]any{"capability": requested}); err != nil {
		return nil, fmt.Errorf("failed to request capability %s: %w", requested, err)
	}
	return map[string]any{"status": "confirmation_requested", "capability": requested}, nil
}
