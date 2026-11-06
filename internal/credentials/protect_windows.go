//go:build windows

package credentials

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// protect delegates to DPAPI's CryptProtectData with the current user's key
// and no entropy: the goal is binding the value to the account, not adding a
// second secret we would then have to store somewhere.
func protect(plain []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	blob := unsafe.Slice(out.Data, out.Size)
	copied := append([]byte(nil), blob...)
	windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return copied, nil
}

func unprotect(blob []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(blob)), Data: &blob[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	plain := unsafe.Slice(out.Data, out.Size)
	copied := append([]byte(nil), plain...)
	windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return copied, nil
}
