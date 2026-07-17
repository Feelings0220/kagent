package handlers

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	api "github.com/kagent-dev/kagent/go/api/httpapi"
	"github.com/kagent-dev/kagent/go/core/internal/httpserver/errors"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	contextProviderJenkins = "jenkins"

	// maxJenkinsLogBytes caps the console log tail injected as context.
	maxJenkinsLogBytes = 8 * 1024
	// maxJenkinsBuilds caps the builds listed per job.
	maxJenkinsBuilds = 20
	// jenkinsRequestTimeout bounds each upstream Jenkins call.
	jenkinsRequestTimeout = 15 * time.Second
)

// secretishLine masks credential-looking values in injected console logs.
var secretishLine = regexp.MustCompile(`(?i)(password|passwd|token|secret|api[_-]?key|authorization)(\s*[=:]\s*)\S+`)

// JenkinsConfig holds the connection settings for the Jenkins context
// provider. Populated from env (JENKINS_URL, JENKINS_USERNAME,
// JENKINS_API_TOKEN), which the helm chart wires from a Secret.
type JenkinsConfig struct {
	URL      string
	Username string
	APIToken string
}

// JenkinsConfigFromEnv reads the provider configuration from the environment.
func JenkinsConfigFromEnv() JenkinsConfig {
	return JenkinsConfig{
		URL:      strings.TrimRight(strings.TrimSpace(os.Getenv("JENKINS_URL")), "/"),
		Username: os.Getenv("JENKINS_USERNAME"),
		APIToken: os.Getenv("JENKINS_API_TOKEN"),
	}
}

// Enabled reports whether the Jenkins provider is configured.
func (c JenkinsConfig) Enabled() bool {
	return c.URL != ""
}

// JenkinsHandler serves Jenkins job/build listings and injectable context for
// the chat @-mention feature, plus the provider discovery endpoint.
type JenkinsHandler struct {
	*Base
	config     JenkinsConfig
	httpClient *http.Client
}

// NewJenkinsHandler creates a JenkinsHandler configured from the environment.
func NewJenkinsHandler(base *Base) *JenkinsHandler {
	return NewJenkinsHandlerWithConfig(base, JenkinsConfigFromEnv(), &http.Client{Timeout: jenkinsRequestTimeout})
}

// NewJenkinsHandlerWithConfig creates a JenkinsHandler with explicit
// configuration (used by tests).
func NewJenkinsHandlerWithConfig(base *Base, config JenkinsConfig, httpClient *http.Client) *JenkinsHandler {
	return &JenkinsHandler{Base: base, config: config, httpClient: httpClient}
}

// HandleListProviders handles GET /api/context/providers.
func (h *JenkinsHandler) HandleListProviders(w ErrorResponseWriter, r *http.Request) {
	providers := []string{contextProviderKubernetes}
	if h.config.Enabled() {
		providers = append(providers, contextProviderJenkins)
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(providers, "Successfully listed context providers", false))
}

type jenkinsJob struct {
	Name     string       `json:"name"`
	FullName string       `json:"fullName"`
	Color    string       `json:"color"`
	Jobs     []jenkinsJob `json:"jobs"`
}

type jenkinsBuild struct {
	Number    int    `json:"number"`
	Result    string `json:"result"`
	Building  bool   `json:"building"`
	Timestamp int64  `json:"timestamp"`
	Duration  int64  `json:"duration"`
}

// HandleListJenkinsResources handles GET /api/context/jenkins/resources
// Query params: kind (job|build), job (for kind=build), query, limit.
func (h *JenkinsHandler) HandleListJenkinsResources(w ErrorResponseWriter, r *http.Request) {
	if !h.config.Enabled() {
		w.RespondWithError(errors.NewBadRequestError("Jenkins context provider is not configured", nil))
		return
	}

	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	limit := defaultResourceListLimit
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = min(parsed, maxResourceListLimit)
		}
	}

	switch kind {
	case "job":
		jobs, err := h.fetchJobs(r)
		if err != nil {
			h.respondUpstreamError(w, r, err)
			return
		}
		items := make([]api.ClusterResourceItem, 0, len(jobs))
		for _, job := range jobs {
			if query != "" && !strings.Contains(strings.ToLower(job.FullName), query) {
				continue
			}
			items = append(items, api.ClusterResourceItem{
				Kind:   "job",
				Name:   job.FullName,
				Status: jenkinsColorToStatus(job.Color),
			})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		if len(items) > limit {
			items = items[:limit]
		}
		RespondWithJSON(w, http.StatusOK, api.NewResponse(items, "Successfully listed Jenkins jobs", false))

	case "build":
		jobName := strings.TrimSpace(r.URL.Query().Get("job"))
		if jobName == "" {
			w.RespondWithError(errors.NewBadRequestError("job is required for kind=build", nil))
			return
		}
		builds, err := h.fetchBuilds(r, jobName)
		if err != nil {
			h.respondUpstreamError(w, r, err)
			return
		}
		items := make([]api.ClusterResourceItem, 0, len(builds))
		for _, build := range builds {
			status := build.Result
			if build.Building {
				status = "BUILDING"
			}
			items = append(items, api.ClusterResourceItem{
				Kind:   "build",
				Scope:  jobName,
				Name:   strconv.Itoa(build.Number),
				Status: status,
			})
		}
		if len(items) > limit {
			items = items[:limit]
		}
		RespondWithJSON(w, http.StatusOK, api.NewResponse(items, "Successfully listed Jenkins builds", false))

	default:
		w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("Unsupported kind %q (job|build)", kind), nil))
	}
}

