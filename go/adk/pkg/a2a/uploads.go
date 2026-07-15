package a2a

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	a2atype "github.com/a2aproject/a2a-go/a2a"
	"github.com/go-logr/logr"

	"github.com/kagent-dev/kagent/go/adk/pkg/skills"
)

var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// sanitizeUploadName reduces a client-supplied file name to a safe basename.
// Falls back to a positional name when nothing usable remains.
func sanitizeUploadName(name string, index int) string {
	base := filepath.Base(strings.TrimSpace(name))
	base = unsafeFilenameChars.ReplaceAllString(base, "_")
	base = strings.TrimLeft(base, ".")
	if base == "" {
		return fmt.Sprintf("upload-%d", index+1)
	}
	return base
}

// uniqueUploadPath returns a path in dir for name that does not collide with
// an existing file, appending -1, -2, ... before the extension as needed.
func uniqueUploadPath(dir, name string) string {
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func asFilePart(part a2atype.Part) (*a2atype.FilePart, bool) {
	switch p := part.(type) {
	case a2atype.FilePart:
		return &p, true
	case *a2atype.FilePart:
		return p, true
	default:
		return nil, false
	}
}

// materializeInboundFiles writes base64-encoded file parts of an inbound
// message into the session workspace uploads/ directory so the agent's bash
// and file tools can access them.
//
// Non-image file parts are replaced with a text part referencing the saved
// path (most chat-completion providers reject arbitrary inline blobs); image
// parts are kept inline in addition to being saved, so vision-capable models
// still see them. URI file parts are left untouched.
//
// Returns msg unchanged when there is no skills workspace configured, no
// session, or no file parts.
func materializeInboundFiles(msg *a2atype.Message, sessionID, skillsDirectory string, logger logr.Logger) *a2atype.Message {
	if msg == nil || skillsDirectory == "" || sessionID == "" {
		return msg
	}

	hasFileParts := false
	for _, part := range msg.Parts {
		if _, ok := asFilePart(part); ok {
			hasFileParts = true
			break
		}
	}
	if !hasFileParts {
		return msg
	}

	sessionPath, err := skills.GetSessionPath(sessionID, skillsDirectory)
	if err != nil {
		logger.Error(err, "Failed to resolve session path; keeping file parts inline", "sessionID", sessionID)
		return msg
	}
	uploadsDir := filepath.Join(sessionPath, "uploads")

	cp := *msg
	parts := make(a2atype.ContentParts, 0, len(msg.Parts))
	for i, part := range msg.Parts {
		fp, ok := asFilePart(part)
		if !ok {
			parts = append(parts, part)
			continue
		}
		fileBytes, ok := fp.File.(a2atype.FileBytes)
		if !ok {
			// URI-based file: leave to the default conversion path.
			parts = append(parts, part)
			continue
		}
		data, err := base64.StdEncoding.DecodeString(fileBytes.Bytes)
		if err != nil {
			logger.Error(err, "Failed to decode uploaded file; keeping part inline", "name", fileBytes.Name)
			parts = append(parts, part)
			continue
		}
		name := sanitizeUploadName(fileBytes.Name, i)
		target := uniqueUploadPath(uploadsDir, name)
		if err := os.WriteFile(target, data, 0o644); err != nil {
			logger.Error(err, "Failed to save uploaded file; keeping part inline", "name", name)
			parts = append(parts, part)
			continue
		}

		savedName := filepath.Base(target)
		mimeType := fileBytes.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		note := fmt.Sprintf(
			"[File uploaded: %s (%s, %d bytes). It is saved in the session workspace at uploads/%s — use the file or bash tools to read it.]",
			savedName, mimeType, len(data), savedName,
		)
		parts = append(parts, a2atype.TextPart{Text: note})
		if strings.HasPrefix(mimeType, "image/") {
			// Keep images inline as well so vision-capable models see them.
			parts = append(parts, part)
		}
		logger.Info("Saved uploaded file to session workspace", "path", target, "bytes", len(data))
	}
	cp.Parts = parts
	return &cp
}
