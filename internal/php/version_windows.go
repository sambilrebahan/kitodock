//go:build windows

package php

import "kitodock/internal/runtimeinventory"

func runtimeRead(exe string) (string, error) {
	meta, err := runtimeinventory.Read(exe)
	if err != nil {
		return "", err
	}
	return meta.FullVersion, nil
}
