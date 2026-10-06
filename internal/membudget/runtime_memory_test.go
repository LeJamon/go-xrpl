package membudget

import (
	"runtime/debug"
	"testing"
)

func TestRuntimeMemoryLimitZeroIsFinite(t *testing.T) {
	previous := debug.SetMemoryLimit(0)
	limit, finite := runtimeMemoryLimit()
	debug.SetMemoryLimit(previous)
	if limit != 0 || !finite {
		t.Fatalf("zero Go memory limit must be exhausted headroom, got (%d, %t)", limit, finite)
	}
}
