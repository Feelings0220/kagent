package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	api "github.com/kagent-dev/kagent/go/api/httpapi"
	"github.com/kagent-dev/kagent/go/core/internal/httpserver/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

const (
	// contextProviderKubernetes names the built-in provider; CI providers
	// (jenkins, ...) plug in alongside it later.
	contextProviderKubernetes = "kubernetes"

	defaultResourceListLimit = 20
	maxResourceListLimit     = 100

	// maxContextYAMLBytes caps the sanitized resource YAML in the context text.
	maxContextYAMLBytes = 12 * 1024
	// maxContextEvents caps how many recent events are appended.
	maxContextEvents = 10
	// maxConfigMapValueBytes truncates each ConfigMap data value.
	maxConfigMapValueBytes = 2 * 1024
)

// contextKind describes one @-mentionable resource kind. The set is a
// deliberate allowlist matching the controller's read RBAC (core, apps,
// batch); Secrets are intentionally excluded.
type contextKind struct {
	gvk        schema.GroupVersionKind
	namespaced bool
}

var contextKinds = map[string]contextKind{
	"pod":         {schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, true},
	"service":     {schema.GroupVersionKind{Version: "v1", Kind: "Service"}, true},
	"configmap":   {schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, true},
	"node":        {schema.GroupVersionKind{Version: "v1", Kind: "Node"}, false},
	"namespace":   {schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, false},
	"deployment":  {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, true},
	"statefulset": {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, true},
	"daemonset":   {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, true},
	"replicaset":  {schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, true},
	"job":         {schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, true},
	"cronjob":     {schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}, true},
}

// ResourcesHandler serves cluster resource listings and injectable context
// text for the chat @-mention feature.
type ResourcesHandler struct {
	*Base
}

// NewResourcesHandler creates a new ResourcesHandler.
func NewResourcesHandler(base *Base) *ResourcesHandler {
	return &ResourcesHandler{Base: base}
}

// HandleListResources handles GET /api/cluster/resources
// Query params: kind (required), namespace, query, limit.
func (h *ResourcesHandler) HandleListResources(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("resources-handler").WithValues("operation", "list")

	kindName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	kind, ok := contextKinds[kindName]
	if !ok {
		w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("Unsupported kind %q", kindName), nil))
		return
	}

	limit := defaultResourceListLimit
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = min(parsed, maxResourceListLimit)
		}
	}
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(kind.gvk.GroupVersion().WithKind(kind.gvk.Kind + "List"))
	var opts []client.ListOption
	if kind.namespaced && namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := h.KubeClient.List(r.Context(), list, opts...); err != nil {
		log.Error(err, "Failed to list resources", "kind", kindName)
		w.RespondWithError(errors.NewInternalServerError("Failed to list resources", err))
		return
	}

	items := make([]api.ClusterResourceItem, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		if query != "" && !strings.Contains(strings.ToLower(item.GetName()), query) {
			continue
		}
		items = append(items, api.ClusterResourceItem{
			Kind:      kindName,
			Namespace: item.GetNamespace(),
			Name:      item.GetName(),
			Status:    resourceStatusSummary(kindName, item),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Namespace != items[j].Namespace {
			return items[i].Namespace < items[j].Namespace
		}
		return items[i].Name < items[j].Name
	})
	if len(items) > limit {
		items = items[:limit]
	}

	RespondWithJSON(w, http.StatusOK, api.NewResponse(items, "Successfully listed resources", false))
}

// HandleGetResourceContext handles GET /api/cluster/resources/context
// Query params: kind (required), name (required), namespace.
func (h *ResourcesHandler) HandleGetResourceContext(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("resources-handler").WithValues("operation", "context")

	kindName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	kind, ok := contextKinds[kindName]
	if !ok {
		w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("Unsupported kind %q", kindName), nil))
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		w.RespondWithError(errors.NewBadRequestError("name is required", nil))
		return
	}
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if kind.namespaced && namespace == "" {
		w.RespondWithError(errors.NewBadRequestError("namespace is required for namespaced kinds", nil))
		return
	}
	if !kind.namespaced {
		namespace = ""
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kind.gvk)
	if err := h.KubeClient.Get(r.Context(), client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			w.RespondWithError(errors.NewNotFoundError("Resource not found", err))
			return
		}
		log.Error(err, "Failed to get resource", "kind", kindName, "name", name)
		w.RespondWithError(errors.NewInternalServerError("Failed to get resource", err))
		return
	}

	text := buildResourceContextText(kindName, kind, obj, h.recentEvents(r, kind, namespace, name))

	response := api.ClusterResourceContext{
		Provider:  contextProviderKubernetes,
		Kind:      kindName,
		Namespace: namespace,
		Name:      name,
		Text:      text,
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(response, "Successfully built resource context", false))
}

