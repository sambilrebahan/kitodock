// Package vcruntime detects the classic "service silently dies at launch"
// failure mode on Windows: a binary linked against a Visual C++ runtime that
// is missing or older than it needs. The process never starts — it dies with
// STATUS_DLL_NOT_FOUND before main() — so the only honest check is to run
// the binary once with --version and look at how it dies.
package vcruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Download links for the x64 redistributables, by toolchain generation.
const (
	VS17URL = "https://aka.ms/vs/17/release/vc_redist.x64.exe"
	VS18URL = "https://aka.ms/vs/18/release/vc_redist.x64.exe"
)

// statusDLLNotFound is the NTSTATUS a process exits with when the runtime
// DLLs it links are absent.
const statusDLLNotFound = 0xC0000135

// Missing reports that exePath needs a VC++ redistributable that this
// machine does not have (or has in a generation too old for it).
type Missing struct {
	// Binary is the executable that failed to start.
	Binary string
	// URL is the aka.ms download that installs what is needed.
	URL string
	// Stale is true when a runtime is installed but too old (the binary
	// started far enough to complain about the mismatch).
	Stale bool
}

// The message keeps the exact product name "Visual C++ Redistributable":
// the UI matches on it to offer the download button.
func (e *Missing) Error() string {
	what := "requires the Visual C++ Redistributable, which is not installed"
	if e.Stale {
		what = "requires a newer Visual C++ Redistributable than the one installed"
	}
	return fmt.Sprintf("%s %s", e.Binary, what)
}

// Probe describes how the smoke run ended; split out so tests can drive the
// classification directly.
type Probe struct {
	// ExitCode is the process exit code (-1073741515 == 0xC0000135).
	ExitCode int
	// Output is stdout+stderr, for the mismatch diagnostics.
	Output string
	// Err is the exec error, if any.
	Err error
}

// RunProbe launches `<exe> --version` hidden, waits briefly, and reports how
// it ended. A missing executable returns a zero Probe — there is nothing to
// check yet, which is not an error.
func RunProbe(exe string) Probe {
	if exe == "" {
		return Probe{}
	}
	if _, err := os.Stat(exe); err != nil {
		return Probe{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.SysProcAttr = hiddenWindow()
	out, err := cmd.CombinedOutput()
	probe := Probe{Output: string(out), Err: err}
	if cmd.ProcessState != nil {
		probe.ExitCode = cmd.ProcessState.ExitCode()
	}
	return probe
}

// Classify turns a probe into a verdict. A clean exit, or a crash that has
// nothing to do with the runtime, returns nil.
func Classify(exe, redistURL string, probe Probe) error {
	if probe.ExitCode == int(uint32(statusDLLNotFound)) {
		return &Missing{Binary: exe, URL: redistURL}
	}
	if probe.Err == nil && probe.ExitCode == 0 {
		return nil
	}
	// A binary whose runtime is present but too old manages to print a
	// complaint before dying; PHP builds phrase it as an incompatible link.
	text := strings.ToLower(probe.Output)
	if strings.Contains(text, "not compatible with") || strings.Contains(text, "linked with") {
		return &Missing{Binary: exe, URL: redistURL, Stale: true}
	}
	return probe.Err
}

// Check smoke-runs exe and reports a *Missing when the VC++ runtime is the
// problem. redistURL is the download to offer; VS17URL when unsure.
func Check(exe, redistURL string) error {
	if redistURL == "" {
		redistURL = VS17URL
	}
	return Classify(exe, redistURL, RunProbe(exe))
}
