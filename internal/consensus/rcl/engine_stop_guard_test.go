package rcl

import "testing"

// TestEngine_StopBeforeStart verifies Stop does not nil-panic when Start never
// ran (e.cancel is nil until Start). A defensive doShutdown / error-path stop
// must tolerate this, same class as the fuzz-found doShutdown nil-panic.
func TestEngine_StopBeforeStart(t *testing.T) {
	e := NewEngine(newMockAdaptor(), DefaultConfig())
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop before Start returned error: %v", err)
	}
	// A second Stop must also be safe (eventBus.Stop is idempotent).
	if err := e.Stop(); err != nil {
		t.Fatalf("second Stop returned error: %v", err)
	}
}
