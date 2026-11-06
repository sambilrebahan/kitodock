//go:build windows

package atomicfile

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// copyAccessRules transfers the security descriptor (owner, group and the
// DACL — the parts that decide who may open the file) from an existing file
// onto the temporary replacement. Without this, CreateTemp's inherited
// default DACL would silently broaden or narrow access the moment the swap
// lands; config.json's admin-only ACL must outlive every save.
//
// SACLs are deliberately not copied: they are audit policy, not access
// policy, and reading them needs a privilege the app should not require.
func copyAccessRules(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("open source for its security descriptor: %w", err)
	}
	defer source.Close()

	const what = windows.OWNER_SECURITY_INFORMATION |
		windows.GROUP_SECURITY_INFORMATION |
		windows.DACL_SECURITY_INFORMATION

	descriptor, err := windows.GetSecurityInfo(windows.Handle(source.Fd()), windows.SE_FILE_OBJECT, what)
	if err != nil {
		return fmt.Errorf("read security descriptor: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	group, _, err := descriptor.Group()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}

	// SetSecurityInfo needs the handle opened with the rights matching what
	// it writes: WRITE_OWNER for the owner, WRITE_DAC for the DACL. A plain
	// write handle is denied there, whatever the DACL says.
	to16, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	target, err := windows.CreateFile(
		to16,
		windows.GENERIC_WRITE|windows.WRITE_OWNER|windows.WRITE_DAC,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return fmt.Errorf("open replacement for descriptor write: %w", err)
	}
	defer windows.CloseHandle(target)
	// SetSecurityInfo without PROTECTED_DACL lets the parent's inheritable
	// ACEs flow on top of the copied ones — on a temp directory that widens
	// access. Protecting the DACL pins exactly the source's access list:
	// GetSecurityInfo already resolved inherited ACEs into it, so effective
	// access round-trips unchanged.
	setFlags := what | windows.PROTECTED_DACL_SECURITY_INFORMATION
	if err := windows.SetSecurityInfo(target, windows.SE_FILE_OBJECT, windows.SECURITY_INFORMATION(setFlags), owner, group, dacl, nil); err != nil {
		return fmt.Errorf("copy security descriptor: %w", err)
	}
	return nil
}