// HandleGetJenkinsResourceContext handles GET /api/context/jenkins/resources/context
// Query params: kind (job|build), name, job (for kind=build).
func (h *JenkinsHandler) HandleGetJenkinsResourceContext(w ErrorResponseWriter, r *http.Request) {
	if !h.config.Enabled() {
		w.RespondWithError(errors.NewBadRequestError("Jenkins context provider is not configured", nil))
		return
	}

	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		w.RespondWithError(errors.NewBadRequestError("name is required", nil))
		return
	}

	var (
		text  string
		scope string
		err   error
	)
	switch kind {
	case "job":
		text, err = h.buildJobContext(r, name)
	case "build":
		scope = strings.TrimSpace(r.URL.Query().Get("job"))
		if scope == "" {
			w.RespondWithError(errors.NewBadRequestError("job is required for kind=build", nil))
			return
		}
		text, err = h.buildBuildContext(r, scope, name)
	default:
		w.RespondWithError(errors.NewBadRequestError(fmt.Sprintf("Unsupported kind %q (job|build)", kind), nil))
		return
	}
	if err != nil {
		h.respondUpstreamError(w, r, err)
		return
	}

	response := api.ClusterResourceContext{
		Provider: contextProviderJenkins,
		Kind:     kind,
		Scope:    scope,
		Name:     name,
		Text:     text,
	}
	RespondWithJSON(w, http.StatusOK, api.NewResponse(response, "Successfully built Jenkins context", false))
}

// jenkinsGet performs an authenticated GET against the Jenkins API.
func (h *JenkinsHandler) jenkinsGet(r *http.Request, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, h.config.URL+path, nil)
	if err != nil {
		return nil, err
	}
	if h.config.Username != "" {
		req.SetBasicAuth(h.config.Username, h.config.APIToken)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.NewNotFoundError("Jenkins resource not found", nil)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jenkins returned status %d", resp.StatusCode)
	}
	// Bound reads: console logs can be huge; JSON responses are small anyway.
	return io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
}

// jobPath converts a job full name ("folder/app") to the Jenkins URL path
// ("/job/folder/job/app"), escaping each segment.
func jobPath(fullName string) string {
	segments := strings.Split(fullName, "/")
	var b strings.Builder
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		b.WriteString("/job/")
		b.WriteString(url.PathEscape(segment))
	}
	return b.String()
}

// fetchJobs lists jobs, flattening one folder level.
func (h *JenkinsHandler) fetchJobs(r *http.Request) ([]jenkinsJob, error) {
	body, err := h.jenkinsGet(r, "/api/json?tree=jobs[name,fullName,color,jobs[name,fullName,color]]")
	if err != nil {
		return nil, err
	}
	var root struct {
		Jobs []jenkinsJob `json:"jobs"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("failed to parse Jenkins response: %w", err)
	}

	var flat []jenkinsJob
	for _, job := range root.Jobs {
		if len(job.Jobs) > 0 {
			flat = append(flat, job.Jobs...)
			continue
		}
		flat = append(flat, job)
	}
	for i := range flat {
		if flat[i].FullName == "" {
			flat[i].FullName = flat[i].Name
		}
	}
	return flat, nil
}

func (h *JenkinsHandler) fetchBuilds(r *http.Request, jobName string) ([]jenkinsBuild, error) {
	path := fmt.Sprintf("%s/api/json?tree=builds[number,result,building,timestamp,duration]{0,%d}", jobPath(jobName), maxJenkinsBuilds)
	body, err := h.jenkinsGet(r, path)
	if err != nil {
		return nil, err
	}
	var job struct {
		Builds []jenkinsBuild `json:"builds"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, fmt.Errorf("failed to parse Jenkins response: %w", err)
	}
	return job.Builds, nil
}

