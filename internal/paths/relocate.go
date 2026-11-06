package paths

import (
	"path/filepath"
	"strings"
)

// IsSameRoot reports whether two strings name the same directory, ignoring
// case and separator style. Windows treats C:\Kitodock and c:/kitodock as one
// folder; comparing them as raw strings would make a non-move look like a
// relocation and trigger needless rewrites of every stored path.
func IsSameRoot(a, b string) bool {
	return canonical(a) == canonical(b)
}

// MovePath returns p rewritten from the old environment root onto the new
// one. Paths that do not live under oldRoot are returned untouched: a
// projects folder the user deliberately keeps on another drive is theirs, not
// part of the environment, and must survive a move unedited.
//
// The case style of the replaced prefix is preserved: some stored values are
// lowercased lookup keys, and handing them a mixed-case root would silently
// orphan every record indexed by them.
func MovePath(oldRoot, newRoot, p string) (string, bool) {
	if p == "" {
		return p, false
	}
	oldCanonical := canonical(oldRoot)
	if canonical(p) != oldCanonical && !strings.HasPrefix(canonical(p), oldCanonical+"/") {
		return p, false
	}
	suffix := p[len(oldRoot):]
	replacement := newRoot
	if head := p[:len(oldRoot)]; head == strings.ToLower(head) {
		replacement = strings.ToLower(newRoot)
	}
	return replacement + suffix, true
}

// canonical lowercases a path and normalizes its separators without a
// trailing one, giving two spellings of one directory the same text.
func canonical(p string) string {
	p = strings.ReplaceAll(p, string(filepath.Separator), "/")
	p = strings.ReplaceAll(p, `\`, "/")
	return strings.TrimRight(strings.ToLower(p), "/")
}
