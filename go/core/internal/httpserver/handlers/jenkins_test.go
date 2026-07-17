package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/kagent-dev/kagent/go/api/httpapi"
	"github.com/kagent-dev/kagent/go/core/internal/httpserver/handlers"
)

// newJenkinsServer fakes the subset of the Jenkins REST API the provider uses.
func newJenkinsServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jobs":[
			{"name":"standalone","fullName":"standalone","color":"blue"},
			{"name":"folder","fullName":"folder","color":"","jobs":[
				{"name":"app","fullName":"folder/app","color":"red"}
			]}
		]}`))
	})
	mux.HandleFunc("/job/folder/job/app/api/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fullName":"folder/app","description":"builds the app","color":"red","inQueue":false,
			"healthReport":[{"description":"Build stability: 1 of 5 failed","score":80}],
			"builds":[{"number":42,"result":"FAILURE","building":false,"timestamp":1700000000000,"duration":65000},
			          {"number":41,"result":"SUCCESS","building":false,"timestamp":1699990000000,"duration":61000}]}`))
	})
	mux.HandleFunc("/job/folder/job/app/42/api/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":42,"result":"FAILURE","building":false,"timestamp":1700000000000,"duration":65000,
			"actions":[{"causes":[{"shortDescription":"Started by user admin"}]},{}]}`))
	})
	mux.HandleFunc("/job/folder/job/app/42/consoleText", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("step 1 ok\nexport API_KEY=super-secret-value\nERROR: tests failed\n"))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func newJenkinsHandler(t *testing.T, url string) *handlers.JenkinsHandler {
	t.Helper()
	return handlers.NewJenkinsHandlerWithConfig(
		&handlers.Base{},
		handlers.JenkinsConfig{URL: url, Username: "admin", APIToken: "token"},
		http.DefaultClient,
	)
}

func TestJenkinsHandleListProviders(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		wantProviders []string
	}{
		{name: "jenkins configured", url: "http://jenkins.example", wantProviders: []string{"kubernetes", "jenkins"}},
		{name: "jenkins not configured", url: "", wantProviders: []string{"kubernetes"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newJenkinsHandler(t, tt.url)
			req := httptest.NewRequest(http.MethodGet, "/api/context/providers", nil)
			w := httptest.NewRecorder()

			handler.HandleListProviders(&testErrorResponseWriter{w}, req)

			require.Equal(t, http.StatusOK, w.Code)
			var response api.StandardResponse[[]string]
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			require.Equal(t, tt.wantProviders, response.Data)
		})
	}
}

func TestJenkinsHandleListResources(t *testing.T) {
	server := newJenkinsServer(t)
	handler := newJenkinsHandler(t, server.URL)

	t.Run("lists jobs flattened and sorted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources?kind=job", nil)
		w := httptest.NewRecorder()
		handler.HandleListJenkinsResources(&testErrorResponseWriter{w}, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response api.StandardResponse[[]api.ClusterResourceItem]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Len(t, response.Data, 2)
		require.Equal(t, "folder/app", response.Data[0].Name)
		require.Equal(t, "Failed", response.Data[0].Status)
		require.Equal(t, "standalone", response.Data[1].Name)
		require.Equal(t, "Success", response.Data[1].Status)
	})

	t.Run("query filters jobs", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources?kind=job&query=folder", nil)
		w := httptest.NewRecorder()
		handler.HandleListJenkinsResources(&testErrorResponseWriter{w}, req)

		var response api.StandardResponse[[]api.ClusterResourceItem]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Len(t, response.Data, 1)
		require.Equal(t, "folder/app", response.Data[0].Name)
	})

	t.Run("lists builds for a job", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources?kind=build&job=folder/app", nil)
		w := httptest.NewRecorder()
		handler.HandleListJenkinsResources(&testErrorResponseWriter{w}, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response api.StandardResponse[[]api.ClusterResourceItem]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Len(t, response.Data, 2)
		require.Equal(t, "42", response.Data[0].Name)
		require.Equal(t, "folder/app", response.Data[0].Scope)
		require.Equal(t, "FAILURE", response.Data[0].Status)
	})

	t.Run("build kind requires job", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources?kind=build", nil)
		w := httptest.NewRecorder()
		handler.HandleListJenkinsResources(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("unconfigured provider rejected", func(t *testing.T) {
		unconfigured := newJenkinsHandler(t, "")
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources?kind=job", nil)
		w := httptest.NewRecorder()
		unconfigured.HandleListJenkinsResources(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestJenkinsHandleGetResourceContext(t *testing.T) {
	server := newJenkinsServer(t)
	handler := newJenkinsHandler(t, server.URL)

	t.Run("job context includes health and recent builds", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources/context?kind=job&name=folder/app", nil)
		w := httptest.NewRecorder()
		handler.HandleGetJenkinsResourceContext(&testErrorResponseWriter{w}, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response api.StandardResponse[api.ClusterResourceContext]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		require.Equal(t, "jenkins", response.Data.Provider)
		require.Contains(t, response.Data.Text, "=== Jenkins context: job folder/app ===")
		require.Contains(t, response.Data.Text, "Build stability")
		require.Contains(t, response.Data.Text, "#42 FAILURE")
	})

	t.Run("build context includes masked console tail", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources/context?kind=build&job=folder/app&name=42", nil)
		w := httptest.NewRecorder()
		handler.HandleGetJenkinsResourceContext(&testErrorResponseWriter{w}, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response api.StandardResponse[api.ClusterResourceContext]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		text := response.Data.Text
		require.Contains(t, text, "=== Jenkins context: build folder/app #42 ===")
		require.Contains(t, text, "Result: FAILURE")
		require.Contains(t, text, "Started by user admin")
		require.Contains(t, text, "ERROR: tests failed")
		require.Contains(t, text, "API_KEY=***")
		require.False(t, strings.Contains(text, "super-secret-value"), "credentials must be masked")
	})

	t.Run("missing build is 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/context/jenkins/resources/context?kind=build&job=folder/app&name=999", nil)
		w := httptest.NewRecorder()
		handler.HandleGetJenkinsResourceContext(&testErrorResponseWriter{w}, req)
		require.Equal(t, http.StatusNotFound, w.Code)
	})
}
