package cli

import (
	"strings"
	"testing"
)

func TestInitMethodRegistry_Idempotent(t *testing.T) {
	r1 := initMethodRegistry()
	if r1 == nil {
		t.Fatal("registry is nil")
	}
	// The CLI shares handlers.RegisterAll with the servers, so a core method
	// must be present.
	if _, ok := r1.Get("ping"); !ok {
		t.Error("ping not registered")
	}
	// Memoized: a second call returns the same instance.
	if r2 := initMethodRegistry(); r2 != r1 {
		t.Error("initMethodRegistry should memoize the registry")
	}
}

func TestExecuteMethod_UnknownMethod(t *testing.T) {
	err := executeMethod("definitely_not_a_method", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("expected unknown-method error, got %v", err)
	}
}

func TestExecuteMethod_Ping(t *testing.T) {
	restore := silenceStdout(t)
	defer restore()

	// ping is fully self-contained (no ledger/services), so it exercises the
	// success path of executeMethod end to end.
	if err := executeMethod("ping", nil); err != nil {
		t.Fatalf("ping without params: %v", err)
	}
	if err := executeMethod("ping", map[string]interface{}{"unused": true}); err != nil {
		t.Fatalf("ping with params: %v", err)
	}
}
