package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	api "github.com/kagent-dev/kagent/go/api/httpapi"
	"github.com/kagent-dev/kagent/go/core/internal/httpserver/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

const (
	defaultQueryListLimit = 50
	maxQueryListLimit     = 500

	defaultEventsLimit = 30
	maxEventsLimit     = 200

	defaultLogTailLines = 200
	maxLogTailLines     = 2000
	// maxLogBytes caps a single log response so huge logs can't blow up the
	// LLM context; callers page with sinceSeconds/tailLines instead.
	maxLogBytes = 64 * 1024
)

// kindAliases maps kubectl-style shorthands to canonical resource names.
var kindAliases = map[string]string{
	"po":     "pods",
	"svc":    "services",
	"deploy": "deployments",
	"ns":     "namespaces",
	"no":     "nodes",
	"cm":     "configmaps",
	"pvc":    "persistentvolumeclaims",
	"pv":     "persistentvolumes",
	"sa":     "serviceaccounts",
	"ing":    "ingresses",
	"sts":    "statefulsets",
	"ds":     "daemonsets",
	"rs":     "replicasets",
	"cj":     "cronjobs",
	"hpa":    "horizontalpodautoscalers",
	"netpol": "networkpolicies",
	"ep":     "endpoints",
	"ev":     "events",
	"crd":    "customresourcedefinitions",
}

// ClusterQueryHandler serves the generic cluster query API backing the agent
// k8s_* builtin tools: list/get arbitrary kinds, pod logs, and events.
// Secrets are always refused; everything else is bounded by the controller's
// RBAC (Forbidden errors surface to the caller).
type ClusterQueryHandler struct {
	*Base
}

// NewClusterQueryHandler creates a new ClusterQueryHandler.
func NewClusterQueryHandler(base *Base) *ClusterQueryHandler {
	return &ClusterQueryHandler{Base: base}
}

// resolveKind maps a user-supplied kind string ("pod", "deploy",
// "deployments.apps", "Ingress") to a GVK and its scope.
func (h *ClusterQueryHandler) resolveKind(input string) (schema.GroupVersionKind, bool, error) {
	raw := strings.ToLower(strings.TrimSpace(input))
	if raw == "" {
		return schema.GroupVersionKind{}, false, fmt.Errorf("kind is required")
	}

	// Optional "resource.group" qualifier (e.g. "deployments.apps").
	group := ""
	resource := raw
	if idx := strings.Index(raw, "."); idx > 0 {
		resource, group = raw[:idx], raw[idx+1:]
	}
	if alias, ok := kindAliases[resource]; ok {
		resource = alias
	}

	// The static allowlist doubles as a fast path and a fallback when no
	// RESTMapper is configured (tests).
	if group == "" {
		if kind, ok := contextKinds[strings.TrimSuffix(resource, "s")]; ok {
			return kind.gvk, kind.namespaced, nil
		}
	}

	if h.Cluster == nil || h.Cluster.RESTMapper == nil {
		return schema.GroupVersionKind{}, false, fmt.Errorf("unknown kind %q", input)
	}

	// Try plural/singular spellings against discovery.
	candidates := []string{resource}
	if !strings.HasSuffix(resource, "s") {
		candidates = append(candidates, resource+"s", resource+"es")
		if strings.HasSuffix(resource, "y") {
			candidates = append(candidates, strings.TrimSuffix(resource, "y")+"ies")
		}
	} else {
		candidates = append(candidates, resource+"es")
	}
	for _, candidate := range candidates {
		gvk, err := h.Cluster.RESTMapper.KindFor(schema.GroupVersionResource{Group: group, Resource: candidate})
		if err != nil {
			continue
		}
		mapping, err := h.Cluster.RESTMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			continue
		}
		namespaced := mapping.Scope.Name() == "namespace"
		return gvk, namespaced, nil
	}
	return schema.GroupVersionKind{}, false, fmt.Errorf("unknown kind %q", input)
}

// forbidSecrets rejects Secret access regardless of RBAC.
func forbidSecrets(gvk schema.GroupVersionKind) error {
	if gvk.Group == "" && gvk.Kind == "Secret" {
		return fmt.Errorf("access to Secrets is not allowed through cluster query tools")
	}
	return nil
}