func (h *JenkinsHandler) buildJobContext(r *http.Request, jobName string) (string, error) {
	body, err := h.jenkinsGet(r, jobPath(jobName)+"/api/json?tree=fullName,description,inQueue,color,healthReport[description,score],builds[number,result,building,timestamp,duration]{0,10}")
	if err != nil {
		return "", err
	}
	var job struct {
		FullName     string `json:"fullName"`
		Description  string `json:"description"`
		InQueue      bool   `json:"inQueue"`
		Color        string `json:"color"`
		HealthReport []struct {
			Description string `json:"description"`
			Score       int    `json:"score"`
		} `json:"healthReport"`
		Builds []jenkinsBuild `json:"builds"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return "", fmt.Errorf("failed to parse Jenkins response: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "=== Jenkins context: job %s ===\n", jobName)
	fmt.Fprintf(&b, "Status: %s | In queue: %t\n", jenkinsColorToStatus(job.Color), job.InQueue)
	if job.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", job.Description)
	}
	for _, health := range job.HealthReport {
		fmt.Fprintf(&b, "Health: %s (score %d)\n", health.Description, health.Score)
	}
	if len(job.Builds) > 0 {
		b.WriteString("\nRecent builds (newest first):\n")
		for _, build := range job.Builds {
			b.WriteString(formatJenkinsBuildLine(build))
		}
	}
	return b.String(), nil
}

func (h *JenkinsHandler) buildBuildContext(r *http.Request, jobName, buildNumber string) (string, error) {
	buildBase := fmt.Sprintf("%s/%s", jobPath(jobName), url.PathEscape(buildNumber))
	body, err := h.jenkinsGet(r, buildBase+"/api/json?tree=number,result,building,timestamp,duration,actions[causes[shortDescription]]")
	if err != nil {
		return "", err
	}
	var build struct {
		Number    int    `json:"number"`
		Result    string `json:"result"`
		Building  bool   `json:"building"`
		Timestamp int64  `json:"timestamp"`
		Duration  int64  `json:"duration"`
		Actions   []struct {
			Causes []struct {
				ShortDescription string `json:"shortDescription"`
			} `json:"causes"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(body, &build); err != nil {
		return "", fmt.Errorf("failed to parse Jenkins response: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "=== Jenkins context: build %s #%s ===\n", jobName, buildNumber)
	result := build.Result
	if build.Building {
		result = "BUILDING"
	}
	fmt.Fprintf(&b, "Result: %s | Started: %s | Duration: %s\n",
		result,
		time.UnixMilli(build.Timestamp).UTC().Format(time.RFC3339),
		(time.Duration(build.Duration) * time.Millisecond).Round(time.Second))
	for _, action := range build.Actions {
		for _, cause := range action.Causes {
			fmt.Fprintf(&b, "Cause: %s\n", cause.ShortDescription)
		}
	}

	// Console log tail (best-effort; masked for credential-looking values).
	if logBody, err := h.jenkinsGet(r, buildBase+"/consoleText"); err == nil && len(logBody) > 0 {
		logText := string(logBody)
		if len(logText) > maxJenkinsLogBytes {
			logText = "... (log truncated)\n" + logText[len(logText)-maxJenkinsLogBytes:]
		}
		logText = secretishLine.ReplaceAllString(logText, "$1$2***")
		b.WriteString("\n--- Console log (tail) ---\n")
		b.WriteString(logText)
		if !strings.HasSuffix(logText, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func formatJenkinsBuildLine(build jenkinsBuild) string {
	result := build.Result
	if build.Building {
		result = "BUILDING"
	}
	if result == "" {
		result = "UNKNOWN"
	}
	return fmt.Sprintf("- #%d %s at %s (took %s)\n",
		build.Number,
		result,
		time.UnixMilli(build.Timestamp).UTC().Format(time.RFC3339),
		(time.Duration(build.Duration) * time.Millisecond).Round(time.Second))
}

// jenkinsColorToStatus maps Jenkins "color" values to readable statuses.
func jenkinsColorToStatus(color string) string {
	building := strings.HasSuffix(color, "_anime")
	base := strings.TrimSuffix(color, "_anime")
	var status string
	switch base {
	case "blue":
		status = "Success"
	case "red":
		status = "Failed"
	case "yellow":
		status = "Unstable"
	case "aborted":
		status = "Aborted"
	case "disabled":
		status = "Disabled"
	case "notbuilt":
		status = "NotBuilt"
	default:
		status = base
	}
	if building {
		status += " (building)"
	}
	return status
}

// respondUpstreamError maps upstream failures onto API errors.
func (h *JenkinsHandler) respondUpstreamError(w ErrorResponseWriter, r *http.Request, err error) {
	log := ctrllog.FromContext(r.Context()).WithName("jenkins-handler")
	var apiErr *errors.APIError
	if stderrors.As(err, &apiErr) {
		w.RespondWithError(err)
		return
	}
	log.Error(err, "Jenkins request failed")
	w.RespondWithError(errors.NewInternalServerError("Jenkins request failed", err))
}
