package notifications

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func itemAt(id string, at time.Time) Item {
	return Item{ID: id, Kind: KindFinished, Title: "t-" + id, At: at}
}

func opened(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "feed.json")
	store, notice, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if notice != nil {
		t.Fatalf("a fresh feed reported a notice: %+v", notice)
	}
	return store, path
}

func TestAppendPersistsAndItemsSortsNewestFirst(t *testing.T) {
	store, path := opened(t)
	base := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

	if _, added, err := store.Append(itemAt("early", base)); err != nil || !added {
		t.Fatalf("first Append = (_, %v, %v), want added", added, err)
	}
	if _, added, err := store.Append(itemAt("late", base.Add(time.Minute))); err != nil || !added {
		t.Fatalf("second Append = (_, %v, %v), want added", added, err)
	}

	feed := store.Items()
	if len(feed) != 2 || feed[0].ID != "late" || feed[1].ID != "early" {
		t.Fatalf("Items() = [%s %s], want newest first", feed[0].ID, feed[1].ID)
	}

	reloaded, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Items(); len(got) != 2 {
		t.Fatalf("reloaded feed has %d items, want 2", len(got))
	}
}

func TestFingerprintSuppressesRepeats(t *testing.T) {
	store, _ := opened(t)
	first := itemAt("one", time.Now())
	first.fingerprint = "session-1:waiting"
	repeat := itemAt("two", time.Now().Add(time.Second))
	repeat.fingerprint = "session-1:waiting"

	if _, added, err := store.Append(first); err != nil || !added {
		t.Fatalf("first Append = (_, %v, %v), want added", added, err)
	}
	returned, added, err := store.Append(repeat)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("a repeated fingerprint must not append a second item")
	}
	if returned.ID != "one" {
		t.Fatalf("the repeat returned %q, want the original item", returned.ID)
	}
	if store.Unread() != 1 {
		t.Fatalf("Unread() = %d, want 1", store.Unread())
	}
}

func TestFeedStaysWithinItsLimit(t *testing.T) {
	store, _ := opened(t)
	start := time.Now().Add(-time.Hour)
	for i := 0; i < feedLimit+30; i++ {
		item := itemAt(time.Now().Local().Format("150405.000000000"), start.Add(time.Duration(i)*time.Second))
		item.ID = item.ID + "-" + time.Unix(int64(i), 0).Format("0000")
		if _, _, err := store.Append(item); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(store.Items()); got != feedLimit {
		t.Fatalf("feed grew to %d, want the %d cap", got, feedLimit)
	}
}

func TestReadFlags(t *testing.T) {
	store, _ := opened(t)
	now := time.Now()
	if _, _, err := store.Append(itemAt("a", now)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Append(itemAt("b", now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}

	if err := store.MarkRead("a"); err != nil {
		t.Fatal(err)
	}
	if store.Unread() != 1 {
		t.Fatalf("Unread() after one read = %d, want 1", store.Unread())
	}
	if err := store.MarkAllRead(); err != nil {
		t.Fatal(err)
	}
	if store.Unread() != 0 {
		t.Fatal("MarkAllRead left unread items behind")
	}
}

func TestDeliveryRoundTrips(t *testing.T) {
	store, path := opened(t)

	mine := Delivery{OnFinished: true}
	if err := store.SetDelivery(mine); err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Delivery(); got != mine {
		t.Fatalf("delivery after reload = %+v, want %+v", got, mine)
	}
}

// An unreadable feed must never take the app down with it: the file is set
// aside under a derivable name and the feed restarts empty.
func TestUnreadableFeedIsSetAsideAndRestarted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, notice, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if notice == nil {
		t.Fatal("an unreadable file must produce a notice")
	}
	if notice.SidecarPath == path {
		t.Fatalf("SidecarPath = %q, want a sibling copy", notice.SidecarPath)
	}
	if _, err := os.Stat(notice.SidecarPath); err != nil {
		t.Fatalf("the sidecar copy does not exist: %v", err)
	}
	if len(store.Items()) != 0 {
		t.Fatal("the feed must restart empty")
	}

	// And the rewritten feed is valid JSON from the first persist onwards.
	if _, _, err := store.Append(itemAt("after", time.Now())); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the rewritten feed is not valid JSON: %v", err)
	}
}

func TestForeignVersionIsRefusedNotSilentlyDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.json")
	if err := os.WriteFile(path, []byte(`{"version":42,"items":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	store, notice, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if notice == nil {
		t.Fatal("a future schema must produce a notice, not silent loss")
	}
	if len(store.Items()) != 0 {
		t.Fatal("items from a foreign schema must not load")
	}
}
