//go:build !windows

package devserver

import (
	"encoding/json"
	"os/exec"
)

func hide(*exec.Cmd) {}

func killTree(int) {}

func killPort(int, ...string) {}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
