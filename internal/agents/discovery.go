package agents

// Session discovery: scan each provider's transcript directory for past
// sessions so the UI can offer resume. Transcripts are JSONL with
// provider-specific shapes; parsing stays defensive — an unreadable file
// is skipped, never fatal.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DiscoveredSession is one resumable past session from a provider's store.
type DiscoveredSession struct {
	Provider ProviderKind `json:"provider"`
	// ID is the provider-side session id for `--resume`.
	ID string `json:"id"`
	// DirHint is the project path the transcript belongs to, when the
	// provider records one.
	DirHint string `json:"dirHint,omitempty"`
	// Summary is the first user message, truncated (best effort).
	Summary  string    `json:"summary,omitempty"`
	Modified time.Time `json:"modified"`
	// Path is the transcript file, for debugging.
	Path string `json:"-"`
}

// DiscoverSessions scans one provider's transcripts under home, newest
// first, capped at limit (0 = 50).
func DiscoverSessions(p ProviderKind, home string, limit int) ([]DiscoveredSession, error) {
	root := sessionsRoot(p, home)
	if root == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var found []DiscoveredSession
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, keep walking
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		s := DiscoveredSession{
			Provider: p,
			Modified: info.ModTime(),
			Path:     path,
		}
		s.ID, s.DirHint, s.Summary = inspectTranscript(p, path)
		if s.ID == "" {
			// Fall back to the file stem so resume can still be attempted.
			s.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		}
		found = append(found, s)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Modified.After(found[j].Modified) })
	if len(found) > limit {
		found = found[:limit]
	}
	return found, nil
}

// inspectTranscript pulls the session id, project hint and first user
// message from a transcript's leading lines. Every field is best-effort.
func inspectTranscript(p ProviderKind, path string) (id, dirHint, summary string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	userSeen := 0
	for scanner.Scan() && (id == "" || dirHint == "" || userSeen < 1) {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var obj map[string]any
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		if id == "" {
			id = firstString(obj, "sessionId", "session_id", "threadId", "thread_id")
		}
		if dirHint == "" {
			if cwd := firstString(obj, "cwd", "projectPath", "project_dir"); cwd != "" {
				dirHint = cwd
			}
		}
		if summary == "" {
			switch {
			case firstString(obj, "type") == "user" || firstString(obj, "role") == "user":
				summary = clipSummary(firstString(obj, "message", "text", "content"))
				userSeen++
			case firstString(obj, "kind") == "user_prompt" || firstString(obj, "record_type") == "prompt":
				summary = clipSummary(firstString(obj, "prompt", "text", "message"))
				userSeen++
			}
		}
	}
	return id, dirHint, summary
}

// firstString returns the first present non-empty string field.
func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// clipSummary trims a summary to one short line.
func clipSummary(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}