// recentEvents returns formatted recent events for the object (best-effort).
func (h *ResourcesHandler) recentEvents(r *http.Request, kind contextKind, namespace, name string) []string {
	eventList := &corev1.EventList{}
	selector := fields.Set{
		"involvedObject.name": name,
		"involvedObject.kind": kind.gvk.Kind,
	}
	opts := []client.ListOption{client.MatchingFieldsSelector{Selector: selector.AsSelector()}}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := h.KubeClient.List(r.Context(), eventList, opts...); err != nil {
		return nil
	}

	events := eventList.Items
	sort.Slice(events, func(i, j int) bool {
		return events[i].LastTimestamp.Time.After(events[j].LastTimestamp.Time)
	})
	if len(events) > maxContextEvents {
		events = events[:maxContextEvents]
	}

	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s",
			event.LastTimestamp.Format("2006-01-02T15:04:05Z"), event.Type, event.Reason, event.Message))
	}
	return lines
}

// buildResourceContextText renders the sanitized YAML plus recent events.
func buildResourceContextText(kindName string, kind contextKind, obj *unstructured.Unstructured, eventLines []string) string {
	sanitizeResource(kindName, obj)

	yamlBytes, err := yaml.Marshal(obj.Object)
	if err != nil {
		yamlBytes = []byte(fmt.Sprintf("failed to render resource: %v", err))
	}
	yamlText := string(yamlBytes)
	if len(yamlText) > maxContextYAMLBytes {
		yamlText = yamlText[:maxContextYAMLBytes] + "\n... (truncated)"
	}

	ref := obj.GetName()
	if obj.GetNamespace() != "" {
		ref = obj.GetNamespace() + "/" + obj.GetName()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "=== Kubernetes context: %s %s ===\n", kind.gvk.Kind, ref)
	b.WriteString(yamlText)
	if len(eventLines) > 0 {
		b.WriteString("\n--- Recent events (newest first) ---\n")
		b.WriteString(strings.Join(eventLines, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// sanitizeResource strips noisy/bulky fields before injection.
func sanitizeResource(kindName string, obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	annotations := obj.GetAnnotations()
	if annotations != nil {
		delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
		if len(annotations) == 0 {
			unstructured.RemoveNestedField(obj.Object, "metadata", "annotations")
		} else {
			obj.SetAnnotations(annotations)
		}
	}

	// ConfigMaps can carry huge blobs; truncate each value individually.
	if kindName == "configmap" {
		if data, found, _ := unstructured.NestedStringMap(obj.Object, "data"); found {
			truncated := false
			for key, value := range data {
				if len(value) > maxConfigMapValueBytes {
					data[key] = value[:maxConfigMapValueBytes] + "... (truncated)"
					truncated = true
				}
			}
			if truncated {
				_ = unstructured.SetNestedStringMap(obj.Object, data, "data")
			}
		}
	}
}

// resourceStatusSummary returns a short, kind-specific status string.
func resourceStatusSummary(kindName string, obj *unstructured.Unstructured) string {
	switch kindName {
	case "pod", "namespace":
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		return phase
	case "deployment", "statefulset":
		ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
		desired, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
		return fmt.Sprintf("%d/%d ready", ready, desired)
	case "daemonset":
		ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "numberReady")
		desired, _, _ := unstructured.NestedInt64(obj.Object, "status", "desiredNumberScheduled")
		return fmt.Sprintf("%d/%d ready", ready, desired)
	case "node":
		conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if condition["type"] == "Ready" {
				if condition["status"] == "True" {
					return "Ready"
				}
				return "NotReady"
			}
		}
		return ""
	case "job":
		succeeded, _, _ := unstructured.NestedInt64(obj.Object, "status", "succeeded")
		if succeeded > 0 {
			return "Succeeded"
		}
		failed, _, _ := unstructured.NestedInt64(obj.Object, "status", "failed")
		if failed > 0 {
			return "Failed"
		}
		active, _, _ := unstructured.NestedInt64(obj.Object, "status", "active")
		if active > 0 {
			return "Active"
		}
		return ""
	default:
		return ""
	}
}
