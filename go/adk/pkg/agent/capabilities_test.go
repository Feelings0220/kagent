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
func (s *fakeState) Set(key string, value any) error { s.values[key] = value; return nil }
func (s *fakeState) All() iter.Seq2[string, any]     { return maps.All(s.values) }

// fakeToolContext implements agent.ToolContext for capability tests.
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

func TestMakeCapabilityCallback(t *testing.T) {
	callback := MakeCapabilityCallback()
	readTool := &fakeTool{name: "k8s_get_resource"} // cluster-read (default-granted)
	writeTool := &fakeTool{name: "k8s_scale"}       // cluster-write (grantable)
	reqTool := &fakeTool{name: RequestCapabilityToolName}

	tests := []struct {
		name                      string
		tool                      tool.Tool
		args                      map[string]any
		setup                     func(ctx *fakeToolContext)
		wantResultNil             bool
		wantConfirmationRequested bool
		wantCapGranted            string // capability expected to be granted after the call
	}{
		{
			name:          "default-granted read tool passes through",
			tool:          readTool,
			wantResultNil: true,
		},
		{
			name:                      "ungranted write tool requests the capability",
			tool:                      writeTool,
			wantResultNil:             false,
			wantConfirmationRequested: true,
		},
		{
			name: "granted write tool passes through",
			tool: writeTool,
			setup: func(ctx *fakeToolContext) {
				_ = ctx.state.Set(GrantedCapabilitiesStateKey, []string{"cluster-write"})
			},
			wantResultNil: true,
		},
		{
			name: "session grant from JSON round-trip shape",
			tool: writeTool,
			setup: func(ctx *fakeToolContext) {
				_ = ctx.state.Set(GrantedCapabilitiesStateKey, []any{"cluster-write"})
			},
			wantResultNil: true,
		},
		{
			name: "approving a gated tool grants its whole capability",
			tool: writeTool,
			setup: func(ctx *fakeToolContext) {
				ctx.confirmation = &toolconfirmation.ToolConfirmation{Confirmed: true}
			},
			wantResultNil:  true,
			wantCapGranted: "cluster-write",
		},
		{
			name: "rejecting a gated tool returns a message",
			tool: writeTool,
			setup: func(ctx *fakeToolContext) {
				ctx.confirmation = &toolconfirmation.ToolConfirmation{Confirmed: false}
			},
			wantResultNil: false,
		},
		{
			name:                      "request_capability first call asks for confirmation",
			tool:                      reqTool,
			args:                      map[string]any{"capability": "cluster-write", "reason": "fix a deployment"},
			wantResultNil:             false,
			wantConfirmationRequested: true,
		},
		{
			name: "request_capability approval grants the capability",
			tool: reqTool,
			args: map[string]any{"capability": "cluster-write"},
			setup: func(ctx *fakeToolContext) {
				ctx.confirmation = &toolconfirmation.ToolConfirmation{Confirmed: true}
			},
			wantResultNil:  false, // returns a result message
			wantCapGranted: "cluster-write",
		},
		{
			name:          "request_capability rejects an unknown capability",
			tool:          reqTool,
			args:          map[string]any{"capability": "root-on-nodes"},
			wantResultNil: false,
		},
		{
			name:          "request_capability rejects a default-granted capability",
			tool:          reqTool,
			args:          map[string]any{"capability": "cluster-read"},
			wantResultNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newFakeToolContext()
			if tt.setup != nil {
				tt.setup(ctx)
			}
			args := tt.args
			if args == nil {
				args = map[string]any{}
			}

			result, err := callback(ctx, tt.tool, args)
			if err != nil {
				t.Fatalf("callback error = %v", err)
			}
			if (result == nil) != tt.wantResultNil {
				t.Errorf("result = %v, wantNil %v", result, tt.wantResultNil)
			}
			if ctx.confirmationRequested != tt.wantConfirmationRequested {
				t.Errorf("confirmationRequested = %v, want %v", ctx.confirmationRequested, tt.wantConfirmationRequested)
			}
			if tt.wantCapGranted != "" && !grantedCapabilities(ctx)[tt.wantCapGranted] {
				t.Errorf("expected capability %q to be granted", tt.wantCapGranted)
			}
		})
	}
}

func TestGrantCapabilityIdempotent(t *testing.T) {
	ctx := newFakeToolContext()
	if err := grantCapability(ctx, "cluster-write"); err != nil {
		t.Fatal(err)
	}
	if err := grantCapability(ctx, "cluster-write"); err != nil {
		t.Fatal(err)
	}
	value, _ := ctx.state.Get(GrantedCapabilitiesStateKey)
	names, _ := value.([]string)
	if len(names) != 1 {
		t.Errorf("granted list = %v, want exactly one entry", names)
	}
}

func TestGrantableCapabilityForTool(t *testing.T) {
	if _, gated := grantableCapabilityForTool("k8s_get_resource"); gated {
		t.Error("cluster-read tool should not be gated (default-granted)")
	}
	if c, gated := grantableCapabilityForTool("k8s_apply"); !gated || c.Name != "cluster-write" {
		t.Errorf("k8s_apply should gate on cluster-write, got %q gated=%v", c.Name, gated)
	}
	if _, gated := grantableCapabilityForTool("some_random_tool"); gated {
		t.Error("unknown tool should not be gated")
	}
}
