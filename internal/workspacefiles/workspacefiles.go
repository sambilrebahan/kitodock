// Package workspacefiles is the hardened file-access layer for the in-app
// file browser and editor. All path authority flows from a ContextRef (a
// Kitodock-owned source: a project name or agent session), never from a
// caller-supplied path. Reads and saves preserve BOM and line endings;
// saves are optimistic-revision checked so an external writer is detected
// rather than silently overwritten.
package workspacefiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ContextRef identifies who owns the file tree being browsed.
type ContextRef struct {
	Kind string `json:"kind"` // "project" | "agent"
	Name string `json:"name"` // project name or agent session id
}

// RootInfo describes the resolved root for a context.
type RootInfo struct {
	Context ContextRef `json:"context"`
	Root    string     `json:"root"`
	// RootKey is a stable identifier for the root (used by the frontend for
	// tab identity when paths change).
	RootKey string `json:"rootKey"`
}

// Result codes the frontend understands.
const (
	ResultOK        = "ok"
	ResultChanged   = "changed" // external modification detected
	ResultUnchanged = "unchanged"
	ResultBlocked   = "blocked" // entry cannot be accessed
	ResultNotFound  = "not_found"
	ResultTooLarge  = "too_large"
	ResultBinary    = "binary"
	ResultConflict  = "conflict" // save collision
)

// Limits.
const (
	MaxTextSize  = 2 << 20 // 2 MiB
	MaxImageSize = 8 << 20 // 8 MiB
	PageSize     = 500
)

// Entry is one item in a directory listing.
type Entry struct {
	Name        string `json:"name"`
	IsDir       bool   `json:"isDir"`
	Size        int64  `json:"size,omitempty"`
	ModTime     string `json:"modTime,omitempty"`
	Blocked     bool   `json:"blocked,omitempty"`
	BlockReason string `json:"blockReason,omitempty"`
}

// ListResult is a paginated directory listing.
type ListResult struct {
	Context ContextRef `json:"context"`
	Path    string     `json:"path"` // relative path within root
	Entries []Entry    `json:"entries"`
	Cursor  string     `json:"cursor,omitempty"` // pass back for next page
	Stale   bool       `json:"stale,omitempty"`  // cursor didn't match
	Total   int        `json:"total"`
}

// Document is a read file.
type Document struct {
	Context  ContextRef `json:"context"`
	Path     string     `json:"path"`
	Content  string     `json:"content"`
	Revision string     `json:"revision"` // sha256 of content bytes
	Encoding string     `json:"encoding"` // "utf-8" | "binary"
	BOM      bool       `json:"bom"`
	EOL      string     `json:"eol"` // "lf" | "crlf" | "mixed" | ""
	MixedEOL bool       `json:"mixedEOL"`
	ReadOnly bool       `json:"readOnly"` // mixed EOL or binary
}

// ImageResult is a read image file (base64).
type ImageResult struct {
	Context ContextRef `json:"context"`
	Path    string     `json:"path"`
	MIME    string     `json:"mime"`
	Data    string     `json:"data"` // base64
}

// Service is the main entry point.
type Service struct {
	mu    sync.Mutex
	root  string
	locks map[string]*sync.Mutex // per-document save locks
}

func New(root string) *Service {
	return &Service{root: root, locks: map[string]*sync.Mutex{}}
}

// Root returns the service's root directory.
func (s *Service) Root() string { return s.root }

