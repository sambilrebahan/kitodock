// Package webtuning owns the managed main configuration of the nginx we
// run. The stock nginx.conf a fresh install ships with neither loads
// Kitodock's sites directory nor is safe to machine-edit, so the first
// start rewrites it into a small managed file (clearly marked), and the
// tuning API edits allowlisted directives inside it: worker sizing at the
// top level, connection/body limits inside the right blocks.
package webtuning

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// managedMark tags a conf Kitodock owns end to end.
const managedMark = "# kitodock managed nginx.conf — edits survive upgrades"

// Tunable directives and where they live.
var nginxAllowlist = map[string]string{
	// directive → context: "main", "events", "http"
	"worker_processes":              "main",
	"worker_connections":            "events",
	"client_max_body_size":          "http",
	"keepalive_timeout":             "http",
	"sendfile":                      "http",
	"server_names_hash_bucket_size": "http",
}

// IsManaged reports whether the conf at path is the managed one.
func IsManaged(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), managedMark)
}

// EnsureManagedConf rewrites a stock (or missing) nginx.conf into the
// managed layout. An already-managed conf is left untouched.
func EnsureManagedConf(path string) error {
	if IsManaged(path) {
		return nil
	}
	conf := managedMark + `
worker_processes auto;

events {
    worker_connections 1024;
}

http {
    include mime.types;
    include sites/*.conf;
}
`
	if err := os.MkdirAll(strings.TrimSuffix(path, `\`+lastSegment(path)), 0o755); err != nil {
		// The conf dir is created by the installer; ignore shape issues
		// and let the write surface real errors.
		_ = err
	}
	return os.WriteFile(path, []byte(conf), 0o644)
}

func lastSegment(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// Tuning reads every allowlisted directive currently in the conf.
// Missing directives report ""; the UI treats that as the default.
func Tuning(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for directive := range nginxAllowlist {
		re := directiveRe(directive)
		if match := re.FindStringSubmatch(string(data)); match != nil {
			out[directive] = match[1]
		} else {
			out[directive] = ""
		}
	}
	return out, nil
}

// SetTuning writes one allowlisted directive into its context block,
// creating the block when needed. Unknown keys and badly shaped values
// are refused — this file drives a live server.
func SetTuning(path, directive, value string) error {
	context, ok := nginxAllowlist[directive]
	if !ok {
		return fmt.Errorf("%q is not a tunable directive", directive)
	}
	if err := validateValue(directive, value); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	line := directive + " " + value + ";"

	re := directiveRe(directive)
	if re.MatchString(text) {
		text = re.ReplaceAllString(text, regexp.QuoteMeta(line))
		// ReplaceAllString with quoted replacement keeps literals safe.
		text = strings.ReplaceAll(text, regexp.QuoteMeta(line), line)
		return os.WriteFile(path, []byte(text), 0o644)
	}
	return os.WriteFile(path, []byte(insertInto(text, context, line)), 0o644)
}

// directiveRe matches the directive's active (uncommented) line anywhere;
// managed confs keep one occurrence per directive.
func directiveRe(directive string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(directive) + `[ \t]+([^;\n]+);`)
}

// insertInto places a directive line inside its context block, creating
// the block (or the http wrapper) when absent.
func insertInto(text, context, line string) string {
	indent := "    "
	if context == "main" {
		return strings.TrimRight(text, "\n") + "\n" + line + "\n"
	}
	if context == "events" {
		return insertInBlock(text, "events", line, indent)
	}
	// http-level: also inside http{}.
	return insertInBlock(text, "http", line, indent)
}

// insertInBlock drops the line right after the opening brace of the named
// block, appending a fresh block before http{} (or at EOF) when missing.
func insertInBlock(text, block, line, indent string) string {
	re := regexp.MustCompile(`(?m)^([ \t]*)` + block + `[ \t]*\{`)
	if loc := re.FindStringSubmatchIndex(text); loc != nil {
		at := loc[3] // end of the block keyword line's opening brace
		insert := "\n" + indent + line
		return text[:at] + insert + text[at:]
	}
	fresh := "\n" + block + " {\n" + indent + line + "\n}\n"
	// Prefer parking a new events{} before http{}; http{} goes last.
	if block == "events" {
		if i := strings.Index(text, "http {"); i >= 0 {
			return text[:i] + strings.TrimLeft(fresh, "\n") + "\n" + text[i:]
		}
	}
	return strings.TrimRight(text, "\n") + "\n" + fresh
}

// validateValue keeps sizes numeric-with-unit and flags on/off.
func validateValue(directive, value string) error {
	v := strings.TrimSpace(value)
	switch directive {
	case "sendfile":
		if v != "on" && v != "off" {
			return fmt.Errorf("sendfile must be on or off")
		}
	case "worker_processes":
		if v != "auto" {
			if _, err := strconv.Atoi(v); err != nil {
				return fmt.Errorf("worker_processes must be a number or auto")
			}
		}
	case "worker_connections", "server_names_hash_bucket_size":
		if _, err := strconv.Atoi(v); err != nil {
			return fmt.Errorf("%s must be a number", directive)
		}
	default: // size/time values like 64m, 2m, 65
		if !regexp.MustCompile(`^[0-9]+[kKmMgG]?$|^[0-9]+[smh]?$`).MatchString(v) {
			return fmt.Errorf("%q is not a size or duration", value)
		}
	}
	return nil
}
