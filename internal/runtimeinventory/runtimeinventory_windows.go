//go:build windows

// Package runtimeinventory identifies an installed runtime by reading the
// version resource out of its executable. Folder names and install markers
// can lie — an interrupted install leaves an empty directory behind — but
// the binary's own version block cannot.
package runtimeinventory

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Metadata is what the executable says about itself.
type Metadata struct {
	// FullVersion is the product version string ("8.4.25").
	FullVersion string
	// Prerelease is true when the version resource carries the
	// VS_FF_PRERELEASE flag (alphas, betas, RCs).
	Prerelease bool
}

// vsFlagPrerelease is VS_FF_PRERELEASE from the Ver API.
const vsFlagPrerelease = 0x00000002

var (
	cacheMu sync.Mutex
	cache   = map[string]Metadata{}
)

// Read returns the version metadata of the executable at path. Results are
// memoized per path: the call walks the PE resource section, and installed
// runtimes do not change underneath a running app.
func Read(path string) (Metadata, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if hit, ok := cache[path]; ok {
		return hit, nil
	}
	meta, err := readResource(path)
	if err == nil {
		cache[path] = meta
	}
	return meta, err
}

func readResource(path string) (Metadata, error) {
	var none Metadata
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return none, fmt.Errorf("%s has no version resource", path)
	}
	block := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&block[0])); err != nil {
		return none, err
	}
	return inspect(block)
}

// inspect pulls the fixed header (prerelease flag, numeric fallback) and the
// ProductVersion string out of an already-loaded version block.
func inspect(block []byte) (Metadata, error) {
	var none Metadata

	var fixed *windows.VS_FIXEDFILEINFO
	fixedSize := uint32(0)
	if err := windows.VerQueryValue(unsafe.Pointer(&block[0]), `\`, unsafe.Pointer(&fixed), &fixedSize); err != nil || fixed == nil || fixedSize == 0 {
		return none, fmt.Errorf("version resource has no fixed header")
	}

	meta := Metadata{
		FullVersion: numericProductVersion(fixed),
		Prerelease:  fixed.FileFlags&vsFlagPrerelease != 0,
	}

	// Prefer the human-authored string ("8.4.25", "22.22.2") over the
	// four-number composition, but only when it actually looks like one.
	for _, pair := range translations(block) {
		for _, field := range []string{"ProductVersion", "FileVersion"} {
			var text *uint16
			textSize := uint32(0)
			subBlock := fmt.Sprintf(`\StringFileInfo\%04x%04x\%s`, pair[0], pair[1], field)
			if err := windows.VerQueryValue(unsafe.Pointer(&block[0]), subBlock, unsafe.Pointer(&text), &textSize); err != nil || text == nil || textSize == 0 {
				continue
			}
			value := strings.TrimSpace(windows.UTF16PtrToString(text))
			if looksLikeVersion(value) {
				meta.FullVersion = value
				return meta, nil
			}
		}
	}
	return meta, nil
}

// translations lists the (language, codepage) pairs present in the block's
// VarFileInfo\Translation table.
func translations(block []byte) [][2]uint16 {
	var table *uint16
	tableSize := uint32(0)
	if err := windows.VerQueryValue(unsafe.Pointer(&block[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&table), &tableSize); err != nil || table == nil || tableSize < 4 {
		return nil
	}
	pairs := make([][2]uint16, 0, tableSize/4)
	raw := unsafe.Slice(table, tableSize/2)
	for i := 0; i+1 < len(raw); i += 2 {
		pairs = append(pairs, [2]uint16{raw[i], raw[i+1]})
	}
	return pairs
}

func numericProductVersion(fixed *windows.VS_FIXEDFILEINFO) string {
	major := fixed.ProductVersionMS >> 16
	minor := fixed.ProductVersionMS & 0xffff
	patch := fixed.ProductVersionLS >> 16
	rev := fixed.ProductVersionLS & 0xffff
	if rev == 0 {
		return fmt.Sprintf("%d.%d.%d", major, minor, patch)
	}
	return fmt.Sprintf("%d.%d.%d.%d", major, minor, patch, rev)
}

// looksLikeVersion accepts dot-numeric strings, optionally carrying a
// pre-release suffix ("8.6.0beta1", "9.7.0-rc2"): the numeric head decides.
func looksLikeVersion(value string) bool {
	end := 0
	for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.') {
		end++
	}
	head := value[:end]
	if head == "" || head[len(head)-1] == '.' || head[0] == '.' {
		return false
	}
	for _, part := range strings.Split(head, ".") {
		if part == "" {
			return false
		}
	}
	return true
}
