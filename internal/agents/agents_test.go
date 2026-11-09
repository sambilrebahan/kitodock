package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSpawner records launches without touching real PTYs.
type fakeSpawner struct {
	lastExe  string
	lastArgs []string
	lastDir  string
	calls    int
}

func (f *fakeSpawner) Start(provider ProviderKind, exe string, args []string, dir string) (string, error) {
	f.calls++
	f.lastExe, f.lastArgs, f.lastDir = exe, args, dir
	return "pty-1", nil
}

func TestCatalogHasFourProviders(t *testing.T) {
	cat := Catalog()
	if len(cat) != 4 {
		t.Fatalf("catalog = %d providers, want 4", len(cat))
	}
	kinds := map[ProviderKind]bool{}
	for _, p := range cat {
		kinds[p.Kind] = true
		if p.Exe == "" || p.Label == "" {
			t.Fatalf("incomplete provider %+v", p)
		}
	}
	for _, want := range []ProviderKind{Claude, Codex, Gemini, Copilot} {
		if !kinds[want] {
			t.Errorf("missing provider %s", want)
		}
	}
	// Copilot honestly reports no hooks.
	if p, _ := ProviderByKind(Copilot); p.HookStyle != HookStyleNone {
		t.Errorf("copilot hook style = %q", p.HookStyle)
	}
}

func TestProbeVersionAbsentProvider(t *testing.T) {
	// An executable that cannot exist: "not installed", not an error.
	ghost := Provider{Kind: "ghost", Exe: "kitodock-no-such-cli-xyz", ProbeArgs: []string{"--version"}}
	if v, err := ProbeVersion(context.Background(), ghost); err != nil || v != "" {
		t.Fatalf("probe of absent exe = %q err=%v", v, err)
	}
	// A provider without probe args reports "installed, version unknown".
	calm := Provider{Kind: "calm", Exe: "cmd"}
	if v, err := ProbeVersion(context.Background(), calm); err != nil || v != "" {
		t.Fatalf("probe without args = %q err=%v", v, err)
	}
}

