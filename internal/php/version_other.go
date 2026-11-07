//go:build !windows

package php

import "errors"

func runtimeRead(string) (string, error) { return "", errors.New("no version resources off Windows") }
