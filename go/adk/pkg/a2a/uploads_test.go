package a2a

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/a2a"
	"github.com/go-logr/logr"
)

func TestSanitizeUploadName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		index int
		want  string
	}{
		{name: "simple name", input: "data.csv", index: 0, want: "data.csv"},
		{name: "path traversal stripped", input: "../../etc/passwd", index: 0, want: "passwd"},
		{name: "unsafe chars replaced", input: "my file (1).txt", index: 0, want: "my_file__1_.txt"},
		{name: "hidden file dot stripped", input: ".env", index: 0, want: "env"},
		{name: "empty falls back", input: "", index: 2, want: "upload-3"},
		{name: "dots only falls back", input: "...", index: 0, want: "upload-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeUploadName(tt.input, tt.index); got != tt.want {
				t.Errorf("sanitizeUploadName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUniqueUploadPath(t *testing.T) {
	dir := t.TempDir()

	first := uniqueUploadPath(dir, "data.csv")
	if filepath.Base(first) != "data.csv" {
		t.Fatalf("first path = %q, want data.csv", first)
	}
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := uniqueUploadPath(dir, "data.csv")
	if filepath.Base(second) != "data-1.csv" {
		t.Errorf("second path = %q, want data-1.csv", filepath.Base(second))
	}
}

func newFileMessage(name, mimeType string, content []byte) *a2atype.Message {
	return &a2atype.Message{
		Parts: a2atype.ContentParts{
			a2atype.TextPart{Text: "please analyze this"},
			a2atype.FilePart{
				File: a2atype.FileBytes{
					FileMeta: a2atype.FileMeta{Name: name, MimeType: mimeType},
					Bytes:    base64.StdEncoding.EncodeToString(content),
				},
			},
		},
	}
}

func TestMaterializeInboundFiles_WritesFileAndReplacesPart(t *testing.T) {
	skillsDir := t.TempDir()
	sessionID := "test-session-upload-1"
	t.Cleanup(func() { os.RemoveAll(filepath.Join(os.TempDir(), "kagent", sessionID)) })

	content := []byte("col1,col2\n1,2\n")
	msg := newFileMessage("data.csv", "text/csv", content)

	got := materializeInboundFiles(msg, sessionID, skillsDir, logr.Discard())
	if got == nil {
		t.Fatal("materializeInboundFiles returned nil")
	}
	if len(got.Parts) != 2 {
		t.Fatalf("parts = %d, want 2 (text + note)", len(got.Parts))
	}

	note, ok := got.Parts[1].(a2atype.TextPart)
	if !ok {
		t.Fatalf("part[1] = %T, want TextPart note", got.Parts[1])
	}
	if !strings.Contains(note.Text, "uploads/data.csv") {
		t.Errorf("note = %q, want reference to uploads/data.csv", note.Text)
	}

	saved := filepath.Join(os.TempDir(), "kagent", sessionID, "uploads", "data.csv")
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("saved file not readable: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("saved content = %q, want %q", data, content)
	}
}

func TestMaterializeInboundFiles_KeepsImageInline(t *testing.T) {
	skillsDir := t.TempDir()
	sessionID := "test-session-upload-2"
	t.Cleanup(func() { os.RemoveAll(filepath.Join(os.TempDir(), "kagent", sessionID)) })

	msg := newFileMessage("chart.png", "image/png", []byte{0x89, 0x50, 0x4e, 0x47})

	got := materializeInboundFiles(msg, sessionID, skillsDir, logr.Discard())
	// text + note + original image part kept inline
	if len(got.Parts) != 3 {
		t.Fatalf("parts = %d, want 3 (text + note + inline image)", len(got.Parts))
	}
	if _, ok := got.Parts[2].(a2atype.FilePart); !ok {
		t.Errorf("part[2] = %T, want FilePart kept inline", got.Parts[2])
	}
}

func TestMaterializeInboundFiles_NoWorkspaceNoChange(t *testing.T) {
	msg := newFileMessage("data.csv", "text/csv", []byte("x"))

	got := materializeInboundFiles(msg, "session-x", "", logr.Discard())
	if got != msg {
		t.Error("expected message returned unchanged when no skills workspace")
	}

	got = materializeInboundFiles(msg, "", "/tmp/skills", logr.Discard())
	if got != msg {
		t.Error("expected message returned unchanged when no session ID")
	}
}

func TestMaterializeInboundFiles_InvalidBase64KeptInline(t *testing.T) {
	skillsDir := t.TempDir()
	sessionID := "test-session-upload-3"
	t.Cleanup(func() { os.RemoveAll(filepath.Join(os.TempDir(), "kagent", sessionID)) })

	msg := &a2atype.Message{
		Parts: a2atype.ContentParts{
			a2atype.FilePart{
				File: a2atype.FileBytes{
					FileMeta: a2atype.FileMeta{Name: "bad.bin", MimeType: "application/octet-stream"},
					Bytes:    "!!!not-base64!!!",
				},
			},
		},
	}

	got := materializeInboundFiles(msg, sessionID, skillsDir, logr.Discard())
	if len(got.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(got.Parts))
	}
	if _, ok := got.Parts[0].(a2atype.FilePart); !ok {
		t.Errorf("part[0] = %T, want original FilePart kept", got.Parts[0])
	}
}