func TestManagerLaunchResumePersist(t *testing.T) {
	dir := t.TempDir()
	spawner := &fakeSpawner{}
	m, err := NewManager(spawner, filepath.Join(dir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}

	s, err := m.Launch(Claude, "shop", filepath.Join(dir, "www", "shop"), "")
	if err != nil {
		t.Fatal(err)
	}
	if spawner.lastExe != "claude" || spawner.lastDir != filepath.Join(dir, "www", "shop") {
		t.Fatalf("spawn = %s %v", spawner.lastExe, spawner.lastDir)
	}
	if s.PtyID != "pty-1" || s.Title != "shop" {
		t.Fatalf("session = %+v", s)
	}

	// Resume adds the provider's resume args before the id.
	if _, err := m.Launch(Codex, "", dir, "abc123"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(spawner.lastArgs, " ") != "resume abc123" {
		t.Fatalf("resume args = %v", spawner.lastArgs)
	}
	// Copilot resume is refused honestly.
	if _, err := m.Launch(Copilot, "", dir, "x"); err == nil {
		t.Fatal("copilot resume accepted")
	}

	// Persistence round-trips; PTY ids never survive a restart.
	m.MarkProviderSession(s.ID, "prov-sess-1")
	m.Rename(s.ID, "checkout-flow")
	m2, err := NewManager(spawner, filepath.Join(dir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	list := m2.List(true)
	if len(list) != 2 {
		t.Fatalf("persisted sessions = %d", len(list))
	}
	var reloaded Session
	for _, x := range list {
		if x.ID == s.ID {
			reloaded = x
		}
	}
	if reloaded.PtyID != "" || reloaded.ProviderSession != "prov-sess-1" || reloaded.Title != "checkout-flow" {
		t.Fatalf("reloaded = %+v", reloaded)
	}

	// Archive filters the default list; Delete forgets.
	m2.Archive(s.ID, true)
	if len(m2.List(false)) != 1 || len(m2.List(true)) != 2 {
		t.Fatalf("archive filtering broken")
	}
	if err := m2.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if len(m2.List(true)) != 1 {
		t.Fatal("delete left the session behind")
	}
}

func TestDiscoverSessionsParsesTranscripts(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "shop-slug")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := `{"type":"user","cwd":"C:\\www\\shop","message":"fix the checkout bug"}
{"type":"assistant","sessionId":"sess-42"}
{"type":"user","sessionId":"sess-42","message":"and add tests"}`
	if err := os.WriteFile(filepath.Join(proj, "a.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unparsable sibling must not break the scan.
	os.WriteFile(filepath.Join(proj, "b.jsonl"), []byte("not json\n"), 0o600)

	found, err := DiscoverSessions(Claude, home, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("discovered = %+v", found)
	}
	var main DiscoveredSession
	for _, f := range found {
		if strings.HasSuffix(f.Path, "a.jsonl") {
			main = f
		}
	}
	if main.ID != "sess-42" {
		t.Errorf("session id = %q, want sess-42", main.ID)
	}
	if main.DirHint != "C:\\www\\shop" {
		t.Errorf("dir hint = %q", main.DirHint)
	}
	if main.Summary != "fix the checkout bug" {
		t.Errorf("summary = %q", main.Summary)
	}
}

func TestHooksInstallRemoveSettingsJSON(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	spec := HookSpec{
		Command: `"C:\apps\kd\kitodock.exe" --agent-event claude`,
		Events:  []HookEvent{EventSessionStart, EventTurnStop},
	}
	if err := InstallHooks(Claude, home, spec); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	doc := string(data)
	if !strings.Contains(doc, `"Stop"`) || !strings.Contains(doc, hookMarker) ||
		!strings.Contains(doc, `--agent-event`) {
		t.Fatalf("hooks not written: %s", doc)
	}
	if !strings.Contains(doc, `"theme": "dark"`) {
		t.Fatalf("existing settings clobbered: %s", doc)
	}
	// The untouched original is backed up once.
	bak, err := os.ReadFile(path + ".kitodock-bak")
	if err != nil || strings.TrimSpace(string(bak)) != `{"theme":"dark"}` {
		t.Fatalf("backup = %q err=%v", bak, err)
	}

	// Re-install is idempotent (no duplicated managed entries).
	if err := InstallHooks(Claude, home, spec); err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(mustRead(t, path)), hookMarker) != 2 {
		t.Fatalf("managed entries duplicated: %s", mustRead(t, path))
	}

	if err := RemoveHooks(Claude, home); err != nil {
		t.Fatal(err)
	}
	after := mustRead(t, path)
	if strings.Contains(after, hookMarker) || strings.Contains(after, `"Stop"`) {
		t.Fatalf("hooks not removed: %s", after)
	}
	// Removal restores the untouched backup — the original compact form.
	if !strings.Contains(after, `"theme":"dark"`) {
		t.Fatalf("removal lost user settings: %s", after)
	}
}

func TestHooksInstallRemoveConfigTOML(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// The original carries no notify line: what Kitodock adds must be
	// fully removable again.
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec := HookSpec{
		Command: `kitodock.exe --agent-event codex`,
		Events:  []HookEvent{EventTurnStop},
	}
	if err := InstallHooks(Codex, home, spec); err != nil {
		t.Fatal(err)
	}
	doc := mustRead(t, path)
	if !strings.Contains(doc, `notify = ["kitodock.exe", "--agent-event", "codex"]`) {
		t.Fatalf("notify line = %s", doc)
	}
	if !strings.Contains(doc, `model = "gpt-5"`) {
		t.Fatalf("toml edit clobbered: %s", doc)
	}

	if err := RemoveHooks(Codex, home); err != nil {
		t.Fatal(err)
	}
	after := mustRead(t, path)
	if strings.Contains(after, "notify") || strings.Contains(after, tomlMarker) {
		t.Fatalf("notify not removed: %s", after)
	}
	if !strings.Contains(after, `model = "gpt-5"`) {
		t.Fatalf("removal lost config: %s", after)
	}
}

func TestHooksUnsupportedProvider(t *testing.T) {
	if err := InstallHooks(Copilot, t.TempDir(), HookSpec{Command: "x"}); err == nil {
		t.Fatal("copilot hooks accepted")
	}
}

func TestSpoolAndActivityDrain(t *testing.T) {
	base := t.TempDir()
	spoolDir := filepath.Join(base, "spool")
	logPath := filepath.Join(base, "activity.jsonl")

	if err := Spool(spoolDir, SpoolEvent{
		Provider: Claude, Event: EventTurnStop,
		SessionID: "prov-1", Summary: "turn done",
	}); err != nil {
		t.Fatal(err)
	}
	if err := Spool(spoolDir, SpoolEvent{Provider: Codex, Event: EventSessionStart}); err != nil {
		t.Fatal(err)
	}

	feed := NewActivityLog(logPath)
	feed.SetLink(func(providerSession string) string {
		if providerSession == "prov-1" {
			return "ag1"
		}
		return ""
	})
	ingested := feed.Drain(spoolDir)
	if len(ingested) != 2 {
		t.Fatalf("ingested = %d", len(ingested))
	}
	if ingested[0].KitodockID != "ag1" || ingested[0].Summary != "turn done" {
		t.Fatalf("linked entry = %+v", ingested[0])
	}
	// Spool drained: second pass is empty.
	if again := feed.Drain(spoolDir); len(again) != 0 {
		t.Fatalf("drain not clean: %d", len(again))
	}
	// Feed is newest-first and bounded.
	if got := feed.Feed(10); len(got) != 2 || got[0].Time.Before(got[1].Time) {
		t.Fatalf("feed = %+v", got)
	}
	// Durable log resumes a fresh feed.
	if fresh := NewActivityLog(logPath); len(fresh.Feed(0)) != 2 {
		t.Fatalf("log replay = %d entries", len(fresh.Feed(0)))
	}
	// Corrupt spool files are dropped, not fatal.
	os.WriteFile(filepath.Join(spoolDir, "junk.jsonl"), []byte("{nope"), 0o600)
	if dropped := feed.Drain(spoolDir); len(dropped) != 0 {
		t.Fatalf("corrupt spool ingested: %+v", dropped)
	}
	if _, err := os.Stat(filepath.Join(spoolDir, "junk.jsonl")); !os.IsNotExist(err) {
		t.Fatal("corrupt spool file not removed")
	}
	_ = time.Now
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