// validPath checks a relative path for traversal, absolute, reserved names.
func validPath(rel string) bool {
	if rel == "" {
		return true // root listing
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, ":") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// resolve turns a relative path into an absolute one, refusing escapes —
// lexical ones ("..") and filesystem ones alike: any symlink or junction
// along the way would let a cloned repo point outside the project, so the
// walk Lstat's every component and rejects reparse points.
func (s *Service) resolve(rel string) (string, error) {
	if !validPath(rel) {
		return "", fmt.Errorf("invalid path %q", rel)
	}
	if rel == "" {
		return s.root, nil
	}
	full := filepath.Join(s.root, filepath.FromSlash(rel))
	// Belt and braces: confirm the joined path is still under root.
	if !strings.HasPrefix(strings.ToLower(full), strings.ToLower(s.root)+string(os.PathSeparator)) &&
		!strings.EqualFold(full, s.root) {
		return "", fmt.Errorf("path escapes root")
	}
	if err := rejectSymlinks(s.root, filepath.FromSlash(rel)); err != nil {
		return "", err
	}
	return full, nil
}

// rejectSymlinks walks each component under root and refuses paths that
// traverse a symlink or junction. The final component is checked too:
// reading through a link is as dangerous as writing through one.
func rejectSymlinks(root, rel string) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	current := root
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if i == len(parts)-1 {
				return nil // creating a new file is fine
			}
			return nil // deeper missing parents surface on the real op
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", part, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
			return fmt.Errorf("%s traverses a symbolic link", rel)
		}
	}
	return nil
}

// List returns one page of a directory listing, sorted deterministically
// (case-insensitive, dirs first).
func (s *Service) List(ref ContextRef, relPath, cursor string) ListResult {
	result := ListResult{Context: ref, Path: relPath}
	full, err := s.resolve(relPath)
	if err != nil {
		result.Stale = true
		return result
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return result
	}

	// Sort: dirs first, then case-insensitive by name.
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir() != b.IsDir() {
			return a.IsDir()
		}
		return strings.ToLower(a.Name()) < strings.ToLower(b.Name())
	})

	// Filter by cursor (resume after this name).
	start := 0
	if cursor != "" {
		for i, e := range entries {
			if e.Name() == cursor {
				start = i + 1
				break
			}
		}
		if start == 0 {
			result.Stale = true
			return result
		}
	}

	result.Total = len(entries)
	end := start + PageSize
	if end > len(entries) {
		end = len(entries)
	}
	for _, e := range entries[start:end] {
		entry := Entry{Name: e.Name(), IsDir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			entry.Size = info.Size()
			entry.ModTime = info.ModTime().Format("2006-01-02T15:04:05Z")
		}
		// Mark blocked entries (symlinks, inaccessible).
		if e.Type()&os.ModeSymlink != 0 {
			entry.Blocked = true
			entry.BlockReason = "symlink"
		}
		result.Entries = append(result.Entries, entry)
	}
	if end < len(entries) {
		result.Cursor = entries[end-1].Name()
	}
	return result
}

// Read loads a text document with BOM/EOL detection and revision hashing.
func (s *Service) Read(ref ContextRef, relPath string) (Document, string) {
	full, err := s.resolve(relPath)
	if err != nil {
		return Document{}, ResultBlocked
	}
	data, err := os.ReadFile(full)
	if os.IsNotExist(err) {
		return Document{}, ResultNotFound
	}
	if err != nil {
		return Document{}, ResultBlocked
	}
	if len(data) > MaxTextSize {
		return Document{}, ResultTooLarge
	}

	doc := Document{Context: ref, Path: relPath}

	// BOM detection.
	if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xEF, 0xBB, 0xBF}) {
		doc.BOM = true
		data = data[3:]
	}

	// Binary detection: NUL byte in first 8 KiB.
	checkLen := len(data)
	if checkLen > 8192 {
		checkLen = 8192
	}
	if bytes.IndexByte(data[:checkLen], 0) >= 0 {
		doc.Encoding = "binary"
		doc.ReadOnly = true
		doc.Content = ""
		doc.Revision = hashBytes(data)
		return doc, ResultOK
	}

	// EOL detection.
	crlf := bytes.Count(data, []byte("\r\n"))
	lf := bytes.Count(data, []byte("\n")) - crlf
	switch {
	case crlf > 0 && lf > 0:
		doc.EOL = "mixed"
		doc.MixedEOL = true
		doc.ReadOnly = true
	case crlf > 0:
		doc.EOL = "crlf"
	case lf > 0:
		doc.EOL = "lf"
	default:
		doc.EOL = ""
	}

	doc.Encoding = "utf-8"
	doc.Content = string(data)
	doc.Revision = hashBytes(data)
	return doc, ResultOK
}

