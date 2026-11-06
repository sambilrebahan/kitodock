//go:build windows

package atomicfile

import (
	"os"

	"golang.org/x/sys/windows"
)

// replace puts the temporary file onto target's name. MoveFileEx with
// REPLACE_EXISTING and WRITE_THROUGH is the supported atomic swap on NTFS:
// readers see either the old file or the new one, and the metadata write is
// flushed before the call returns.
func replace(tmp, target string) error {
	tmp16, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	target16, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH)
	if err := windows.MoveFileEx(tmp16, target16, flags); err != nil {
		return os.NewSyscallError("MoveFileEx", err)
	}
	return nil
}