func queryLimit(r *http.Request, fallback, ceiling int) int {
	limit := fallback
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = min(parsed, ceiling)
		}
	}
	return limit
}

// HandleListKinds handles GET /api/cluster/query/kinds — API discovery of
// queryable kinds (Secrets excluded).
func (h *ClusterQueryHandler) HandleListKinds(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-query").WithValues("operation", "kinds")

	// Fallback for deployments without discovery access: the static set.
	if h.Cluster == nil || h.Cluster.Clientset == nil {
		kinds := make([]api.ClusterKindInfo, 0, len(contextKinds))
		for name, kind := range contextKinds {
			kinds = append(kinds, api.ClusterKindInfo{
				Kind:       kind.gvk.Kind,
				Group:      kind.gvk.Group,
				Version:    kind.gvk.Version,
				Resource:   name + "s",
				Namespaced: kind.namespaced,
			})
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i].Resource < kinds[j].Resource })
		RespondWithJSON(w, http.StatusOK, api.NewResponse(kinds, "Successfully listed kinds", false))
		return
	}

	resourceLists, err := h.Cluster.Clientset.Discovery().ServerPreferredResources()
	if err != nil && len(resourceLists) == 0 {
		log.Error(err, "Failed to discover API resources")
		w.RespondWithError(errors.NewInternalServerError("Failed to discover API resources", err))
		return
	}

	kinds := make([]api.ClusterKindInfo, 0, 64)
	for _, resourceList := range resourceLists {
		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			continue
		}
		for _, resource := range resourceList.APIResources {
			// Skip subresources ("pods/log") and anything not listable.
			if strings.Contains(resource.Name, "/") {
				continue
			}
			listable := false
			for _, verb := range resource.Verbs {
				if verb == "list" {
					listable = true
					break
				}
			}
			if !listable {
				continue
			}
			if gv.Group == "" && resource.Kind == "Secret" {
				continue
			}
			kinds = append(kinds, api.ClusterKindInfo{
				Kind:       resource.Kind,
				Group:      gv.Group,
				Version:    gv.Version,
				Resource:   resource.Name,
				Namespaced: resource.Namespaced,
			})
		}
	}
	sort.Slice(kinds, func(i, j int) bool {
		if kinds[i].Group != kinds[j].Group {
			return kinds[i].Group < kinds[j].Group
		}
		return kinds[i].Resource < kinds[j].Resource
	})
	RespondWithJSON(w, http.StatusOK, api.NewResponse(kinds, "Successfully listed kinds", false))
}

// HandleQueryList handles GET /api/cluster/query/list
// Query params: kind (required), namespace, labelSelector, fieldSelector, limit.
func (h *ClusterQueryHandler) HandleQueryList(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-query").WithValues("operation", "list")

	gvk, namespaced, err := h.resolveKind(r.URL.Query().Get("kind"))
	if err != nil {
		w.RespondWithError(errors.NewBadRequestError(err.Error(), nil))
		return
	}
	if err := forbidSecrets(gvk); err != nil {
		w.RespondWithError(errors.NewForbiddenError(err.Error(), nil))
		return
	}

	limit := queryLimit(r, defaultQueryListLimit, maxQueryListLimit)
	opts := []client.ListOption{client.Limit(int64(limit))}
	if namespace := strings.TrimSpace(r.URL.Query().Get("namespace")); namespaced && namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if rawSelector := strings.TrimSpace(r.URL.Query().Get("labelSelector")); rawSelector != "" {
		selector, err := labels.Parse(rawSelector)
		if err != nil {
			w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("invalid labelSelector: %v", err), nil))
			return
		}
		opts = append(opts, client.MatchingLabelsSelector{Selector: selector})
	}
	if rawSelector := strings.TrimSpace(r.URL.Query().Get("fieldSelector")); rawSelector != "" {
		selector, err := fields.ParseSelector(rawSelector)
		if err != nil {
			w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("invalid fieldSelector: %v", err), nil))
			return
		}
		opts = append(opts, client.MatchingFieldsSelector{Selector: selector})
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := h.clusterReader().List(r.Context(), list, opts...); err != nil {
		if apierrors.IsForbidden(err) {
			w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
			return
		}
		log.Error(err, "Failed to list resources", "gvk", gvk.String())
		w.RespondWithError(errors.NewInternalServerError("Failed to list resources", err))
		return
	}

	kindName := strings.ToLower(gvk.Kind)
	items := make([]api.ClusterQueryItem, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		items = append(items, api.ClusterQueryItem{
			Kind:       gvk.Kind,
			APIVersion: gvk.GroupVersion().String(),
			Namespace:  item.GetNamespace(),
			Name:       item.GetName(),
			Status:     resourceStatusSummary(kindName, item),
			CreatedAt:  item.GetCreationTimestamp().Format("2006-01-02T15:04:05Z"),
		})
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(items, "Successfully listed resources", false))
}

