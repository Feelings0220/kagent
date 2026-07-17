package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	schemev1 "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kagent-dev/kagent/go/api/httpapi"
	"github.com/kagent-dev/kagent/go/core/internal/httpserver/handlers"
)

func newQueryHandler(t *testing.T, writeEnabled bool, objects ...client.Object) (*handlers.ClusterQueryHandler, client.Client) {
	t.Helper()
	kubeClient := fake.NewClientBuilder().
		WithScheme(schemev1.Scheme).
		WithObjects(objects...).
		Build()

	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.AddSpecific(
		schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
		schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
		schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingress"},
		meta.RESTScopeNamespace,
	)
	mapper.AddSpecific(
		schema.GroupVersionKind{Version: "v1", Kind: "Secret"},
		schema.GroupVersionResource{Version: "v1", Resource: "secrets"},
		schema.GroupVersionResource{Version: "v1", Resource: "secret"},
		meta.RESTScopeNamespace,
	)

	base := &handlers.Base{
		KubeClient: kubeClient,
		Cluster: &handlers.ClusterAccess{
			Client:       kubeClient,
			RESTMapper:   mapper,
			WriteEnabled: writeEnabled,
		},
	}
	return handlers.NewClusterQueryHandler(base), kubeClient
}

func makeDeployment(namespace, name string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
}

func TestHandleQueryList(t *testing.T) {
	handler, _ := newQueryHandler(t, false,
		makePod("default", "nginx-1", "Running"),
		makePod("other", "redis-1", "Pending"),
		makeDeployment("default", "web", 2),
	)

	tests := []struct {
		name           string
		url            string
		wantStatusCode int
		wantNames      []string
	}{
		{
			name:           "kubectl shorthand resolves",
			url:            "/api/cluster/query/list?kind=po",
			wantStatusCode: http.StatusOK,
			wantNames:      []string{"nginx-1", "redis-1"},
		},
		{
			name:           "deployments via alias",
			url:            "/api/cluster/query/list?kind=deploy",
			wantStatusCode: http.StatusOK,
			wantNames:      []string{"web"},
		},
		{
			name:           "namespace filter",
			url:            "/api/cluster/query/list?kind=pod&namespace=other",
			wantStatusCode: http.StatusOK,
			wantNames:      []string{"redis-1"},
		},
		{
			name:           "secrets are refused",
			url:            "/api/cluster/query/list?kind=secret",
			wantStatusCode: http.StatusForbidden,
		},
		{
			name:           "unknown kind is a bad request",
			url:            "/api/cluster/query/list?kind=doesnotexist",
			wantStatusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()
			handler.HandleQueryList(&testErrorResponseWriter{w}, req)

			require.Equal(t, tt.wantStatusCode, w.Code, w.Body.String())
			if tt.wantStatusCode != http.StatusOK {
				return
			}
			var response api.StandardResponse[[]api.ClusterQueryItem]
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			var names []string
			for _, item := range response.Data {
				names = append(names, item.Name)
			}
			require.ElementsMatch(t, tt.wantNames, names)
		})
	}
}

