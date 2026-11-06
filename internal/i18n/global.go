package i18n

import "sync/atomic"

// The global bundle exists so deep backend packages can render translatable
// errors without threading presentation state through every signature. It is
// installed once at startup, before any goroutine that could produce an
// error is allowed to run; the atomics keep even a misbehaving future reload
// data-race-free rather than merely lucky.

var (
	globalBundle atomic.Pointer[Bundle]
	activeCode   atomic.Value // string
)

func init() { activeCode.Store(FallbackCode) }

// SetGlobal installs the bundle every package-level render uses.
func SetGlobal(b *Bundle) { globalBundle.Store(b) }

// Global returns the installed bundle, or nil before SetGlobal ran.
func Global() *Bundle { return globalBundle.Load() }

// SetActive records the language backend strings render in. Call it wherever
// the active language is established or changes.
func SetActive(code string) {
	if code == "" {
		code = FallbackCode
	}
	activeCode.Store(code)
}

// Active is the language package-level Text and LocalizedError render in.
func Active() string {
	if code, ok := activeCode.Load().(string); ok && code != "" {
		return code
	}
	return FallbackCode
}

// Text renders a key in the active language. Without an installed bundle it
// returns the bare key, which keeps unit tests and early startup noise-free.
func Text(key string, vars map[string]any) string {
	b := Global()
	if b == nil {
		return key
	}
	return b.Text(Active(), key, vars)
}

// LocalizedError carries a catalogue key (plus its interpolation vars and an
// optional wrapped cause) instead of a finished sentence, so a long-lived
// error renders in whatever language is active when it is finally shown.
type LocalizedError struct {
	Key   string
	Vars  map[string]any
	Cause error
}

// Fail builds a localized error from a catalogue key.
func Fail(key string, vars map[string]any) error {
	return &LocalizedError{Key: key, Vars: vars}
}

// FailWrap builds a localized error around err. A nil err returns nil: the
// `if err != nil { return i18n.FailWrap(err, …) }` shape should never have to
// defend against turning nothing into a failure.
func FailWrap(err error, key string, vars map[string]any) error {
	if err == nil {
		return nil
	}
	return &LocalizedError{Key: key, Vars: vars, Cause: err}
}

func (e *LocalizedError) Error() string {
	text := Text(e.Key, e.Vars)
	if e.Cause != nil {
		return text + ": " + e.Cause.Error()
	}
	return text
}

// Unwrap keeps errors.Is/errors.As working across wrapped causes.
func (e *LocalizedError) Unwrap() error { return e.Cause }