// HandleQueryResource handles GET /api/cluster/query/resource
// Query params: kind (required), name (required), namespace.
func (h *ClusterQueryHandler) HandleQueryResource(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-query").WithValues("operation", "resource")

	gvk, namespaced, err := h.resolveKind(r.URL.Query().Get("kind"))
	if err != nil {
		w.RespondWithError(errors.NewBadRequestError(err.Error(), nil))
		return
	}
	if err := forbidSecrets(gvk); err != nil {
		w.RespondWithError(errors.NewForbiddenError(err.Error(), nil))
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		w.RespondWithError(errors.NewBadRequestError("name is required", nil))
		return
	}
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespaced && namespace == "" {
		w.RespondWithError(errors.NewBadRequestError("namespace is required for namespaced kinds", nil))
		return
	}
	if !namespaced {
		namespace = ""
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := h.clusterReader().Get(r.Context(), client.ObjectKey{Namespace: namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			w.RespondWithError(errors.NewNotFoundError("Resource not found", err))
			return
		}
		if apierrors.IsForbidden(err) {
			w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
			return
		}
		log.Error(err, "Failed to get resource", "gvk", gvk.String(), "name", name)
		w.RespondWithError(errors.NewInternalServerError("Failed to get resource", err))
		return
	}

	kindName := strings.ToLower(gvk.Kind)
	sanitizeResource(kindName, obj)
	yamlBytes, err := yaml.Marshal(obj.Object)
	if err != nil {
		yamlBytes = []byte(fmt.Sprintf("failed to render resource: %v", err))
	}
	yamlText := string(yamlBytes)
	if len(yamlText) > maxContextYAMLBytes {
		yamlText = yamlText[:maxContextYAMLBytes] + "\n... (truncated)"
	}

	response := api.ClusterQueryResource{
		Kind:       gvk.Kind,
		APIVersion: gvk.GroupVersion().String(),
		Namespace:  namespace,
		Name:       name,
		YAML:       yamlText,
		Events:     h.involvedObjectEvents(r, gvk.Kind, namespace, name, maxContextEvents),
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(response, "Successfully fetched resource", false))
}

// HandleQueryLogs handles GET /api/cluster/query/logs
// Query params: name (required), namespace (required), container, tailLines,
// previous, sinceSeconds.
func (h *ClusterQueryHandler) HandleQueryLogs(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-query").WithValues("operation", "logs")

	if h.Cluster == nil || h.Cluster.Clientset == nil {
		w.RespondWithError(errors.NewInternalServerError("Pod log access is not configured on this controller", nil))
		return
	}

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if name == "" || namespace == "" {
		w.RespondWithError(errors.NewBadRequestError("name and namespace are required", nil))
		return
	}

	tailLines := int64(queryLimit(r, defaultLogTailLines, maxLogTailLines))
	if rawTail := r.URL.Query().Get("tailLines"); rawTail != "" {
		if parsed, err := strconv.ParseInt(rawTail, 10, 64); err == nil && parsed > 0 {
			tailLines = min(parsed, int64(maxLogTailLines))
		}
	}
	logOptions := &corev1.PodLogOptions{
		Container:  strings.TrimSpace(r.URL.Query().Get("container")),
		TailLines:  &tailLines,
		Previous:   r.URL.Query().Get("previous") == "true",
		Timestamps: true,
	}
	if rawSince := r.URL.Query().Get("sinceSeconds"); rawSince != "" {
		if parsed, err := strconv.ParseInt(rawSince, 10, 64); err == nil && parsed > 0 {
			logOptions.SinceSeconds = &parsed
		}
	}
	limitBytes := int64(maxLogBytes)
	logOptions.LimitBytes = &limitBytes

	raw, err := h.Cluster.Clientset.CoreV1().Pods(namespace).GetLogs(name, logOptions).Do(r.Context()).Raw()
	if err != nil {
		if apierrors.IsNotFound(err) {
			w.RespondWithError(errors.NewNotFoundError("Pod (or container) not found", err))
			return
		}
		if apierrors.IsForbidden(err) {
			w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
			return
		}
		if apierrors.IsBadRequest(err) {
			w.RespondWithError(errors.NewBadRequestError(err.Error(), nil))
			return
		}
		log.Error(err, "Failed to fetch pod logs", "namespace", namespace, "pod", name)
		w.RespondWithError(errors.NewInternalServerError("Failed to fetch pod logs", err))
		return
	}

	response := api.ClusterPodLogs{
		Namespace: namespace,
		Name:      name,
		Container: logOptions.Container,
		Logs:      string(raw),
		Truncated: int64(len(raw)) >= limitBytes,
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(response, "Successfully fetched pod logs", false))
}

// HandleQueryEvents handles GET /api/cluster/query/events
// Query params: namespace, name, kind, limit. All optional; with no filters it
// returns the most recent events cluster-wide.
func (h *ClusterQueryHandler) HandleQueryEvents(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-query").WithValues("operation", "events")

	selectorSet := fields.Set{}
	if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
		selectorSet["involvedObject.name"] = name
	}
	if rawKind := strings.TrimSpace(r.URL.Query().Get("kind")); rawKind != "" {
		gvk, _, err := h.resolveKind(rawKind)
		if err != nil {
			w.RespondWithError(errors.NewBadRequestError(err.Error(), nil))
			return
		}
		selectorSet["involvedObject.kind"] = gvk.Kind
	}

	var opts []client.ListOption
	if len(selectorSet) > 0 {
		opts = append(opts, client.MatchingFieldsSelector{Selector: selectorSet.AsSelector()})
	}
	if namespace := strings.TrimSpace(r.URL.Query().Get("namespace")); namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}

	eventList := &corev1.EventList{}
	if err := h.clusterReader().List(r.Context(), eventList, opts...); err != nil {
		if apierrors.IsForbidden(err) {
			w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
			return
		}
		log.Error(err, "Failed to list events")
		w.RespondWithError(errors.NewInternalServerError("Failed to list events", err))
		return
	}

	events := eventList.Items
	sort.Slice(events, func(i, j int) bool {
		return eventTime(&events[i]).After(eventTime(&events[j]))
	})
	limit := queryLimit(r, defaultEventsLimit, maxEventsLimit)
	if len(events) > limit {
		events = events[:limit]
	}

	items := make([]api.ClusterEvent, 0, len(events))
	for _, event := range events {
		items = append(items, api.ClusterEvent{
			Namespace:       event.Namespace,
			LastSeen:        eventTime(&event).Format("2006-01-02T15:04:05Z"),
			Type:            event.Type,
			Reason:          event.Reason,
			InvolvedKind:    event.InvolvedObject.Kind,
			InvolvedName:    event.InvolvedObject.Name,
			SourceComponent: event.Source.Component,
			Message:         event.Message,
			Count:           event.Count,
		})
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(items, "Successfully listed events", false))
}

// involvedObjectEvents returns formatted recent events for one object.
func (h *ClusterQueryHandler) involvedObjectEvents(r *http.Request, kind, namespace, name string, limit int) []string {
	selector := fields.Set{
		"involvedObject.name": name,
		"involvedObject.kind": kind,
	}
	opts := []client.ListOption{client.MatchingFieldsSelector{Selector: selector.AsSelector()}}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	eventList := &corev1.EventList{}
	if err := h.clusterReader().List(r.Context(), eventList, opts...); err != nil {
		return nil
	}
	events := eventList.Items
	sort.Slice(events, func(i, j int) bool {
		return eventTime(&events[i]).After(eventTime(&events[j]))
	})
	if len(events) > limit {
		events = events[:limit]
	}
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s",
			eventTime(&event).Format("2006-01-02T15:04:05Z"), event.Type, event.Reason, event.Message))
	}
	return lines
}

// eventTime picks the most meaningful timestamp an Event carries.
func eventTime(event *corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	return event.CreationTimestamp.Time
}
