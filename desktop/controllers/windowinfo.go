package controllers

// WindowInfo tells the frontend which personality this process runs:
// the main GUI or a detached terminal window. The frontend probes it
// once at mount; bindings exist in both processes.

// WindowInfo is the role probe.
type WindowInfo struct {
	info map[string]any
}

// NewWindowInfoMain describes the primary GUI process.
func NewWindowInfoMain() *WindowInfo {
	return &WindowInfo{info: map[string]any{"role": "main"}}
}

// NewWindowInfoDetached describes a satellite terminal process.
func NewWindowInfoDetached(ptyID, ownerURL, token string) *WindowInfo {
	return &WindowInfo{info: map[string]any{
		"role": "detached", "ptyId": ptyID,
		"ownerUrl": ownerURL, "token": token,
	}}
}

// WindowRole returns this process's identity bundle.
func (w *WindowInfo) WindowRole() map[string]any { return w.info }
