// Package clipboard reads and writes text on the OS clipboard. Kitodock is
// Windows-first; the Windows implementation talks to user32 directly (no cgo)
// and a stub keeps other platforms compiling.
package clipboard

import "errors"

// ErrUnavailable is returned where no native clipboard exists.
var ErrUnavailable = errors.New("clipboard is unavailable on this platform")
