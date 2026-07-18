package agent

import (
	"context"
	"iter"
	"maps"
	"testing"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// fakeState is an in-memory session.State.
type fakeState struct{ values map[string]any }

func (s *fakeState) Get(key string) (any, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}
func (s *fakeState) Set(key string, value any) error {
	s.values[key] = value
	return nil
}
func (s *fakeState) All() iter.Seq2[string, any] { return maps.All(s.values) }

// fakeToolContext implements agent.ToolContext for approval-callback tests.
type fakeToolContext struct {
	context.Context
	state                 *fakeState
	confirmation          *toolconfirmation.ToolConfirmation
	confirmationRequested bool
}

func newFakeToolContext() *fakeToolContext {
	return &fakeToolContext{Context: context.Background(), state: &fakeState{values: map[string]any{}}}
}

func (c *fakeToolContext) UserContent() *genai.Content          { return nil }
func (c *fakeToolContext) InvocationID() string                 { return "inv-1" }
func (c *fakeToolContext) AgentName() string                    { return "test-agent" }
func (c *fakeToolContext) ReadonlyState() session.ReadonlyState { return c.state }
func (c *fakeToolContext) UserID() string                       { return "user-1" }
func (c *fakeToolContext) AppName() string                      { return "app" }
func (c *fakeToolContext) SessionID() string                    { return "session-1" }
func (c *fakeToolContext) Branch() string                       { return "" }
func (c *fakeToolContext) Artifacts() adkagent.Artifacts        { return nil }
func (c *fakeToolContext) State() session.State                 { return c.state }
func (c *fakeToolContext) FunctionCallID() string               { return "fc-1" }
func (c *fakeToolContext) Actions() *session.EventActions       { return &session.EventActions{} }
func (c *fakeToolContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (c *fakeToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return c.confirmation
}
func (c *fakeToolContext) RequestConfirmation(string, any) error {
	c.confirmationRequested = true
	return nil
}

// fakeTool is a minimal tool.Tool.
type fakeTool struct{ name string }

func (t *fakeTool) Name() string        { return t.name }
func (t *fakeTool) Description() string { return "fake" }
func (t *fakeTool) IsLongRunning() bool { return false }

var _ tool.Tool = (*fakeTool)(nil)

func TestMakeApprovalCallback(t *testing.T) {
	gated := map[string]bool{"kubectl_apply": true}
	callback := MakeApprovalCallback(gated)
	applyTool := &fakeTool{name: "kubectl_apply"}
	freeTool := &fakeTool{name: "kubectl_get"}

	tests := []struct {
		name                      string
		tool                      tool.Tool
		setup                     func(ctx *fakeToolContext)
		wantResultNil             bool
		wantConfirmationRequested bool
		wantRemembered            bool
	}{
		{
			name:          "ungated tool passes through",
			tool:          freeTool,
			setup:         func(ctx *fakeToolContext) {},
			wantResultNil: true,
		},
		{
			name:                      "first call requests confirmation",
			tool:                      applyTool,
			setup:                     func(ctx *fakeToolContext) {},
			wantResultNil:             false,
			wantConfirmationRequested: true,
		},
		{
			name: "plain approval proceeds without remembering",
			tool: applyTool,
			setup: func(ctx *fakeToolContext) {
				ctx.confirmation = &toolconfirmation.ToolConfirmation{Confirmed: true}
			},
			wantResultNil: true,
		},
		{
			name: "always_allow approval remembers the tool",
			tool: applyTool,
			setup: func(ctx *fakeToolContext) {
				ctx.confirmation = &toolconfirmation.ToolConfirmation{
					Confirmed: true,
					Payload:   map[string]any{"always_allow": true},
				}
			},
			wantResultNil:  true,
			wantRemembered: true,
		},
		{
			name: "session-approved tool skips confirmation",
			tool: applyTool,
			setup: func(ctx *fakeToolContext) {
				_ = ctx.state.Set(SessionApprovedToolsStateKey, []string{"kubectl_apply"})
			},
			wantResultNil: true,
		},
		{
			name: "session approval list from JSON round-trip shape",
			tool: applyTool,
			setup: func(ctx *fakeToolContext) {
				_ = ctx.state.Set(SessionApprovedToolsStateKey, []any{"kubectl_apply"})
			},
			wantResultNil: true,
		},
		{
			name: "rejection returns result message",
			tool: applyTool,
			setup: func(ctx *fakeToolContext) {
				ctx.confirmation = &toolconfirmation.ToolConfirmation{Confirmed: false}
			},
			wantResultNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newFakeToolContext()
			tt.setup(ctx)

			result, err := callback(ctx, tt.tool, map[string]any{})
			if err != nil {
				t.Fatalf("callback error = %v", err)
			}
			if (result == nil) != tt.wantResultNil {
				t.Errorf("result = %v, wantNil %v", result, tt.wantResultNil)
			}
			if ctx.confirmationRequested != tt.wantConfirmationRequested {
				t.Errorf("confirmationRequested = %v, want %v", ctx.confirmationRequested, tt.wantConfirmationRequested)
			}
			if tt.wantRemembered {
				if !sessionApprovedTools(ctx)["kubectl_apply"] {
					t.Error("expected tool to be remembered in session state")
				}
			}
		})
	}
}
