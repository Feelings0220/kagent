package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveReadPath_AllowsSymlinkedSkillsDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	skillsDir := t.TempDir()
	skillFile := filepath.Join(skillsDir, "script.py")
	if err := os.WriteFile(skillFile, []byte("print('ok')\n"), 0644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	sessionID := fmt.Sprintf("%s-read", t.Name())
	resolved, err := resolveReadPath(sessionID, skillsDir, "skills/script.py")
	if err != nil {
		t.Fatalf("resolveReadPath() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(skillFile)
	if err != nil {
		t.Fatalf("EvalSymlinks(skillFile) error = %v", err)
	}
	if resolved != want {
		t.Fatalf("resolveReadPath() = %q, want %q", resolved, want)
	}
}

func TestResolveWritePath_BlocksSkillsSymlink(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	skillsDir := t.TempDir()
	sessionID := fmt.Sprintf("%s-write", t.Name())
	_, err := resolveWritePath(sessionID, skillsDir, "skills/new-file.txt")
	if err == nil {
		t.Fatal("expected write through skills symlink to be rejected")
	}
	if !strings.Contains(err.Error(), "outside the writable session directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewSkillsTools_ReturnsExpectedToolSet(t *testing.T) {
	skillsDir := t.TempDir()
	t.Setenv("KAGENT_SRT_SETTINGS_PATH", filepath.Join(t.TempDir(), "srt-settings.json"))
	skillDir := filepath.Join(skillsDir, "demo")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: demo
description: Demo skill.
---
`), 0644); err != nil {
		t.Fatalf("failed to write skill metadata: %v", err)
	}

	tools, err := NewSkillsTools(skillsDir)
	if err != nil {
		t.Fatalf("NewSkillsTools() error = %v", err)
	}

	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.Name()] = true
	}

	for _, name := range []string{"skills", "read_file", "write_file", "edit_file", "bash"} {
		if !got[name] {
			t.Errorf("expected tool %q to be present", name)
		}
	}
}

func TestNewWorkspaceTools_FiltersAndCreatesDirectory(t *testing.T) {
	t.Setenv("KAGENT_SRT_SETTINGS_PATH", filepath.Join(t.TempDir(), "srt-settings.json"))
	workspaceDir := filepath.Join(t.TempDir(), "not-yet-created")

	tools, err := NewWorkspaceTools(workspaceDir, []string{"bash", "read_file", "unknown_tool"})
	if err != nil {
		t.Fatalf("NewWorkspaceTools() error = %v", err)
	}

	if _, err := os.Stat(workspaceDir); err != nil {
		t.Errorf("expected workspace directory to be created: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.Name()] = true
	}
	if len(got) != 2 || !got["bash"] || !got["read_file"] {
		t.Errorf("tools = %v, want exactly bash and read_file", got)
	}
	if got["skills"] {
		t.Error("skills discovery tool must not be included in workspace tools")
	}
}

func TestNewWorkspaceTools_EmptyInputs(t *testing.T) {
	tools, err := NewWorkspaceTools("", []string{"bash"})
	if err != nil || tools != nil {
		t.Errorf("empty dir: tools = %v, err = %v; want nil, nil", tools, err)
	}

	tools, err = NewWorkspaceTools(t.TempDir(), nil)
	if err != nil || tools != nil {
		t.Errorf("no names: tools = %v, err = %v; want nil, nil", tools, err)
	}
}
