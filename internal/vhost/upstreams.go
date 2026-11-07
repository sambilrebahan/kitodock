package vhost

// The managed PHP upstream block inside nginx.conf: one upstream per
// installed series, one server line per pool worker. Rewritten wholesale
// inside sentinel comments, so the block is idempotent and removable.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	upstreamBegin = "# >>> kitodock php upstreams"
	upstreamEnd   = "# <<< kitodock php upstreams"
)

// Upstream describes one series' php-cgi pool endpoints.
type Upstream struct {
	Name  string // nginx upstream name (php_8_4)
	Ports []int  // one per pool worker, in order
}

// EnsurePhpUpstreams rewrites the managed block in nginx.conf to exactly
// the given set, creating or replacing as needed.
func EnsurePhpUpstreams(nginxConf string, upstreams []Upstream) error {
	data, err := os.ReadFile(nginxConf)
	if err != nil {
		return err
	}
	text := string(data)

	block := upstreamBegin + "\n"
	for _, upstream := range upstreams {
		block += "upstream " + upstream.Name + " {\n"
		for _, port := range upstream.Ports {
			block += fmt.Sprintf("    server 127.0.0.1:%d;\n", port)
		}
		block += "}\n"
	}
	block += upstreamEnd

	re := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(upstreamBegin) + `.*?` + regexp.QuoteMeta(upstreamEnd))
	var updated string
	if re.MatchString(text) {
		updated = re.ReplaceAllLiteralString(text, block)
	} else {
		// First run: the block goes inside http{} — after the last brace
		// of the events block would be wrong; simplest correct anchor is
		// the start of the http block's body.
		updated = insertIntoHTTP(text, block)
	}
	if updated == text {
		return nil
	}
	return os.WriteFile(nginxConf, []byte(updated), 0o644)
}

// insertIntoHTTP drops the block right after the opening of the http
// block, creating http{} when the conf is minimal.
func insertIntoHTTP(text, block string) string {
	re := regexp.MustCompile(`(?m)^\s*http\s*\{`)
	if loc := re.FindStringIndex(text); loc != nil {
		at := loc[1]
		return text[:at] + "\n" + block + text[at:]
	}
	return text + "\nhttp {\n" + block + "}\n"
}

// ExtractUpstreamServers lists the endpoints of one upstream in
// nginx.conf, for tests and diagnostics.
func ExtractUpstreamServers(nginxConf, name string) []int {
	data, err := os.ReadFile(nginxConf)
	if err != nil {
		return nil
	}
	text := string(data)
	re := regexp.MustCompile(`(?s)upstream ` + regexp.QuoteMeta(name) + ` \{(.*?)\}`)
	match := re.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	var ports []int
	for _, line := range strings.Split(match[1], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "server 127.0.0.1:") {
			continue
		}
		port := 0
		fmt.Sscanf(line, "server 127.0.0.1:%d;", &port)
		if port > 0 {
			ports = append(ports, port)
		}
	}
	return ports
}
