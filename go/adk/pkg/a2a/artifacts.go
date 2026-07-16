package a2a

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"time"

	a2atype "github.com/a2aproject/a2a-go/a2a"
	"github.com/go-logr/logr"

	"github.com/kagent-dev/kagent/go/adk/pkg/skills"
)

const (
	// Per-file inline size cap: larger outputs are announced with a text
	// placeholder instead of base64 bytes (task JSON lives in the DB).
	maxArtifactFileBytes = 2 * 1024 * 1024
	// Per-turn artifact cap so a runaway loop can't flood the task store.
	maxArtifactsPerTurn = 10

	// FileArtifactType marks an A2A artifact carrying a session output file.
	FileArtifactType = "file_artifact"
)

type outputFileInfo struct {
	size    int64
	modTime time.Time
}

// outputSnapshot maps outputs/-relative paths to their size and mtime.
type outputSnapshot map[string]outputFileInfo

type outputFile struct {
	// RelPath is the path relative to the session outputs/ directory.
	RelPath string
	AbsPath string
	Size    int64
}

// sessionOutputsDir resolves the outputs/ directory for a session. Returns ""
// when the session workspace cannot be resolved.
func sessionOutputsDir(sessionID, skillsDirectory string) string {
	if sessionID == "" {
		return ""
	}
	sessionPath, err := skills.GetSessionPath(sessionID, skillsDirectory)
	if err != nil {
		return ""
	}
	return filepath.Join(sessionPath, "outputs")
}

// snapshotOutputs records size and mtime for every file under outputsDir.
// A missing or unreadable directory yields an empty snapshot.
func snapshotOutputs(outputsDir string) outputSnapshot {
	snapshot := outputSnapshot{}
	if outputsDir == "" {
		return snapshot
	}
	_ = filepath.WalkDir(outputsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort scan
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		rel, err := filepath.Rel(outputsDir, path)
		if err != nil {
			return nil //nolint:nilerr
		}
		snapshot[rel] = outputFileInfo{size: info.Size(), modTime: info.ModTime()}
		return nil
	})
	return snapshot
}

// collectNewOutputs returns files under outputsDir that are new or modified
// relative to the before snapshot, sorted by relative path.
func collectNewOutputs(outputsDir string, before outputSnapshot) []outputFile {
	var changed []outputFile
	after := snapshotOutputs(outputsDir)
	for rel, info := range after {
		prev, existed := before[rel]
		if existed && prev.size == info.size && prev.modTime.Equal(info.modTime) {
			continue
		}
		changed = append(changed, outputFile{
			RelPath: rel,
			AbsPath: filepath.Join(outputsDir, rel),
			Size:    info.size,
		})
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].RelPath < changed[j].RelPath })
	return changed
}

// buildFileArtifact converts one output file into an A2A artifact. Files
// within the size cap are inlined as base64 FileParts; larger files get a
// text placeholder part (the task store drops artifacts with no parts).
func buildFileArtifact(file outputFile) (*a2atype.Artifact, error) {
	mimeType := mime.TypeByExtension(filepath.Ext(file.RelPath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	artifact := &a2atype.Artifact{
		ID:   a2atype.NewArtifactID(),
		Name: file.RelPath,
		Metadata: map[string]any{
			GetKAgentMetadataKey(A2ADataPartMetadataTypeKey): FileArtifactType,
			GetKAgentMetadataKey("path"):                     filepath.Join("outputs", file.RelPath),
			GetKAgentMetadataKey("size"):                     file.Size,
			GetKAgentMetadataKey("mime_type"):                mimeType,
		},
	}

	if file.Size > maxArtifactFileBytes {
		artifact.Parts = a2atype.ContentParts{a2atype.TextPart{
			Text: fmt.Sprintf(
				"File %s (%d bytes) exceeds the %dMB inline artifact limit. Ask the agent to compress or split it.",
				file.RelPath, file.Size, maxArtifactFileBytes/1024/1024,
			),
		}}
		return artifact, nil
	}

	data, err := os.ReadFile(file.AbsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read output file %s: %w", file.AbsPath, err)
	}
	artifact.Parts = a2atype.ContentParts{a2atype.FilePart{
		File: a2atype.FileBytes{
			FileMeta: a2atype.FileMeta{Name: file.RelPath, MimeType: mimeType},
			Bytes:    base64.StdEncoding.EncodeToString(data),
		},
	}}
	return artifact, nil
}

// buildOutputArtifacts diffs outputsDir against the pre-run snapshot and
// returns artifacts for new/changed files, capped at maxArtifactsPerTurn.
func buildOutputArtifacts(outputsDir string, before outputSnapshot, logger logr.Logger) []*a2atype.Artifact {
	files := collectNewOutputs(outputsDir, before)
	if len(files) == 0 {
		return nil
	}
	if len(files) > maxArtifactsPerTurn {
		logger.Info("Too many output files changed this turn; emitting only the first ones",
			"changed", len(files), "limit", maxArtifactsPerTurn)
		files = files[:maxArtifactsPerTurn]
	}

	artifacts := make([]*a2atype.Artifact, 0, len(files))
	for _, file := range files {
		artifact, err := buildFileArtifact(file)
		if err != nil {
			logger.Error(err, "Skipping output artifact", "file", file.RelPath)
			continue
		}
		artifacts = append(artifacts, artifact)
		logger.Info("Emitting output file artifact", "file", file.RelPath, "bytes", file.Size)
	}
	return artifacts
}
