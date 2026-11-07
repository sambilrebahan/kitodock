//go:build windows

package nodejs

import (
	"fmt"
	"regexp"

	"kitodock/internal/runtimeinventory"
)

var looseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+`)

func versionFromExecutable(exe string) (string, error) {
	meta, err := runtimeinventory.Read(exe)
	if err != nil {
		return "", err
	}
	if match := looseVersion.FindString(meta.FullVersion); match != "" {
		return match, nil
	}
	return "", fmt.Errorf("unrecognized node version %q", meta.FullVersion)
}
