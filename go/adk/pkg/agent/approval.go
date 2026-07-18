package agent

import (
	"fmt"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// stripConfirmationPartsCallback is a BeforeModelCallback that removes
// adk_request_confirmation FunctionCall and FunctionResponse parts from the
// LLM request before it reaches any model provider. These are synthetic ADK
// HITL events the LLM never produced and does not need to reason about.
// The session still stores them so ADK's resume machinery can find them.
func MakeStripConfirmationPartsCallback() llmagent.BeforeModelCallback {
	return func(_ agent.CallbackContext, req *adkmodel.LLMRequest) (*adkmodel.LLMResponse, error) {
		out := make([]*genai.Content, 0, len(req.Contents))
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			filtered := make([]*genai.Part, 0, len(c.Parts))
			for _, p := range c.Parts {
				if p == nil {
					continue
				}
				if p.FunctionCall != nil && p.FunctionCall.Name == "adk_request_confirmation" {
					continue
				}
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "adk_request_confirmation" {
					continue
				}
				filtered = append(filtered, p)
			}
			if len(filtered) == 0 {
				continue
			}
			newContent := &genai.Content{
				Role:  c.Role,
				Parts: filtered,
			}
			out = append(out, newContent)
		}
		req.Contents = out
		return nil, nil
	}
}

// SessionApprovedToolsStateKey is the session-state key holding the list of
// tool names the user approved with "always allow for this session". The key
// has no app:/user: prefix, so its scope is exactly one session.
const SessionApprovedToolsStateKey = "kagent_approved_tools"

// sessionApprovedTools reads the session's always-allowed tool names.
func sessionApprovedTools(ctx agent.ToolContext) map[string]bool {
	allowed := map[string]bool{}
	value, err := ctx.ReadonlyState().Get(SessionApprovedToolsStateKey)
	if err != nil || value == nil {
		return allowed
	}
	switch list := value.(type) {
	case []string:
		for _, name := range list {
			allowed[name] = true
		}
	case []any: // JSON round-trip shape
		for _, item := range list {
			if name, ok := item.(string); ok {
				allowed[name] = true
			}
		}
	}
	return allowed
}

// rememberSessionApprovedTool appends a tool to the session allow list.
func rememberSessionApprovedTool(ctx agent.ToolContext, toolName string) error {
	allowed := sessionApprovedTools(ctx)
	if allowed[toolName] {
		return nil
	}
	names := make([]string, 0, len(allowed)+1)
	for name := range allowed {
		names = append(names, name)
	}
	names = append(names, toolName)
	return ctx.State().Set(SessionApprovedToolsStateKey, names)
}

// MakeApprovalCallback creates a BeforeToolCallback that gates execution of
// tools in the approval set behind request_confirmation / ToolConfirmation.
// Port of kagent-adk/src/kagent/adk/_approval.py:make_approval_callback().
func MakeApprovalCallback(toolsRequiringApproval map[string]bool) llmagent.BeforeToolCallback {
	return func(ctx agent.ToolContext, t tool.Tool, args map[string]any) (map[string]any, error) {
		toolName := t.Name()

		// No approval needed for this tool.
		if !toolsRequiringApproval[toolName] {
			return nil, nil
		}

		// Previously approved with "always allow" in this session.
		if ctx.ToolConfirmation() == nil && sessionApprovedTools(ctx)[toolName] {
			return nil, nil
		}

		// On re-invocation after confirmation, ADK populates ToolConfirmation.
		if confirmation := ctx.ToolConfirmation(); confirmation != nil {
			if confirmation.Confirmed {
				// "Always allow for this session": remember the tool so later
				// calls skip the confirmation round-trip.
				if payload, ok := confirmation.Payload.(map[string]any); ok {
					if always, _ := payload["always_allow"].(bool); always {
						if err := rememberSessionApprovedTool(ctx, toolName); err != nil {
							// Non-fatal: the approval itself still stands.
							_ = err
						}
					}
				}
				// Approved — proceed with tool execution.
				return nil, nil
			}
			// Rejected — extract optional rejection reason from payload.
			payload, _ := confirmation.Payload.(map[string]any)
			reason, _ := payload["rejection_reason"].(string)
			if reason != "" {
				return map[string]any{
					"result": fmt.Sprintf("Tool call was rejected by user. Reason: %s", reason),
				}, nil
			}
			return map[string]any{
				"result": "Tool call was rejected by user.",
			}, nil
		}

		// First invocation — request confirmation and block execution.
		if err := ctx.RequestConfirmation(
			fmt.Sprintf("Tool '%s' requires approval before execution.", toolName),
			nil,
		); err != nil {
			return nil, fmt.Errorf("failed to request confirmation for tool %s: %w", toolName, err)
		}
		return map[string]any{
			"status": "confirmation_requested",
			"tool":   toolName,
		}, nil
	}
}
