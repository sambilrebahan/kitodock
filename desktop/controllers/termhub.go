package controllers

// termHub mirrors every PTY's output into per-session rings with global
// sequence numbers, so a second Kitodock process (a detached terminal
// window) can attach and replay by asking "everything after seq N".
// The main window keeps receiving Wails events as before; the hub is an
// additional sink, not a replacement.

import "sync"

// termChunk is one ordered piece of output.
type termChunk struct {
	Seq  uint64 `json:"seq"`
	Data string `json:"data"`
}

// termRing is one PTY's bounded history. Chunks are dropped from the head
// once the byte budget is exceeded; late attachers simply miss the oldest
// scrollback, like any terminal.
type termRing struct {
	chunks []termChunk
	bytes  int
	exited bool
}

const termRingMaxBytes = 512 * 1024

// termHub is the global mirror.
type termHub struct {
	mu    sync.Mutex
	rings map[string]*termRing
	seq   uint64
}

var hub = &termHub{rings: map[string]*termRing{}}

// termAppend records output for one PTY.
func termAppend(ptyID string, data string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.seq++
	ring := hub.rings[ptyID]
	if ring == nil {
		ring = &termRing{}
		hub.rings[ptyID] = ring
	}
	if ring.exited {
		return
	}
	ring.chunks = append(ring.chunks, termChunk{Seq: hub.seq, Data: data})
	ring.bytes += len(data)
	for ring.bytes > termRingMaxBytes && len(ring.chunks) > 1 {
		ring.bytes -= len(ring.chunks[0].Data)
		ring.chunks = ring.chunks[1:]
	}
}

// termExit marks a PTY finished; pollers learn through the exited flag.
func termExit(ptyID string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if ring := hub.rings[ptyID]; ring != nil {
		ring.exited = true
	}
}

// termSince returns chunks after seq plus the liveness flag.
func termSince(ptyID string, after uint64) (chunks []termChunk, exited bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	ring := hub.rings[ptyID]
	if ring == nil {
		// Unknown id: an exited-and-forgotten session behaves as exited.
		return nil, true
	}
	for _, chunk := range ring.chunks {
		if chunk.Seq > after {
			chunks = append(chunks, chunk)
		}
	}
	return chunks, ring.exited
}
