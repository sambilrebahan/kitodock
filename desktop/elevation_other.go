//go:build !windows || bindings

package main

func relaunchElevated([]string) (bool, error) { return false, nil }
