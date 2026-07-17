package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adkagent "google.golang.org/adk/agent"
)

func TestNewK8sToolsSelection(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		names     []string
		wantTools []string
	}{
		{
			name:      "no base URL yields no tools",
			baseURL:   "",
			names:     []string{K8sGetResourceToolName},
			wantTools: nil,
		},
		{
			name:      "unknown names ignored",
			baseURL:   "http://controller",
			names:     []string{"bash", K8sPodLogsToolName, "nope"},
			wantTools: []string{K8sPodLogsToolName},
		},
		{
			name:      "duplicates deduped",
			baseURL:   "http://controller",
			names:     []string{K8sEventsToolName, K8sEventsToolName},
			wantTools: []string{K8sEventsToolName},
		},
		{
			name:    "read and write sets covered",
			baseURL: "http://controller",
			names:   append(append([]string{}, K8sReadToolNames...), K8sWriteToolNames...),
			wantTools: []string{
				K8sGetResourceToolName, K8sListResourcesToolName, K8sPodLogsToolName,
				K8sEventsToolName, K8sAPIResourcesToolName,
				K8sApplyToolName, K8sDeleteToolName, K8sScaleToolName, K8sRolloutRestartToolName,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			built, err := NewK8sTools(tt.baseURL, tt.names, nil)
			if err != nil {
				t.Fatalf("NewK8sTools() error = %v", err)
			}
			var names []string
			for _, builtTool := range built {
				names = append(names, builtTool.Name())
			}
			if len(names) != len(tt.wantTools) {
				t.Fatalf("got tools %v, want %v", names, tt.wantTools)
			}
			for i := range names {
				if names[i] != tt.wantTools[i] {
					t.Errorf("tool[%d] = %s, want %s", i, names[i], tt.wantTools[i])
				}
			}
		})
	}
}

func TestIsK8sToolName(t *testing.T) {
	if !IsK8sToolName(K8sApplyToolName) || !IsK8sToolName(K8sPodLogsToolName) {
		t.Error("expected k8s tool names to be recognized")
	}
	if IsK8sToolName("bash") || IsK8sToolName("") {
		t.Error("expected non-k8s names to be rejected")
	}
}

func TestK8sAPICall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cluster/query/logs":
			if r.URL.Query().Get("name") != "nginx-1" {
				http.Error(w, "wrong pod", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": false,
				"data": map[string]any{
					"namespace": "default",
					"name":      "nginx-1",
					"logs":      "line-1\nline-2",
					"truncated": true,
				},
			})
		case "/api/cluster/scale":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   true,
				"message": "Destructive cluster tools are disabled on this kagent deployment.",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	apiClient := &k8sAPI{baseURL: server.URL, httpClient: server.Client()}

	t.Run("successful call returns data", func(t *testing.T) {
		data, err := apiClient.call(testToolCtx(), http.MethodGet, "/api/cluster/query/logs",
			map[string][]string{"name": {"nginx-1"}, "namespace": {"default"}}, nil)
		if err != nil {
			t.Fatalf("call() error = %v", err)
		}
		if !strings.Contains(string(data), "line-1") {
			t.Errorf("data = %s, want logs content", string(data))
		}
	})

	t.Run("error status surfaces server message", func(t *testing.T) {
		_, err := apiClient.call(testToolCtx(), http.MethodPost, "/api/cluster/scale", nil, map[string]string{})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "disabled") {
			t.Errorf("error = %v, want the server's message", err)
		}
	})
}

// testToolCtx builds the minimal ToolContext the HTTP client needs (context).
func testToolCtx() adkagent.ToolContext {
	return httpOnlyToolContext{Context: context.Background()}
}

type httpOnlyToolContext struct {
	context.Context
	adkagent.ToolContext
}

// Disambiguate the context methods shared by both embedded interfaces.
func (c httpOnlyToolContext) Deadline() (time.Time, bool) { return c.Context.Deadline() }
func (c httpOnlyToolContext) Done() <-chan struct{}       { return c.Context.Done() }
func (c httpOnlyToolContext) Err() error                  { return c.Context.Err() }
func (c httpOnlyToolContext) Value(key any) any           { return c.Context.Value(key) }
