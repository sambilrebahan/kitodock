package controllers

// The notifications controller: the in-app inbox behind the sidebar bell.
// Anything user-visible worth remembering (a service that died, a deploy
// that failed) lands here; items are deduplicated and marked read from
// the UI. The store has lived since Phase 0 — this wires it up.

import (
	"path/filepath"
	"sync"
	"time"

	"kitodock/internal/notifications"
	"kitodock/internal/paths"
)

// Notifications owns the inbox store.
type Notifications struct {
	bus   *EventBus
	store *notifications.Store

	// mu guards Push during bursts (a crash-looping service fires fast).
	mu sync.Mutex
	// lastKey/lastAt collapse duplicate notices inside a minute.
	lastKey string
	lastAt  time.Time
}

// NewNotifications opens (or creates) the inbox at the standard path.
func NewNotifications(bus *EventBus) (*Notifications, error) {
	store, _, err := notifications.Open(filepath.Join(paths.StateDir(), "notifications.json"))
	if err != nil {
		return nil, err
	}
	return &Notifications{bus: bus, store: store}, nil
}

// Push records one notice (rate-limited per key) and announces it.
func (c *Notifications) Push(kind, title, body string) {
	c.mu.Lock()
	key := kind + "|" + title
	if key == c.lastKey && time.Since(c.lastAt) < time.Minute {
		c.mu.Unlock()
		return
	}
	c.lastKey, c.lastAt = key, time.Now()
	c.mu.Unlock()

	item, _, err := c.store.Append(notifications.Item{
		Kind: notifications.Kind(kind), Title: title, Detail: body,
	})
	if err != nil {
		return
	}
	c.bus.Emit(EvNotificationsNew, item)
}

// PushNotification is the exported bridge for frontend-sourced notices.
func (c *Notifications) PushNotification(kind, title, body string) {
	c.Push(kind, title, body)
}

// ListNotifications returns the inbox, newest first.
func (c *Notifications) ListNotifications() []notifications.Item {
	return c.store.Items()
}

// UnreadNotifications counts unread items for the bell badge.
func (c *Notifications) UnreadNotifications() int {
	return c.store.Unread()
}

// MarkNotificationRead flags one item read.
func (c *Notifications) MarkNotificationRead(id string) error {
	return c.store.MarkRead(id)
}

// MarkAllNotificationsRead clears the badge.
func (c *Notifications) MarkAllNotificationsRead() error {
	return c.store.MarkAllRead()
}

// NotificationDelivery is the preferences card (kept for later phases).
func (c *Notifications) NotificationDelivery() notifications.Delivery {
	return c.store.Delivery()
}
