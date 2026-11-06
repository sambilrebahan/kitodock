package services

import "sync"

// ring is a bounded, ordered tail of a service's output. Long-running
// services would happily outgrow any honest buffer; the last lines are what
// a person debugging wants.
type ring struct {
	mu    sync.Mutex
	lines []string
}

func (r *ring) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) >= logRingCap {
		copy(r.lines, r.lines[1:])
		r.lines[len(r.lines)-1] = line
		return
	}
	r.lines = append(r.lines, line)
}

func (r *ring) tail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}
