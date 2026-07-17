package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Builtin Kubernetes tool names. The tools proxy through the kagent
// controller's /api/cluster endpoints (KAGENT_URL), so agents get the
// controller's read RBAC without any per-agent RBAC configuration.
const (
	K8sGetResourceToolName    = "k8s_get_resource"
	K8sListResourcesToolName  = "k8s_list_resources"
	K8sPodLogsToolName        = "k8s_pod_logs"
	K8sEventsToolName         = "k8s_events"
	K8sAPIResourcesToolName   = "k8s_api_resources"
	K8sApplyToolName          = "k8s_apply"
	K8sDeleteToolName         = "k8s_delete"
	K8sScaleToolName          = "k8s_scale"
	K8sRolloutRestartToolName = "k8s_rollout_restart"
)

// K8sReadToolNames lists the read-only cluster tools.
var K8sReadToolNames = []string{
	K8sGetResourceToolName,
	K8sListResourcesToolName,
	K8sPodLogsToolName,
	K8sEventsToolName,
	K8sAPIResourcesToolName,
}

// K8sWriteToolNames lists the destructive cluster tools. These are always
// HITL-gated: the runtime adds them to the approval set unconditionally.
var K8sWriteToolNames = []string{
	K8sApplyToolName,
	K8sDeleteToolName,
	K8sScaleToolName,
	K8sRolloutRestartToolName,
}

// IsK8sToolName reports whether name is one of the builtin k8s tools.
func IsK8sToolName(name string) bool {
	for _, known := range K8sReadToolNames {
		if name == known {
			return true
		}
	}
	for _, known := range K8sWriteToolNames {
		if name == known {
			return true
		}
	}
	return false
}

// k8sAPI is a minimal client for the controller's /api/cluster endpoints.
type k8sAPI struct {
	baseURL    string
	httpClient *http.Client
}

