package tx

import "testing"

func TestCurrentCloseTimePreservesExplicitZero(t *testing.T) {
	cfg := EngineConfig{
		ParentCloseTime:         123,
		ApplicationCloseTime:    0,
		ApplicationCloseTimeSet: true,
	}
	if got := cfg.CurrentCloseTime(); got != 0 {
		t.Fatalf("CurrentCloseTime() = %d, want explicit zero", got)
	}

	cfg.ApplicationCloseTimeSet = false
	if got := cfg.CurrentCloseTime(); got != cfg.ParentCloseTime {
		t.Fatalf("CurrentCloseTime() = %d, want parent %d", got, cfg.ParentCloseTime)
	}
}
