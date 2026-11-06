//go:build tools

// Pin the dependencies needed to build the Wails CLI from this module's own
// graph (the workflow docs/development follows: never `go install` a version
// that can drift from go.mod). go mod tidy keeps these because it considers
// imports across every build tag except ignore.
package tools

import (
	_ "github.com/wailsapp/wails/v2/cmd/wails"
	_ "github.com/wailsapp/wails/v2/pkg/templates"
)
