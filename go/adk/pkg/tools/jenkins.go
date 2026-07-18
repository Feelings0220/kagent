package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Builtin Jenkins tool names. Like the k8s_* tools they proxy through the
// kagent controller (KAGENT_URL), which holds the Jenkins credentials and
// masks credential-looking lines in console output.
const (
	JenkinsConsoleLogToolName = "jenkins_console_log"
	JenkinsJobInfoToolName    = "jenkins_job_info"
	JenkinsListBuildsToolName = "jenkins_list_builds"
)

// JenkinsToolNames lists the (read-only) Jenkins tools.
var JenkinsToolNames = []string{
	JenkinsConsoleLogToolName,
	JenkinsJobInfoToolName,
	JenkinsListBuildsToolName,
}

// IsJenkinsToolName reports whether name is one of the builtin Jenkins tools.
func IsJenkinsToolName(name string) bool {
	for _, known := range JenkinsToolNames {
		if name == known {
			return true
		}
	}
	return false
}

// buildNumberSegment matches a build reference in a Jenkins URL path:
// a number or the symbolic lastBuild/lastFailedBuild etc.
var buildNumberSegment = regexp.MustCompile(`^(\d+|last\w*Build)$`)

// parseJenkinsBuildURL extracts the job full name ("folder/app") and build
// reference from a Jenkins build URL like
// https://jenkins.example.com/job/folder/job/app/123/console.
// Returns an empty build when the URL points at a job rather than a build.
func parseJenkinsBuildURL(rawURL string) (job, build string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", fmt.Errorf("invalid Jenkins URL: %w", err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	var jobSegments []string
	for i := 0; i < len(segments); i++ {
		if segments[i] == "job" && i+1 < len(segments) {
			segment, decodeErr := url.PathUnescape(segments[i+1])
			if decodeErr != nil {
				segment = segments[i+1]
			}
			jobSegments = append(jobSegments, segment)
			i++
			continue
		}
		// The first non-job segment after at least one job segment is either
		// the build reference or trailing UI parts (console, pipeline-graph).
		if len(jobSegments) > 0 && buildNumberSegment.MatchString(segments[i]) {
			build = segments[i]
			break
		}
	}
	if len(jobSegments) == 0 {
		return "", "", fmt.Errorf("no /job/ segments found in URL %q", rawURL)
	}
	return strings.Join(jobSegments, "/"), build, nil
}

type jenkinsConsoleLogInput struct {
	URL   string `json:"url,omitempty"`
	Job   string `json:"job,omitempty"`
	Build string `json:"build,omitempty"`
}

type jenkinsJobInfoInput struct {
	URL string `json:"url,omitempty"`
	Job string `json:"job,omitempty"`
}

type jenkinsListBuildsInput struct {
	Job string `json:"job"`
}

// NewJenkinsTools builds the requested builtin Jenkins tools backed by the
// kagent controller at baseURL. Unknown names are ignored; returns nil when
// baseURL is empty.
func NewJenkinsTools(baseURL string, names []string, httpClient *http.Client) ([]tool.Tool, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" || len(names) == 0 {
		return nil, nil
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	apiClient := &k8sAPI{baseURL: baseURL, httpClient: httpClient}

	// jenkinsContext fetches the injectable context text for a job or build.
	jenkinsContext := func(ctx adkagent.ToolContext, kind, job, build string) (string, error) {
		query := url.Values{"kind": {kind}, "name": {build}}
		if kind == "job" {
			query.Set("name", job)
		} else {
			query.Set("job", job)
		}
		data, err := apiClient.call(ctx, http.MethodGet, "/api/context/jenkins/resources/context", query, nil)
		if err != nil {
			return err.Error(), nil
		}
		var context struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &context); err != nil {
			return "", fmt.Errorf("unexpected response: %w", err)
		}
		return context.Text, nil
	}

	builders := map[string]func() (tool.Tool, error){
		JenkinsConsoleLogToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: JenkinsConsoleLogToolName,
				Description: `Fetch a Jenkins build's result, cause, and console log tail (credentials masked).

- url: a Jenkins build/pipeline URL (e.g. https://jenkins/.../job/app/123/) — job and build are extracted automatically
- or job ("folder/app") plus build (number; defaults to "lastBuild")
Use this first when investigating a pipeline failure, then correlate errors with cluster events and pod logs.`,
			}, func(ctx adkagent.ToolContext, in jenkinsConsoleLogInput) (string, error) {
				job, build := strings.TrimSpace(in.Job), strings.TrimSpace(in.Build)
				if in.URL != "" {
					parsedJob, parsedBuild, err := parseJenkinsBuildURL(in.URL)
					if err != nil {
						return err.Error(), nil
					}
					job = parsedJob
					if parsedBuild != "" {
						build = parsedBuild
					}
				}
				if job == "" {
					return "Provide either url or job.", nil
				}
				if build == "" {
					build = "lastBuild"
				}
				return jenkinsContext(ctx, "build", job, build)
			})
		},
		JenkinsJobInfoToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: JenkinsJobInfoToolName,
				Description: `Get a Jenkins job's status, health, and recent builds.

- url: a Jenkins job URL — the job name is extracted automatically
- or job: the job full name ("folder/app")`,
			}, func(ctx adkagent.ToolContext, in jenkinsJobInfoInput) (string, error) {
				job := strings.TrimSpace(in.Job)
				if in.URL != "" {
					parsedJob, _, err := parseJenkinsBuildURL(in.URL)
					if err != nil {
						return err.Error(), nil
					}
					job = parsedJob
				}
				if job == "" {
					return "Provide either url or job.", nil
				}
				return jenkinsContext(ctx, "job", job, "")
			})
		},
		JenkinsListBuildsToolName: func() (tool.Tool, error) {
			return functiontool.New(functiontool.Config{
				Name: JenkinsListBuildsToolName,
				Description: `List a Jenkins job's recent builds (number, result, timestamp).

- job: the job full name ("folder/app")`,
			}, func(ctx adkagent.ToolContext, in jenkinsListBuildsInput) (string, error) {
				query := url.Values{"kind": {"build"}, "job": {strings.TrimSpace(in.Job)}}
				data, err := apiClient.call(ctx, http.MethodGet, "/api/context/jenkins/resources", query, nil)
				if err != nil {
					return err.Error(), nil
				}
				var items []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal(data, &items); err != nil {
					return "", fmt.Errorf("unexpected response: %w", err)
				}
				if len(items) == 0 {
					return "No builds found.", nil
				}
				var b strings.Builder
				for _, item := range items {
					fmt.Fprintf(&b, "#%s\t%s\n", item.Name, item.Status)
				}
				return b.String(), nil
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
