package tools

import "testing"

func TestParseJenkinsBuildURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantJob   string
		wantBuild string
		wantErr   bool
	}{
		{
			name:      "simple build url",
			url:       "https://jenkins.example.com/job/deploy/123/",
			wantJob:   "deploy",
			wantBuild: "123",
		},
		{
			name:      "folder job with console suffix",
			url:       "https://jenkins.example.com/job/team/job/app/456/console",
			wantJob:   "team/app",
			wantBuild: "456",
		},
		{
			name:      "blue ocean style symbolic build",
			url:       "https://jenkins.example.com/job/app/lastFailedBuild/",
			wantJob:   "app",
			wantBuild: "lastFailedBuild",
		},
		{
			name:    "job url without build",
			url:     "https://jenkins.example.com/job/team/job/app/",
			wantJob: "team/app",
		},
		{
			name:      "escaped job segment",
			url:       "https://jenkins.example.com/job/my%20app/7/",
			wantJob:   "my app",
			wantBuild: "7",
		},
		{
			name:    "no job segments",
			url:     "https://jenkins.example.com/view/all/",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, build, err := parseJenkinsBuildURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseJenkinsBuildURL() error = %v, wantErr %v", err, tt.wantErr)
			}
			if job != tt.wantJob {
				t.Errorf("job = %q, want %q", job, tt.wantJob)
			}
			if build != tt.wantBuild {
				t.Errorf("build = %q, want %q", build, tt.wantBuild)
			}
		})
	}
}

func TestIsJenkinsToolName(t *testing.T) {
	if !IsJenkinsToolName(JenkinsConsoleLogToolName) {
		t.Error("expected jenkins tool name to be recognized")
	}
	if IsJenkinsToolName("k8s_events") || IsJenkinsToolName("") {
		t.Error("expected non-jenkins names to be rejected")
	}
}
