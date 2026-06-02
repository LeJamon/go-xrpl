//go:build cgo && wasmi

package wasm

import (
	"encoding/hex"
	"sync"
	"testing"
)

func mustDecode(t *testing.T, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return b
}

// TestEngineFibParity is the core determinism check: a pure-WASM computation
// must consume exactly the fuel rippled's wasmi does. A mismatch here means the
// engine would fork the network.
func TestEngineFibParity(t *testing.T) {
	e := New()
	defer e.Close()

	res, err := e.Run(mustDecode(t, fibWasmHex), "fib", []Param{I32(10)}, nil, GasUnlimited)
	if err != nil {
		t.Fatalf("run fib: %v", err)
	}
	if res.Result != 55 {
		t.Errorf("fib(10) result = %d, want 55", res.Result)
	}
	if res.Cost != 1137 {
		t.Errorf("fib(10) cost = %d, want 1137 (fuel model mismatch vs rippled wasmi 1.0.9)", res.Cost)
	}
}

// TestEngineDisabledFloatRejected proves the float-disabling config flag is
// wired: a module using f32 ops must fail to load, matching rippled's
// tecFAILED_PROCESSING.
func TestEngineDisabledFloatRejected(t *testing.T) {
	e := New()
	defer e.Close()

	_, err := e.Run(mustDecode(t, disabledFloatHex), "finish", nil, nil, GasUnlimited)
	if err == nil {
		t.Fatal("expected float module to be rejected, got success")
	}
}

// TestEngineConcurrent exercises the claim that one Engine is safe for
// concurrent use: each Run gets a fresh store, so parallel runs must not
// interfere. Run under -race in CI.
func TestEngineConcurrent(t *testing.T) {
	e := New()
	defer e.Close()

	fib := mustDecode(t, fibWasmHex)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := e.Run(fib, "fib", []Param{I32(10)}, nil, GasUnlimited); err != nil || r.Result != 55 || r.Cost != 1137 {
				t.Errorf("concurrent fib = %+v err=%v", r, err)
			}
		}()
	}
	wg.Wait()
}
