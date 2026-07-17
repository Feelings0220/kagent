package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schemev1 "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/kagent-dev/kagent/go/api/httpapi"
	"github.com/kagent-dev/kagent/go/core/internal/httpserver/handlers"
)

func makePod(namespace, name, phase string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			ManagedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationApply,
					APIVersion: "v1",
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{}}`)},
				},
			},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "{...}",
				"keep-me": "yes",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPhase(phase)},
	}
}

func TestHandleListResources(t *testing.T) {
	scheme := schemev1.Scheme
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			makePod("default", "nginx-1", "Running"),
			makePod("default", "nginx-2", "Pending"),
			makePod("other", "redis-1", "Running"),
		).
		Build()
	handler := handlers.NewResourcesHandler(&handlers.Base{KubeClient: kubeClient})

	tests := []struct {
		name           string
		url            string
		wantStatusCode int
		wantNames      []string
	}{
		{
			name:           "lists pods across namespaces",
			url:            "/api/cluster/resources?kind=pod",
			wantStatusCode: http.StatusOK,
			wantNames:      []string{"nginx-1", "nginx-2", "redis-1"},
		},
		{
			name:           "namespace filter",
			url:            "/api/cluster/resources?kind=pod&namespace=other",
			wantStatusCode: http.StatusOK,
			wantNames:      []string{"redis-1"},
		},
		{
			name:           "query filter",
			url:            "/api/cluster/resources?kind=pod&query=NGINX",
			wantStatusCode: http.StatusOK,
			wantNames:      []string{"nginx-1", "nginx-2"},
		},
		{
			name:           "limit applies",
			url:            "/api/cluster/resources?kind=pod&limit=1",
			wantStatusCode: http.StatusOK,
			wantNames:      []string{"nginx-1"},
		},
		{
			name:           "unsupported kind rejected",
			url:            "/api/cluster/resources?kind=secret",
			wantStatusCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()

			handler.HandleListResources(&testErrorResponseWriter{w}, req)

			require.Equal(t, tt.wantStatusCode, w.Code, w.Body.String())
			if tt.wantStatusCode != http.StatusOK {
				return
			}

			var response api.StandardResponse[[]api.ClusterResourceItem]
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			var names []string
			for _, item := range response.Data {
				names = append(names, item.Name)
			}
			require.Equal(t, tt.wantNames, names)
		})
	}

	t.Run("pod status summary included", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/resources?kind=pod&query=nginx-1", nil)
		w := httptest.NewRecorder()
		handler.HandleListResources(&testErrorResponseWriter{w}, req)

		var response api.StandardResponse[[]api.ClusterResourceItem]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Len(t, response.Data, 1)
		require.Equal(t, "Running", response.Data[0].Status)
	})
}

func TestHandleGetResourceContext(t *testing.T) {
	scheme := schemev1.Scheme
	longValue := strings.Repeat("x", 5000)
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			makePod("default", "nginx-1", "Running"),
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "big-config"},
				Data:       map[string]string{"blob": longValue, "small": "ok"},
			},
		).
		Build()
	handler := handlers.NewResourcesHandler(&handlers.Base{KubeClient: kubeClient})

	t.Run("returns sanitized yaml with header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/resources/context?kind=pod&namespace=default&name=nginx-1", nil)
		w := httptest.NewRecorder()
		handler.HandleGetResourceContext(&testErrorResponseWriter{w}, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response api.StandardResponse[api.ClusterResourceContext]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))

		require.Equal(t, "kubernetes", response.Data.Provider)
		require.Equal(t, "pod", response.Data.Kind)
		require.Contains(t, response.Data.Text, "=== Kubernetes context: Pod default/nginx-1 ===")
		require.Contains(t, response.Data.Text, "keep-me")
		require.NotContains(t, response.Data.Text, "managedFields")
		require.NotContains(t, response.Data.Text, "last-applied-configuration")
	})

	t.Run("configmap values truncated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/resources/context?kind=configmap&namespace=default&name=big-config", nil)
		w := httptest.NewRecorder()
		handler.HandleGetResourceContext(&testErrorResponseWriter{w}, req)

		require.Equal(t, http.StatusOK, w.Code)
		var response api.StandardResponse[api.ClusterResourceContext]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		// yaml wraps long scalars, so match the marker loosely.
		require.Contains(t, response.Data.Text, "(truncated)")
		require.Contains(t, response.Data.Text, "small: ok")
		require.NotContains(t, response.Data.Text, longValue)
	})

	t.Run("missing resource is 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/resources/context?kind=pod&namespace=default&name=ghost", nil)
		w := httptest.NewRecorder()
		handler.HandleGetResourceContext(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("namespaced kind requires namespace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/cluster/resources/context?kind=pod&name=nginx-1", nil)
		w := httptest.NewRecorder()
		handler.HandleGetResourceContext(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}
