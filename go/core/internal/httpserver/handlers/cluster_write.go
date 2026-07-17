package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/kagent-dev/kagent/go/api/httpapi"
	"github.com/kagent-dev/kagent/go/core/internal/httpserver/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// clusterToolsFieldOwner marks server-side-apply ownership for agent-driven
// changes so they are attributable and conflict-detectable.
const clusterToolsFieldOwner = "kagent-cluster-tools"

// maxApplyBytes bounds a single apply payload.
const maxApplyBytes = 512 * 1024

// writeGuard rejects the request unless destructive cluster tools are
// explicitly enabled on this controller deployment.
func (h *ClusterQueryHandler) writeGuard(w ErrorResponseWriter) bool {
	if h.Cluster == nil || !h.Cluster.WriteEnabled {
		w.RespondWithError(errors.NewForbiddenError(
			"Destructive cluster tools are disabled on this kagent deployment. "+
				"Set clusterTools.write.enabled=true in the helm values to enable them.", nil))
		return false
	}
	return true
}

// clusterWriter returns the client used for destructive operations.
func (h *ClusterQueryHandler) clusterWriter() client.Client {
	return h.clusterReader()
}

type clusterApplyRequest struct {
	YAML string `json:"yaml"`
}

type clusterObjectRequest struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	// Replicas is used by scale only.
	Replicas *int32 `json:"replicas,omitempty"`
}

// HandleApply handles POST /api/cluster/apply — server-side apply of one or
// more YAML documents (\n--- separated).
func (h *ClusterQueryHandler) HandleApply(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-write").WithValues("operation", "apply")
	if !h.writeGuard(w) {
		return
	}

	var request clusterApplyRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxApplyBytes)).Decode(&request); err != nil {
		w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("invalid request body: %v", err), nil))
		return
	}
	if strings.TrimSpace(request.YAML) == "" {
		w.RespondWithError(errors.NewBadRequestError("yaml is required", nil))
		return
	}

	applied := make([]api.ClusterQueryItem, 0, 4)
	for _, document := range strings.Split(request.YAML, "\n---") {
		if strings.TrimSpace(document) == "" {
			continue
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(document), &obj.Object); err != nil {
			w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("invalid YAML document: %v", err), nil))
			return
		}
		gvk := obj.GroupVersionKind()
		if gvk.Kind == "" || gvk.Version == "" {
			w.RespondWithError(errors.NewBadRequestError("every document needs apiVersion and kind", nil))
			return
		}
		if err := forbidSecrets(gvk); err != nil {
			w.RespondWithError(errors.NewForbiddenError(err.Error(), nil))
			return
		}
		if obj.GetName() == "" {
			w.RespondWithError(errors.NewBadRequestError("every document needs metadata.name", nil))
			return
		}
		if err := h.clusterWriter().Patch(r.Context(), obj, client.Apply,
			client.FieldOwner(clusterToolsFieldOwner), client.ForceOwnership); err != nil {
			if apierrors.IsForbidden(err) {
				w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
				return
			}
			log.Error(err, "Failed to apply resource", "gvk", gvk.String(), "name", obj.GetName())
			w.RespondWithError(errors.NewInternalServerError(fmt.Sprintf("Failed to apply %s %s: %v", gvk.Kind, obj.GetName(), err), err))
			return
		}
		applied = append(applied, api.ClusterQueryItem{
			Kind:       gvk.Kind,
			APIVersion: gvk.GroupVersion().String(),
			Namespace:  obj.GetNamespace(),
			Name:       obj.GetName(),
		})
	}
	if len(applied) == 0 {
		w.RespondWithError(errors.NewBadRequestError("no YAML documents found", nil))
		return
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(applied, "Successfully applied resources", false))
}