func TestHandleQueryResource(t *testing.T) {
	handler, _ := newQueryHandler(t, false, makePod("default", "nginx-1", "Running"))

	t.Run("returns sanitized yaml", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/query/resource?kind=pod&namespace=default&name=nginx-1", nil)
		w := httptest.NewRecorder()
		handler.HandleQueryResource(&testErrorResponseWriter{w}, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response api.StandardResponse[api.ClusterQueryResource]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Equal(t, "Pod", response.Data.Kind)
		require.Contains(t, response.Data.YAML, "nginx-1")
		require.NotContains(t, response.Data.YAML, "managedFields")
		require.NotContains(t, response.Data.YAML, "last-applied-configuration")
	})

	t.Run("missing namespace for namespaced kind", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/query/resource?kind=pod&name=nginx-1", nil)
		w := httptest.NewRecorder()
		handler.HandleQueryResource(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/query/resource?kind=pod&namespace=default&name=missing", nil)
		w := httptest.NewRecorder()
		handler.HandleQueryResource(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("secret is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/query/resource?kind=secret&namespace=default&name=creds", nil)
		w := httptest.NewRecorder()
		handler.HandleQueryResource(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestHandleQueryEvents(t *testing.T) {
	older := metav1.Time{Time: metav1.Now().Add(-3600e9)}
	newer := metav1.Now()
	handler, _ := newQueryHandler(t, false,
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "default", Name: "ev-old"},
			LastTimestamp:  older,
			Type:           "Warning",
			Reason:         "BackOff",
			Message:        "Back-off restarting failed container",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "nginx-1"},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "default", Name: "ev-new"},
			LastTimestamp:  newer,
			Type:           "Normal",
			Reason:         "Pulled",
			Message:        "Container image pulled",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "nginx-2"},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/cluster/query/events?namespace=default", nil)
	w := httptest.NewRecorder()
	handler.HandleQueryEvents(&testErrorResponseWriter{w}, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response api.StandardResponse[[]api.ClusterEvent]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Data, 2)
	// Newest first.
	require.Equal(t, "Pulled", response.Data[0].Reason)
	require.Equal(t, "BackOff", response.Data[1].Reason)
}

func TestHandleQueryLogsUnconfigured(t *testing.T) {
	handler, _ := newQueryHandler(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/cluster/query/logs?namespace=default&name=nginx-1", nil)
	w := httptest.NewRecorder()
	handler.HandleQueryLogs(&testErrorResponseWriter{w}, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestClusterWriteGuard(t *testing.T) {
	handler, _ := newQueryHandler(t, false)

	for _, tt := range []struct {
		name string
		call func(w handlers.ErrorResponseWriter, r *http.Request)
	}{
		{"apply", handler.HandleApply},
		{"delete", handler.HandleDelete},
		{"scale", handler.HandleScale},
		{"rollout-restart", handler.HandleRolloutRestart},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/cluster/"+tt.name, strings.NewReader(`{}`))
			w := httptest.NewRecorder()
			tt.call(&testErrorResponseWriter{w}, req)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "disabled")
		})
	}
}

func TestHandleScale(t *testing.T) {
	handler, kubeClient := newQueryHandler(t, true, makeDeployment("default", "web", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/cluster/scale",
		strings.NewReader(`{"kind":"deployment","name":"web","namespace":"default","replicas":3}`))
	w := httptest.NewRecorder()
	handler.HandleScale(&testErrorResponseWriter{w}, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	updated := &appsv1.Deployment{}
	require.NoError(t, kubeClient.Get(req.Context(), client.ObjectKey{Namespace: "default", Name: "web"}, updated))
	require.EqualValues(t, 3, *updated.Spec.Replicas)
}

func TestHandleDelete(t *testing.T) {
	handler, kubeClient := newQueryHandler(t, true, makePod("default", "nginx-1", "Running"))

	req := httptest.NewRequest(http.MethodPost, "/api/cluster/delete",
		strings.NewReader(`{"kind":"pod","name":"nginx-1","namespace":"default"}`))
	w := httptest.NewRecorder()
	handler.HandleDelete(&testErrorResponseWriter{w}, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	err := kubeClient.Get(req.Context(), client.ObjectKey{Namespace: "default", Name: "nginx-1"}, &corev1.Pod{})
	require.Error(t, err)
}

func TestHandleApplyValidation(t *testing.T) {
	handler, _ := newQueryHandler(t, true)

	t.Run("empty yaml", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/cluster/apply", strings.NewReader(`{"yaml":""}`))
		w := httptest.NewRecorder()
		handler.HandleApply(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("secret refused", func(t *testing.T) {
		secretYAML := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n  namespace: default\n"
		body, _ := json.Marshal(map[string]string{"yaml": secretYAML})
		req := httptest.NewRequest(http.MethodPost, "/api/cluster/apply", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		handler.HandleApply(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusForbidden, w.Code)
	})
}
