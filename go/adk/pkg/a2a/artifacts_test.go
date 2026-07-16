package a2a

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/a2a"
	"github.com/go-logr/logr"
)

func writeOutput(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollectNewOutputs(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string) outputSnapshot
		mutate  func(t *testing.T, dir string)
		wantRel []string
	}{
		{
			name:    "new file detected",
			setup:   func(t *testing.T, dir string) outputSnapshot { return snapshotOutputs(dir) },
			mutate:  func(t *testing.T, dir string) { writeOutput(t, dir, "report.md", "# hi") },
			wantRel: []string{"report.md"},
		},
		{
			name: "unchanged file skipped",
			setup: func(t *testing.T, dir string) outputSnapshot {
				writeOutput(t, dir, "keep.txt", "same")
				return snapshotOutputs(dir)
			},
			mutate:  func(t *testing.T, dir string) {},
			wantRel: nil,
		},
		{
			name: "modified file detected",
			setup: func(t *testing.T, dir string) outputSnapshot {
				path := writeOutput(t, dir, "data.csv", "a,b")
				// Backdate mtime so the rewrite below always differs.
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
				return snapshotOutputs(dir)
			},
			mutate:  func(t *testing.T, dir string) { writeOutput(t, dir, "data.csv", "a,b,c") },
			wantRel: []string{"data.csv"},
		},
		{
			name:    "nested files detected with relative paths",
			setup:   func(t *testing.T, dir string) outputSnapshot { return snapshotOutputs(dir) },
			mutate:  func(t *testing.T, dir string) { writeOutput(t, dir, filepath.Join("sub", "chart.svg"), "<svg/>") },
			wantRel: []string{filepath.Join("sub", "chart.svg")},
		},
		{
			name:    "missing directory yields nothing",
			setup:   func(t *testing.T, dir string) outputSnapshot { return snapshotOutputs(filepath.Join(dir, "gone")) },
			mutate:  func(t *testing.T, dir string) {},
			wantRel: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			outputsDir := dir
			if tt.name == "missing directory yields nothing" {
				outputsDir = filepath.Join(dir, "gone")
			}
			before := tt.setup(t, outputsDir)
			tt.mutate(t, outputsDir)

			got := collectNewOutputs(outputsDir, before)
			var gotRel []string
			for _, f := range got {
				gotRel = append(gotRel, f.RelPath)
			}
			if len(gotRel) != len(tt.wantRel) {
				t.Fatalf("changed files = %v, want %v", gotRel, tt.wantRel)
			}
			for i := range gotRel {
				if gotRel[i] != tt.wantRel[i] {
					t.Errorf("changed[%d] = %q, want %q", i, gotRel[i], tt.wantRel[i])
				}
			}
		})
	}
}

func TestBuildFileArtifact_InlinesSmallFile(t *testing.T) {
	dir := t.TempDir()
	content := "# report\nhello"
	writeOutput(t, dir, "report.md", content)

	artifact, err := buildFileArtifact(outputFile{
		RelPath: "report.md",
		AbsPath: filepath.Join(dir, "report.md"),
		Size:    int64(len(content)),
	})
	if err != nil {
		t.Fatal(err)
	}

	if artifact.Name != "report.md" {
		t.Errorf("name = %q, want report.md", artifact.Name)
	}
	if got := artifact.Metadata[GetKAgentMetadataKey(A2ADataPartMetadataTypeKey)]; got != FileArtifactType {
		t.Errorf("type metadata = %v, want %q", got, FileArtifactType)
	}
	if got := artifact.Metadata[GetKAgentMetadataKey("path")]; got != filepath.Join("outputs", "report.md") {
		t.Errorf("path metadata = %v", got)
	}

	if len(artifact.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(artifact.Parts))
	}
	filePart, ok := artifact.Parts[0].(a2atype.FilePart)
	if !ok {
		t.Fatalf("part = %T, want FilePart", artifact.Parts[0])
	}
	fileBytes, ok := filePart.File.(a2atype.FileBytes)
	if !ok {
		t.Fatalf("file = %T, want FileBytes", filePart.File)
	}
	decoded, err := base64.StdEncoding.DecodeString(fileBytes.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != content {
		t.Errorf("decoded = %q, want %q", decoded, content)
	}
	if !strings.HasPrefix(fileBytes.MimeType, "text/markdown") {
		t.Errorf("mime = %q, want text/markdown*", fileBytes.MimeType)
	}
}

func TestBuildFileArtifact_OversizedGetsPlaceholder(t *testing.T) {
	artifact, err := buildFileArtifact(outputFile{
		RelPath: "huge.bin",
		AbsPath: "/nonexistent/huge.bin", // must not be read
		Size:    maxArtifactFileBytes + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(artifact.Parts))
	}
	text, ok := artifact.Parts[0].(a2atype.TextPart)
	if !ok {
		t.Fatalf("part = %T, want TextPart placeholder", artifact.Parts[0])
	}
	if !strings.Contains(text.Text, "huge.bin") {
		t.Errorf("placeholder text = %q, want file name mentioned", text.Text)
	}
}

func TestBuildOutputArtifacts_CapsPerTurn(t *testing.T) {
	dir := t.TempDir()
	before := snapshotOutputs(dir)
	for i := 0; i < maxArtifactsPerTurn+5; i++ {
		writeOutput(t, dir, filepath.Join("many", string(rune('a'+i))+".txt"), "x")
	}

	artifacts := buildOutputArtifacts(dir, before, logr.Discard())
	if len(artifacts) != maxArtifactsPerTurn {
		t.Errorf("artifacts = %d, want cap %d", len(artifacts), maxArtifactsPerTurn)
	}
}

func TestBuildOutputArtifacts_NoChangesNoArtifacts(t *testing.T) {
	dir := t.TempDir()
	writeOutput(t, dir, "existing.txt", "old")
	before := snapshotOutputs(dir)

	artifacts := buildOutputArtifacts(dir, before, logr.Discard())
	if len(artifacts) != 0 {
		t.Errorf("artifacts = %d, want 0", len(artifacts))
	}
}
