// Package deploycoord provides a single-flight mutex for remote deployment
// mutations: one deployment operation at a time per desktop process.
package deploycoord

import "errors"

// ErrBusy is returned when a second mutation is attempted while one runs.
var ErrBusy = errors.New("a deployment run is already in progress")

// Coordinator serializes deployment mutations.
type Coordinator struct {
	busy bool
}

// Acquire claims the mutation lock. The returned release function must be
// called when the operation finishes.
func (c *Coordinator) Acquire() (func(), error) {
	if c.busy {
		return nil, ErrBusy
	}
	c.busy = true
	return func() { c.busy = false }, nil
}
