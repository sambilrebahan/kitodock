// Package notifications persists the in-app notification feed: a bounded
// list of items plus the user's delivery preferences. This file is only the
// store; the observers that turn agent sessions and usage into items arrive
// with the agents phase.
package notifications

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"kitodock/internal/atomicfile"
	"kitodock/internal/i18n"
)

// feedLimit caps how many items are kept; the newest win.
const feedLimit = 200

// Kind says why an item exists, which drives filtering and preferences.
type Kind string

const (
	// KindFinished: a background task completed.
	KindFinished Kind = "finished"
	// KindWaiting: something is blocked waiting for the user.
	KindWaiting Kind = "waiting"
	// KindFailed: something errored out.
	KindFailed Kind = "failed"
	// KindQuota: a usage limit is close or crossed.
	KindQuota Kind = "quota"
)

// Scope names what an item is about; any part may be empty.
type Scope struct {
	Project  string `json:"project,omitempty"`
	Session  string `json:"session,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Item is one notification.
type Item struct {
	ID     string    `json:"id"`
	Kind   Kind      `json:"kind"`
	Title  string    `json:"title"`
	Detail string    `json:"detail,omitempty"`
	Scope  Scope     `json:"scope"`
	At     time.Time `json:"at"`
	Read   bool      `json:"read"`
	// fingerprint suppresses repeats of the same event; never serialized.
	fingerprint string `json:"-"`
}

// Delivery is the user's delivery toggles.
type Delivery struct {
	Toast      bool `json:"toast"`
	Sound      bool `json:"sound"`
	OnFinished bool `json:"onFinished"`
	OnWaiting  bool `json:"onWaiting"`
	OnFailed   bool `json:"onFailed"`
	OnQuota    bool `json:"onQuota"`
}

// DefaultDelivery turns everything on: silence is a choice, not a default.
func DefaultDelivery() Delivery {
	return Delivery{Toast: true, Sound: true, OnFinished: true, OnWaiting: true, OnFailed: true, OnQuota: true}
}

// Notice reports a feed file that could not be trusted: Message is
// user-facing and SidecarPath names the untouched copy kept aside.
type Notice struct {
	Message     string
	SidecarPath string
}

type storedItem struct {
	Item
	Fingerprint string `json:"fingerprint"`
}

type document struct {
	Version  int          `json:"version"`
	Items    []storedItem `json:"items"`
	Delivery Delivery     `json:"delivery"`
}

// Store owns the feed file.
type Store struct {
	mu    sync.RWMutex
	path  string
	items []storedItem
	how   Delivery
	now   func() time.Time
}

// Open loads (or starts) the feed at path.
func Open(path string) (*Store, *Notice, error) {
	s := &Store{path: path, how: DefaultDelivery(), now: time.Now}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	notice, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	return s, notice, nil
}

func (s *Store) load() (*Notice, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc document
	problem := json.Unmarshal(raw, &doc)
	if problem == nil && doc.Version != 1 {
		problem = i18n.Fail("errors.notifications.unsupportedSchema",
			map[string]any{"version": doc.Version})
	}
	if problem != nil {
		sidecar := s.path + ".unreadable-" + s.now().UTC().Format("20060102T150405Z")
		if renameErr := os.Rename(s.path, sidecar); renameErr != nil {
			return nil, i18n.FailWrap(problem, "errors.notifications.decodeFailed",
				map[string]any{"renameError": renameErr.Error()})
		}
		return &Notice{
			Message:     i18n.Text("backend.notifications.historyMalformed", nil),
			SidecarPath: sidecar,
		}, nil
	}
	s.items = doc.Items
	s.how = doc.Delivery
	return nil, nil
}

// Items returns the feed newest-first.
func (s *Store) Items() []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, len(s.items))
	for i, stored := range s.items {
		out[i] = stored.Item
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// Unread is how many items still carry the unread flag.
func (s *Store) Unread() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, stored := range s.items {
		if !stored.Read {
			count++
		}
	}
	return count
}

// Append adds an item unless an earlier one carried the same fingerprint, in
// which case the earlier item comes back with added=false. A failed persist
// rewinds the feed.
func (s *Store) Append(item Item) (Item, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.fingerprint != "" {
		for _, stored := range s.items {
			if stored.Fingerprint == item.fingerprint {
				return stored.Item, false, nil
			}
		}
	}
	before := append([]storedItem(nil), s.items...)
	mark := item.fingerprint
	item.fingerprint = ""
	s.items = append(s.items, storedItem{Item: item, Fingerprint: mark})
	sort.SliceStable(s.items, func(i, j int) bool { return s.items[i].At.After(s.items[j].At) })
	if extra := len(s.items) - feedLimit; extra > 0 {
		s.items = s.items[:len(s.items)-extra]
	}
	if err := s.persist(); err != nil {
		s.items = before
		return Item{}, false, err
	}
	return item, true, nil
}

// MarkRead flags one item read.
func (s *Store) MarkRead(id string) error {
	return s.adjust(func(items []storedItem) bool {
		for i := range items {
			if items[i].ID == id && !items[i].Read {
				items[i].Read = true
				return true
			}
		}
		return false
	})
}

// MarkAllRead flags the whole feed read.
func (s *Store) MarkAllRead() error {
	return s.adjust(func(items []storedItem) bool {
		changed := false
		for i := range items {
			if !items[i].Read {
				items[i].Read = true
				changed = true
			}
		}
		return changed
	})
}

func (s *Store) adjust(edit func([]storedItem) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := append([]storedItem(nil), s.items...)
	if !edit(s.items) {
		return nil
	}
	if err := s.persist(); err != nil {
		s.items = before
		return err
	}
	return nil
}

// Delivery returns the delivery toggles.
func (s *Store) Delivery() Delivery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.how
}

// SetDelivery stores the delivery toggles, rewinding on failure.
func (s *Store) SetDelivery(how Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.how
	s.how = how
	if err := s.persist(); err != nil {
		s.how = before
		return err
	}
	return nil
}

func (s *Store) persist() error {
	raw, err := json.MarshalIndent(document{Version: 1, Items: s.items, Delivery: s.how}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, append(raw, '\n'), 0o644)
}
