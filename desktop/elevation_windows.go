//go:build windows && !bindings

package main

// UAC relaunch for GUI startup. Phase 0 ships it unused: nothing in the
// shell needs the administrator token yet, so the app runs at whatever level
// the user launched it. Phase 1 turns it on — editing the hosts file and
// managing services is where elevation becomes mandatory.

import (
	"errors"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// relaunchElevated starts a new copy of this executable under the
// administrator token via the runas verb, forwarding args. It reports
// whether the relaunch happened; the caller then exits quietly.
func relaunchElevated(args []string) (bool, error) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	parameters, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return false, err
	}
	dir, _ := windows.UTF16PtrFromString(os.TempDir())
	if err := windows.ShellExecute(0, verb, file, parameters, dir, windows.SW_SHOWNORMAL); err != nil {
		return false, errors.New("opening the Kitodock environment needs an administrator account")
	}
	return true, nil
}
