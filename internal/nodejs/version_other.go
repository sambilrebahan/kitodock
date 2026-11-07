//go:build !windows

package nodejs

import "errors"

func versionFromExecutable(string) (string, error) {
	return "", errors.New("no version resources off Windows")
}