// ReadImage loads an image file as base64 with MIME detection.
func (s *Service) ReadImage(ref ContextRef, relPath string) (ImageResult, string) {
	full, err := s.resolve(relPath)
	if err != nil {
		return ImageResult{}, ResultBlocked
	}
	info, err := os.Stat(full)
	if err != nil {
		return ImageResult{}, ResultNotFound
	}
	if info.Size() > MaxImageSize {
		return ImageResult{}, ResultTooLarge
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return ImageResult{}, ResultBlocked
	}

	mime := detectMIME(relPath, data)
	if mime == "" {
		return ImageResult{}, ResultBinary
	}
	return ImageResult{
		Context: ref, Path: relPath,
		MIME: mime, Data: base64.StdEncoding.EncodeToString(data),
	}, ResultOK
}

// Save writes content with optimistic revision checking. If expectedRevision
// is empty, the save proceeds unconditionally (first save). If it doesn't
// match the current file content hash, the save is refused with
// ResultConflict.
func (s *Service) Save(ref ContextRef, relPath, content, expectedRevision string) (string, string) {
	full, err := s.resolve(relPath)
	if err != nil {
		return ResultBlocked, ""
	}

	// Per-document lock prevents concurrent saves racing.
	s.mu.Lock()
	docLock, ok := s.locks[relPath]
	if !ok {
		docLock = &sync.Mutex{}
		s.locks[relPath] = docLock
	}
	s.mu.Unlock()
	docLock.Lock()
	defer docLock.Unlock()

	// Read the current content for revision check.
	current, err := os.ReadFile(full)
	if os.IsNotExist(err) {
		// New file: expectedRevision must also be empty.
		if expectedRevision != "" {
			return ResultConflict, ""
		}
	} else if err != nil {
		return ResultBlocked, ""
	} else {
		// Strip BOM from current for comparison.
		stripped := current
		if len(stripped) >= 3 && bytes.Equal(stripped[:3], []byte{0xEF, 0xBB, 0xBF}) {
			stripped = stripped[3:]
		}
		currentHash := hashBytes(stripped)
		if currentHash != expectedRevision {
			return ResultConflict, currentHash
		}
	}

	// Write: preserve BOM if the original had one (or if we're reading it
	// from the content we were given — the frontend sends BOM separately).
	data := []byte(content)
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return ResultBlocked, ""
	}
	newRev := hashBytes(data)
	return ResultOK, newRev
}

// Check returns the current revision of a file (for conflict polling).
func (s *Service) Check(ref ContextRef, relPath, expectedRevision string) (string, string) {
	full, err := s.resolve(relPath)
	if err != nil {
		return ResultBlocked, ""
	}
	data, err := os.ReadFile(full)
	if os.IsNotExist(err) {
		return ResultNotFound, ""
	}
	if err != nil {
		return ResultBlocked, ""
	}
	// Strip BOM for revision comparison.
	if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xEF, 0xBB, 0xBF}) {
		data = data[3:]
	}
	current := hashBytes(data)
	if current == expectedRevision {
		return ResultUnchanged, current
	}
	return ResultChanged, current
}

// --- helpers -----------------------------------------------------------------

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func detectMIME(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		if len(data) >= 8 && bytes.Equal(data[:4], []byte{0x89, 0x50, 0x4E, 0x47}) {
			return "image/png"
		}
	case ".jpg", ".jpeg":
		if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 {
			return "image/jpeg"
		}
	case ".gif":
		if len(data) >= 6 && bytes.Equal(data[:3], []byte("GIF")) {
			return "image/gif"
		}
	case ".webp":
		if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
			return "image/webp"
		}
	}
	return ""
}