// standardEnvelope mirrors httpapi.StandardResponse with raw data.
type standardEnvelope struct {
	Error   bool            `json:"error"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

const maxToolResponseBytes = 256 * 1024

func (c *k8sAPI) call(ctx adkagent.ToolContext, method, path string, query url.Values, body any) (json.RawMessage, error) {
	endpoint := strings.TrimRight(c.baseURL, "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode request: %w", err)
		}
		reader = strings.NewReader(string(payload))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("cluster API request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxToolResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read cluster API response: %w", err)
	}
	var envelope standardEnvelope
	if unmarshalErr := json.Unmarshal(raw, &envelope); unmarshalErr != nil {
		envelope = standardEnvelope{Message: strings.TrimSpace(string(raw))}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := envelope.Message
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return nil, fmt.Errorf("cluster API returned %d: %s", response.StatusCode, message)
	}
	return envelope.Data, nil
}

type k8sGetResourceInput struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type k8sListResourcesInput struct {
	Kind          string `json:"kind"`
	Namespace     string `json:"namespace,omitempty"`
	LabelSelector string `json:"label_selector,omitempty"`
	FieldSelector string `json:"field_selector,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type k8sPodLogsInput struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Container    string `json:"container,omitempty"`
	TailLines    int    `json:"tail_lines,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
	SinceSeconds int    `json:"since_seconds,omitempty"`
}

type k8sEventsInput struct {
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type k8sAPIResourcesInput struct{}

type k8sApplyInput struct {
	YAML string `json:"yaml"`
}

type k8sDeleteInput struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type k8sScaleInput struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Replicas  int32  `json:"replicas"`
}

type k8sRolloutRestartInput struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// NewK8sTools builds the requested builtin k8s tools backed by the kagent
// controller at baseURL. Unknown names are ignored so newer CRD enum values
// don't break older runtimes. Returns nil when baseURL is empty (no
// controller to proxy through).
func NewK8sTools(baseURL string, names []string, httpClient *http.Client) ([]tool.Tool, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" || len(names) == 0 {
		return nil, nil
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	apiClient := &k8sAPI{baseURL: baseURL, httpClient: httpClient}

	builders := map[string]func() (tool.Tool, error){
		K8sGetResourceToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sGetResourceToolName,
				Description: `Get one Kubernetes resource as sanitized YAML plus its recent events.

- kind: resource kind or kubectl shorthand ("pod", "deploy", "svc", "deployments.apps", ...)
- name: resource name (required)
- namespace: required for namespaced kinds
Secrets are never accessible.`,
			}, func(ctx adkagent.ToolContext, in k8sGetResourceInput) (string, error) {
				query := url.Values{"kind": {in.Kind}, "name": {in.Name}}
				if in.Namespace != "" {
					query.Set("namespace", in.Namespace)
				}
				data, err := apiClient.call(ctx, http.MethodGet, "/api/cluster/query/resource", query, nil)
				if err != nil {
					return err.Error(), nil
				}
				var resource struct {
					Kind       string   `json:"kind"`
					APIVersion string   `json:"apiVersion"`
					Namespace  string   `json:"namespace"`
					Name       string   `json:"name"`
					YAML       string   `json:"yaml"`
					Events     []string `json:"events"`
				}
				if err := json.Unmarshal(data, &resource); err != nil {
					return "", fmt.Errorf("unexpected response: %w", err)
				}
				var b strings.Builder
				fmt.Fprintf(&b, "# %s %s/%s (%s)\n", resource.Kind, resource.Namespace, resource.Name, resource.APIVersion)
				b.WriteString(resource.YAML)
				if len(resource.Events) > 0 {
					b.WriteString("\n--- Recent events (newest first) ---\n")
					b.WriteString(strings.Join(resource.Events, "\n"))
				}
				return b.String(), nil
			})
		},
		K8sListResourcesToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sListResourcesToolName,
				Description: `List Kubernetes resources of one kind.

- kind: resource kind or kubectl shorthand ("pod", "deploy", "svc", "nodes", ...)
- namespace: empty = all namespaces
- label_selector / field_selector: optional standard selectors
- limit: max items (default 50)
Returns one line per item: NAMESPACE/NAME, STATUS, CREATED. Secrets are never accessible.`,
			}, func(ctx adkagent.ToolContext, in k8sListResourcesInput) (string, error) {
				query := url.Values{"kind": {in.Kind}}
				if in.Namespace != "" {
					query.Set("namespace", in.Namespace)
				}
				if in.LabelSelector != "" {
					query.Set("labelSelector", in.LabelSelector)
				}
				if in.FieldSelector != "" {
					query.Set("fieldSelector", in.FieldSelector)
				}
				if in.Limit > 0 {
					query.Set("limit", strconv.Itoa(in.Limit))
				}
				data, err := apiClient.call(ctx, http.MethodGet, "/api/cluster/query/list", query, nil)
				if err != nil {
					return err.Error(), nil
				}
				var items []struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
					Status    string `json:"status"`
					CreatedAt string `json:"createdAt"`
				}
				if err := json.Unmarshal(data, &items); err != nil {
					return "", fmt.Errorf("unexpected response: %w", err)
				}
				if len(items) == 0 {
					return "No resources found.", nil
				}
				var b strings.Builder
				for _, item := range items {
					ref := item.Name
					if item.Namespace != "" {
						ref = item.Namespace + "/" + item.Name
					}
					fmt.Fprintf(&b, "%s\t%s\t%s\n", ref, item.Status, item.CreatedAt)
				}
				return b.String(), nil
			})
		},
		K8sPodLogsToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sPodLogsToolName,
				Description: `Fetch logs from a pod (with timestamps).

- name / namespace: the pod (both required)
- container: optional, defaults to the only/first container
- tail_lines: default 200
- previous: true to read the previous (crashed) container instance
- since_seconds: only logs newer than this many seconds`,
			}, func(ctx adkagent.ToolContext, in k8sPodLogsInput) (string, error) {
				query := url.Values{"name": {in.Name}, "namespace": {in.Namespace}}
				if in.Container != "" {
					query.Set("container", in.Container)
				}
				if in.TailLines > 0 {
					query.Set("tailLines", strconv.Itoa(in.TailLines))
				}
				if in.Previous {
					query.Set("previous", "true")
				}
				if in.SinceSeconds > 0 {
					query.Set("sinceSeconds", strconv.Itoa(in.SinceSeconds))
				}
				data, err := apiClient.call(ctx, http.MethodGet, "/api/cluster/query/logs", query, nil)
				if err != nil {
					return err.Error(), nil
				}
				var logs struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
					Container string `json:"container"`
					Logs      string `json:"logs"`
					Truncated bool   `json:"truncated"`
				}
				if err := json.Unmarshal(data, &logs); err != nil {
					return "", fmt.Errorf("unexpected response: %w", err)
				}
				header := fmt.Sprintf("# logs %s/%s", logs.Namespace, logs.Name)
				if logs.Container != "" {
					header += " container=" + logs.Container
				}
				result := header + "\n" + logs.Logs
				if logs.Truncated {
					result += "\n... (truncated; narrow with tail_lines or since_seconds)"
				}
				return result, nil
			})
		},
		K8sEventsToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sEventsToolName,
				Description: `List recent Kubernetes events (newest first) for error correlation.

- namespace: optional filter
- kind / name: optional involved-object filter (e.g. kind="pod", name="my-pod")
- limit: default 30
Use this to correlate failures across nodes, pods and controllers.`,
			}, func(ctx adkagent.ToolContext, in k8sEventsInput) (string, error) {
				query := url.Values{}
				if in.Namespace != "" {
					query.Set("namespace", in.Namespace)
				}
				if in.Kind != "" {
					query.Set("kind", in.Kind)
				}
				if in.Name != "" {
					query.Set("name", in.Name)
				}
				if in.Limit > 0 {
					query.Set("limit", strconv.Itoa(in.Limit))
				}
				data, err := apiClient.call(ctx, http.MethodGet, "/api/cluster/query/events", query, nil)
				if err != nil {
					return err.Error(), nil
				}
				var events []struct {
					Namespace    string `json:"namespace"`
					LastSeen     string `json:"lastSeen"`
					Type         string `json:"type"`
					Reason       string `json:"reason"`
					InvolvedKind string `json:"involvedKind"`
					InvolvedName string `json:"involvedName"`
					Message      string `json:"message"`
					Count        int32  `json:"count"`
				}
				if err := json.Unmarshal(data, &events); err != nil {
					return "", fmt.Errorf("unexpected response: %w", err)
				}
				if len(events) == 0 {
					return "No events found.", nil
				}
				var b strings.Builder
				for _, event := range events {
					fmt.Fprintf(&b, "%s\t%s\t%s\t%s %s/%s\tx%d\t%s\n",
						event.LastSeen, event.Type, event.Reason,
						event.InvolvedKind, event.Namespace, event.InvolvedName,
						event.Count, event.Message)
				}
				return b.String(), nil
			})
		},
		K8sAPIResourcesToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sAPIResourcesToolName,
				Description: `List the resource kinds queryable in this cluster (like kubectl api-resources).

Returns one line per kind: RESOURCE, KIND, GROUP/VERSION, NAMESPACED.`,
			}, func(ctx adkagent.ToolContext, _ k8sAPIResourcesInput) (string, error) {
				data, err := apiClient.call(ctx, http.MethodGet, "/api/cluster/query/kinds", nil, nil)
				if err != nil {
					return err.Error(), nil
				}
				var kinds []struct {
					Kind       string `json:"kind"`
					Group      string `json:"group"`
					Version    string `json:"version"`
					Resource   string `json:"resource"`
					Namespaced bool   `json:"namespaced"`
				}
				if err := json.Unmarshal(data, &kinds); err != nil {
					return "", fmt.Errorf("unexpected response: %w", err)
				}
				var b strings.Builder
				for _, kind := range kinds {
					groupVersion := kind.Version
					if kind.Group != "" {
						groupVersion = kind.Group + "/" + kind.Version
					}
					fmt.Fprintf(&b, "%s\t%s\t%s\tnamespaced=%t\n", kind.Resource, kind.Kind, groupVersion, kind.Namespaced)
				}
				return b.String(), nil
			})
		},
		K8sApplyToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sApplyToolName,
				Description: `DESTRUCTIVE: server-side apply Kubernetes YAML (create or update resources).

- yaml: one or more YAML documents separated by "---"
Use for changes like updating a deployment's config or running a debug/test pod.
Requires user approval; the deployment must enable cluster write tools.`,
			}, func(ctx adkagent.ToolContext, in k8sApplyInput) (string, error) {
				data, err := apiClient.call(ctx, http.MethodPost, "/api/cluster/apply", nil, map[string]string{"yaml": in.YAML})
				if err != nil {
					return err.Error(), nil
				}
				var applied []struct {
					Kind      string `json:"kind"`
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				}
				if err := json.Unmarshal(data, &applied); err != nil {
					return "Applied.", nil
				}
				lines := make([]string, 0, len(applied))
				for _, item := range applied {
					ref := item.Name
					if item.Namespace != "" {
						ref = item.Namespace + "/" + item.Name
					}
					lines = append(lines, fmt.Sprintf("applied %s %s", item.Kind, ref))
				}
				return strings.Join(lines, "\n"), nil
			})
		},
		K8sDeleteToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sDeleteToolName,
				Description: `DESTRUCTIVE: delete one Kubernetes resource.

- kind / name / namespace identify the resource.
Requires user approval; the deployment must enable cluster write tools.`,
			}, func(ctx adkagent.ToolContext, in k8sDeleteInput) (string, error) {
				return apiClient.messageCall(ctx, "/api/cluster/delete", in)
			})
		},
		K8sScaleToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sScaleToolName,
				Description: `DESTRUCTIVE: scale a workload (deployment/statefulset/replicaset) to a replica count.

- kind / name / namespace identify the workload; replicas is the target count.
Requires user approval; the deployment must enable cluster write tools.`,
			}, func(ctx adkagent.ToolContext, in k8sScaleInput) (string, error) {
				return apiClient.messageCall(ctx, "/api/cluster/scale", in)
			})
		},
		K8sRolloutRestartToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: K8sRolloutRestartToolName,
				Description: `DESTRUCTIVE: rolling-restart a workload (like kubectl rollout restart).

- kind / name / namespace identify the workload (deployment/statefulset/daemonset).
Requires user approval; the deployment must enable cluster write tools.`,
			}, func(ctx adkagent.ToolContext, in k8sRolloutRestartInput) (string, error) {
				return apiClient.messageCall(ctx, "/api/cluster/rollout-restart", in)
			})
		},
	}

	var selected []tool.Tool
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		builder, ok := builders[name]
		if !ok {
			continue
		}
		builtTool, err := builder()
		if err != nil {
			return nil, fmt.Errorf("failed to create %s tool: %w", name, err)
		}
		selected = append(selected, builtTool)
	}
	return selected, nil
}

// messageCall posts a body and returns the response's data as plain text.
func (c *k8sAPI) messageCall(ctx adkagent.ToolContext, path string, body any) (string, error) {
	data, err := c.call(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return err.Error(), nil
	}
	var message string
	if err := json.Unmarshal(data, &message); err != nil {
		return strings.TrimSpace(string(data)), nil
	}
	return message, nil
}