// resolveObjectRequest parses and validates the common kind/name/namespace body.
func (h *ClusterQueryHandler) resolveObjectRequest(w ErrorResponseWriter, r *http.Request) (*clusterObjectRequest, *unstructured.Unstructured, bool) {
	var request clusterObjectRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("invalid request body: %v", err), nil))
		return nil, nil, false
	}
	gvk, namespaced, err := h.resolveKind(request.Kind)
	if err != nil {
		w.RespondWithError(errors.NewBadRequestError(err.Error(), nil))
		return nil, nil, false
	}
	if err := forbidSecrets(gvk); err != nil {
		w.RespondWithError(errors.NewForbiddenError(err.Error(), nil))
		return nil, nil, false
	}
	if strings.TrimSpace(request.Name) == "" {
		w.RespondWithError(errors.NewBadRequestError("name is required", nil))
		return nil, nil, false
	}
	if namespaced && strings.TrimSpace(request.Namespace) == "" {
		w.RespondWithError(errors.NewBadRequestError("namespace is required for namespaced kinds", nil))
		return nil, nil, false
	}
	if !namespaced {
		request.Namespace = ""
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetNamespace(request.Namespace)
	obj.SetName(request.Name)
	return &request, obj, true
}

// HandleDelete handles POST /api/cluster/delete.
func (h *ClusterQueryHandler) HandleDelete(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-write").WithValues("operation", "delete")
	if !h.writeGuard(w) {
		return
	}
	request, obj, ok := h.resolveObjectRequest(w, r)
	if !ok {
		return
	}
	if err := h.clusterWriter().Delete(r.Context(), obj); err != nil {
		if apierrors.IsNotFound(err) {
			w.RespondWithError(errors.NewNotFoundError("Resource not found", err))
			return
		}
		if apierrors.IsForbidden(err) {
			w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
			return
		}
		log.Error(err, "Failed to delete resource", "kind", request.Kind, "name", request.Name)
		w.RespondWithError(errors.NewInternalServerError("Failed to delete resource", err))
		return
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(
		fmt.Sprintf("Deleted %s %s", request.Kind, request.Name), "Successfully deleted resource", false))
}

// HandleScale handles POST /api/cluster/scale — patches spec.replicas.
func (h *ClusterQueryHandler) HandleScale(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-write").WithValues("operation", "scale")
	if !h.writeGuard(w) {
		return
	}
	request, obj, ok := h.resolveObjectRequest(w, r)
	if !ok {
		return
	}
	if request.Replicas == nil || *request.Replicas < 0 {
		w.RespondWithError(errors.NewBadRequestError("replicas (>= 0) is required", nil))
		return
	}
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, *request.Replicas))
	if err := h.clusterWriter().Patch(r.Context(), obj, client.RawPatch(types.MergePatchType, patch)); err != nil {
		if apierrors.IsNotFound(err) {
			w.RespondWithError(errors.NewNotFoundError("Resource not found", err))
			return
		}
		if apierrors.IsForbidden(err) {
			w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
			return
		}
		log.Error(err, "Failed to scale resource", "kind", request.Kind, "name", request.Name)
		w.RespondWithError(errors.NewInternalServerError("Failed to scale resource", err))
		return
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(
		fmt.Sprintf("Scaled %s %s to %d replicas", request.Kind, request.Name, *request.Replicas),
		"Successfully scaled resource", false))
}

// HandleRolloutRestart handles POST /api/cluster/rollout-restart — bumps the
// pod template restartedAt annotation like kubectl rollout restart.
func (h *ClusterQueryHandler) HandleRolloutRestart(w ErrorResponseWriter, r *http.Request) {
	log := ctrllog.FromContext(r.Context()).WithName("cluster-write").WithValues("operation", "rollout-restart")
	if !h.writeGuard(w) {
		return
	}
	request, obj, ok := h.resolveObjectRequest(w, r)
	if !ok {
		return
	}
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().Format(time.RFC3339)))
	if err := h.clusterWriter().Patch(r.Context(), obj, client.RawPatch(types.MergePatchType, patch)); err != nil {
		if apierrors.IsNotFound(err) {
			w.RespondWithError(errors.NewNotFoundError("Resource not found", err))
			return
		}
		if apierrors.IsForbidden(err) {
			w.RespondWithError(errors.NewForbiddenError("Not permitted by controller RBAC", err))
			return
		}
		log.Error(err, "Failed to restart rollout", "kind", request.Kind, "name", request.Name)
		w.RespondWithError(errors.NewInternalServerError("Failed to restart rollout", err))
		return
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(
		fmt.Sprintf("Restarted rollout of %s %s", request.Kind, request.Name),
		"Successfully restarted rollout", false))
}
