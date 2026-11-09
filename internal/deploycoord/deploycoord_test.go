package deploycoord

import "testing"

func TestCoordinator(t *testing.T) {
	var c Coordinator
	release, err := c.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(); err != ErrBusy {
		t.Fatalf("second acquire = %v, want ErrBusy", err)
	}
	release()
	if _, err := c.Acquire(); err != nil {
		t.Fatalf("after release = %v", err)
	}
}
