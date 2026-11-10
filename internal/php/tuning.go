package php

// php.ini tuning for the common knobs a local dev box wants: memory and
// upload ceilings plus execution time. The allowlist is deliberate — the
// ini drives every pool of the series, so only well-understood directives
// get a write path.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// phpTunable lists the directives the tuning API may read and write.
var phpTunable = []string{
	"memory_limit",
	"upload_max_filesize",
	"post_max_size",
	"max_execution_time",
	"max_input_time",
	"opcache.enable",
}

// Tuning reads the tunable directives of a series' php.ini. Missing
// directives report "" (PHP defaults apply).
func (m *Manager) Tuning(series string) (map[string]string, error) {
	iniPath, err := m.iniPath(series)
	if err != nil {
		return nil, err
	}
	lines, err := readIniLines(iniPath)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, directive := range phpTunable {
		out[directive] = findDirective(lines, directive)
	}
	return out, nil
}

// SetTuning writes one tunable directive. Values are validated per key.
func (m *Manager) SetTuning(series, directive, value string) error {
	if !phpTunableContains(directive) {
		return fmt.Errorf("%q is not a tunable directive", directive)
	}
	if err := validatePhpValue(directive, value); err != nil {
		return err
	}
	iniPath, err := m.iniPath(series)
	if err != nil {
		return err
	}
	lines, err := readIniLines(iniPath)
	if err != nil {
		return err
	}
	lines = setDirectiveLine(lines, directive, value)
	return writeIniLines(iniPath, lines)
}

// iniPath resolves a series' php.ini with a clear error.
func (m *Manager) iniPath(series string) (string, error) {
	dir := m.DirFor(series)
	if dir == "" {
		return "", fmt.Errorf("PHP %s is not installed", series)
	}
	return filepath.Join(dir, "php.ini"), nil
}

// findDirective returns the active value of one directive ("" = unset).
func findDirective(lines []string, directive string) string {
	re := directiveIniRe(directive)
	for _, line := range lines {
		if match := re.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	return ""
}

// setDirectiveLine activates/replaces one directive, preserving position.
func setDirectiveLine(lines []string, directive, value string) []string {
	re := directiveIniRe(directive)
	entry := directive + " = " + value
	for i, line := range lines {
		if re.MatchString(line) {
			lines[i] = entry
			return lines
		}
	}
	// Not present (not even commented): append in the module settings
	// tail area — plain append is fine, php.ini order is not significant
	// for these keys.
	return append(lines, entry)
}

func directiveIniRe(directive string) *regexp.Regexp {
	return regexp.MustCompile(`^[ \t]*;?[ \t]*` + regexp.QuoteMeta(directive) + `[ \t]*=[ \t]*([^\r\n;]+)`)
}

func phpTunableContains(directive string) bool {
	for _, key := range phpTunable {
		if key == directive {
			return true
		}
	}
	return false
}

// validatePhpValue enforces shapes per directive family.
func validatePhpValue(directive, value string) error {
	v := trimIniValue(value)
	switch directive {
	case "opcache.enable":
		if v != "1" && v != "0" {
			return fmt.Errorf("opcache.enable must be 1 or 0")
		}
	case "max_execution_time", "max_input_time":
		if !regexp.MustCompile(`^-?[0-9]+$`).MatchString(v) {
			return fmt.Errorf("%s must be a number of seconds", directive)
		}
	default: // sizes like 512M, 64m, 2G
		if !regexp.MustCompile(`^[0-9]+[kKmMgG]?$`).MatchString(v) {
			return fmt.Errorf("%q is not a size (e.g. 256M)", value)
		}
	}
	return nil
}

func trimIniValue(value string) string {
	// Accept trailing ini comments the way users paste them.
	if i := strings.IndexByte(value, ';'); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}
