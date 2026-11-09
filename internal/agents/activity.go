package agents

// Event spool + activity feed. The `--agent-event` CLI personality must be
// cheap: it appends one JSON line to the spool and exits. The GUI drains
// the spool on a short ticker, turns lines into activity entries, and
// keeps a bounded ring plus a durable JSONL log for restart continuity.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SpoolEvent is the envelope hooks write and the CLI forwards.
type SpoolEvent struct {
	Time     time.Time    `json:"time"`
	Provider ProviderKind `json:"provider"`
	Event    HookEvent    `json:"event"`
	// SessionID carries the provider-side session id when known.
	SessionID string `json:"sessionId,omitempty"`
	// Summary is a short human hint from the hook payload.
	Summary string `json:"summary,omitempty"`
	// Payload keeps the raw hook input for debugging.
	Payload string `json:"payload,omitempty"`
}

// Activity is one spooled event after ingestion, enriched with our own
// session linkage.
type Activity struct {
	Time       time.Time    `json:"time"`
	Provider   ProviderKind `json:"provider"`
	Event      HookEvent    `json:"event"`
	SessionID  string       `json:"sessionId,omitempty"`
	KitodockID string       `json:"kitodockId,omitempty"`
	Summary    string       `json:"summary,omitempty"`
}

// maxActivity caps the in-memory ring and per-drain batch.
const (
	maxActivity   = 500
	maxDrainBatch = 100
)

// ActivityLog is the bounded feed plus its durable JSONL log.
type ActivityLog struct {
	mu      sync.Mutex
	ring    []Activity
	logPath string
	// Link maps provider session ids to Kitodock session ids; set by the
	// manager whenever a transcript id is adopted.
	link func(providerSession string) string
}

// NewActivityLog resumes from the JSONL log's tail (last maxActivity).
func NewActivityLog(logPath string) *ActivityLog {
	a := &ActivityLog{logPath: logPath}
	if f, err := os.Open(logPath); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var entry Activity
			if json.Unmarshal(scanner.Bytes(), &entry) == nil {
				a.ring = append(a.ring, entry)
			}
		}
		f.Close()
		if over := len(a.ring) - maxActivity; over > 0 {
			a.ring = append([]Activity{}, a.ring[over:]...)
		}
	}
	return a
}

// SetLink installs the provider→kitodock session resolver.
func (a *ActivityLog) SetLink(fn func(providerSession string) string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.link = fn
}

// Spool appends one event to the spool dir (the CLI personality's only
// job). dir is typically state/agents/spool.
func Spool(dir string, ev SpoolEvent) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s.jsonl", ev.Time.UnixNano(), ev.Provider)
	return os.WriteFile(filepath.Join(dir, name), data, 0o600)
}

// Drain reads and removes spooled events, appending them to the feed.
// Events whose provider session links to a known Kitodock session are
// annotated with its id.
func (a *ActivityLog) Drain(spoolDir string) []Activity {
	files, err := os.ReadDir(spoolDir)
	if err != nil {
		return nil
	}
	var ingested []Activity
	for i, f := range files {
		if i >= maxDrainBatch {
			break // keep drains bounded; the rest waits for the next tick
		}
		path := filepath.Join(spoolDir, f.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ev SpoolEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			// Unparseable event: drop it so it cannot wedge the spool.
			os.Remove(path)
			continue
		}
		os.Remove(path)
		entry := Activity{
			Time: ev.Time, Provider: ev.Provider, Event: ev.Event,
			SessionID: ev.SessionID, Summary: ev.Summary,
		}
		if ev.SessionID != "" && a.link != nil {
			if kit := a.link(ev.SessionID); kit != "" {
				entry.KitodockID = kit
			}
		}
		a.append(entry)
		ingested = append(ingested, entry)
	}
	return ingested
}

// append stores one entry in the ring and durable log.
func (a *ActivityLog) append(entry Activity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ring = append(a.ring, entry)
	if over := len(a.ring) - maxActivity; over > 0 {
		a.ring = append([]Activity{}, a.ring[over:]...)
	}
	if a.logPath != "" {
		if data, err := json.Marshal(entry); err == nil {
			if err := os.MkdirAll(filepath.Dir(a.logPath), 0o755); err == nil {
				f, err := os.OpenFile(a.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err == nil {
					f.Write(append(data, '\n'))
					f.Close()
				}
			}
		}
	}
}

// Feed returns the recent activity, newest first.
func (a *ActivityLog) Feed(limit int) []Activity {
	a.mu.Lock()
	defer a.mu.Unlock()
	if limit <= 0 || limit > len(a.ring) {
		limit = len(a.ring)
	}
	out := make([]Activity, limit)
	for i := 0; i < limit; i++ {
		out[i] = a.ring[len(a.ring)-1-i]
	}
	return out
}

// StartDrainLoop pumps the spool every interval until stop closes.
func (a *ActivityLog) StartDrainLoop(spoolDir string, interval time.Duration, onEvents func([]Activity)) (stop func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if events := a.Drain(spoolDir); len(events) > 0 && onEvents != nil {
					onEvents(events)
				}
			}
		}
	}()
	return func() { close(done) }
}
