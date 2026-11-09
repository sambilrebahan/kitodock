// Package applicationcontext captures what a running local application
// looks like right now — Laravel routes and environment summary — into a
// durable JSONL log, and reads the local Mailpit inbox. It powers the
// kitodock_context and kitodock_mail_list tools.
package applicationcontext

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandRunner executes a command in a project directory; production
// resolves the project's PHP runtime, tests stub it.
type CommandRunner func(dir string, name string, args ...string) (string, error)

// Snapshot is one captured application context.
type Snapshot struct {
	Time     time.Time `json:"time"`
	Project  string    `json:"project"`
	Provider string    `json:"provider"`
	// Routes holds the parsed `artisan route:list --json` (when Laravel).
	Routes []map[string]any `json:"routes,omitempty"`
	// About holds the parsed `artisan about --json` summary.
	About map[string]any `json:"about,omitempty"`
	// Error carries a capture failure (snapshot still recorded).
	Error string `json:"error,omitempty"`
}

// Capture runs the Laravel probes in dir and returns the snapshot.
// phpBin is the PHP executable to use ("" = "php" from PATH).
func Capture(project, dir, phpBin string, run CommandRunner) (*Snapshot, error) {
	if dir == "" {
		return nil, fmt.Errorf("project directory is required")
	}
	if phpBin == "" {
		phpBin = "php"
	}
	snap := &Snapshot{Time: time.Now(), Project: project, Provider: "laravel"}

	routesJSON, routesErr := run(dir, phpBin, "artisan", "route:list", "--json")
	if routesErr == nil {
		_ = json.Unmarshal([]byte(strings.TrimSpace(routesJSON)), &snap.Routes)
	} else {
		snap.Error = "route:list: " + routesErr.Error()
	}
	aboutJSON, aboutErr := run(dir, phpBin, "artisan", "about", "--json")
	if aboutErr == nil {
		_ = json.Unmarshal([]byte(strings.TrimSpace(aboutJSON)), &snap.About)
	} else if snap.Error == "" {
		snap.Error = "about: " + aboutErr.Error()
	}
	return snap, nil
}

// AppendLog writes one snapshot to the project's JSONL capture log.
func AppendLog(storeDir, project string, snap *Snapshot) (string, error) {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return "", err
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	path := filepath.Join(storeDir, sanitize(project)+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return "", err
	}
	return path, nil
}

// ReadLog loads the newest-last snapshots from a capture log.
func ReadLog(storeDir, project string, limit int) ([]Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(storeDir, sanitize(project)+".jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s Snapshot
		if json.Unmarshal([]byte(line), &s) == nil {
			snaps = append(snaps, s)
		}
	}
	if limit > 0 && len(snaps) > limit {
		snaps = snaps[len(snaps)-limit:]
	}
	return snaps, nil
}

// sanitize makes a project name filename-safe.
func sanitize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// --- Mailpit -----------------------------------------------------------------

// MailSummary is one inbox message.
type MailSummary struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	// Tag names the Kitodock project the mail belongs to (via recipient
	// local-part convention), when derivable.
	Tag       string `json:"tag,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// FetchMailpit reads the inbox listing from a local Mailpit API.
type FetchMailpit struct {
	// BaseURL is Mailpit's HTTP API root (http://127.0.0.1:port).
	BaseURL string
	// HTTPGet fetches a URL; tests stub it. Nil uses net/http via
	// fetchDefault.
	HTTPGet func(url string) ([]byte, error)
}

// List returns the newest messages, capped at limit.
func (f *FetchMailpit) List(limit int) ([]MailSummary, error) {
	if f.BaseURL == "" {
		return nil, fmt.Errorf("mailpit is not running")
	}
	get := f.HTTPGet
	if get == nil {
		get = fetchDefault
	}
	body, err := get(fmt.Sprintf("%s/api/v1/messages?limit=%d", strings.TrimRight(f.BaseURL, "/"), clampLimit(limit)))
	if err != nil {
		return nil, fmt.Errorf("mailpit api: %w", err)
	}
	var doc struct {
		Messages []struct {
			ID        string                           `json:"ID"`
			From      struct{ Address, Name string }   `json:"From"`
			To        []struct{ Address, Name string } `json:"To"`
			Subject   string                           `json:"Subject"`
			CreatedAt time.Time                        `json:"Created"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("mailpit response: %w", err)
	}
	out := make([]MailSummary, 0, len(doc.Messages))
	for _, m := range doc.Messages {
		s := MailSummary{
			ID: m.ID, Subject: m.Subject, CreatedAt: m.CreatedAt.Format(time.RFC3339),
			From: addrOrName(m.From.Address, m.From.Name),
		}
		if len(m.To) > 0 {
			s.To = addrOrName(m.To[0].Address, m.To[0].Name)
		}
		s.Tag = projectTag(s.To)
		out = append(out, s)
	}
	return out, nil
}

func addrOrName(addr, name string) string {
	if name != "" {
		return name + " <" + addr + ">"
	}
	return addr
}

// projectTag derives a project tag from a project-convention mailbox
// (e.g. shop@mailpit.test → "shop"); empty when not derivable.
func projectTag(to string) string {
	at := strings.LastIndex(to, "<")
	if at >= 0 {
		to = to[at+1:]
		if gt := strings.Index(to, ">"); gt >= 0 {
			to = to[:gt]
		}
	}
	if at = strings.Index(to, "@"); at > 0 {
		return strings.ToLower(to[:at])
	}
	return ""
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 25
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// fetchDefault is the production HTTP getter.
func fetchDefault(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// execRunner is the production CommandRunner (direct exec, output only).
func execRunner(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ExecRunner exposes the production runner.
var ExecRunner CommandRunner = execRunner
