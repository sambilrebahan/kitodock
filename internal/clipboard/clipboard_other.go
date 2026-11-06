//go:build !windows

package clipboard

func ReadText() (string, error) { return "", ErrUnavailable }

func WriteText(string) error { return ErrUnavailable }
