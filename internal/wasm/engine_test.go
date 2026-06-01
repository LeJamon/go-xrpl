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

// stubHost is a minimal HostFunctions returning a fixed ledger sequence.
type stubHost struct{ seq int32 }

func (h stubHost) GetLedgerSqn() (int32, HostFunctionError) { return h.seq, HfSuccess }

// TestEngineFibParity is the core determinism check: a pure-WASM computation
// must consume exactly the fuel rippled's wasmi does. A mismatch here means the
// engine would fork the network.
func TestEngineFibParity(t *testing.T) {
	e := New()
	defer e.Close()

	res, err := e.Run(mustDecode(t, fibWasmHex), "fib", []Param{I32(10)}, nil, nil, GasUnlimited)
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

	_, err := e.Run(mustDecode(t, disabledFloatHex), "finish", nil, nil, nil, GasUnlimited)
	if err == nil {
		t.Fatal("expected float module to be rejected, got success")
	}
}

// TestEngineLedgerSqnHostCall exercises the host-import path and per-call gas
// charging: finish() calls get_ledger_sqn (registered at gas 33). The cost is
// the wasmi 1.0.9 fuel for the finish body (118) plus the 33 host gas charged
// before dispatch, proving the import wiring and checkGas-style accounting.
// (Full host-function gas parity against rippled lands in Stage 2 with the
// smart-escrow all_host_functions fixture.)
func TestEngineLedgerSqnHostCall(t *testing.T) {
	e := New()
	defer e.Close()

	hf := stubHost{seq: 0}
	imports := []Import{ImportGetLedgerSqn(33)}
	res, err := e.Run(mustDecode(t, ledgerSqnWasmHex), "finish", nil, imports, hf, 1_000_000)
	if err != nil {
		t.Fatalf("run ledgerSqn: %v", err)
	}
	if res.Result != 0 {
		t.Errorf("finish result = %d, want 0", res.Result)
	}
	if res.Cost != 151 {
		t.Errorf("finish cost = %d, want 151 (118 wasm fuel + 33 host gas)", res.Cost)
	}
}

// TestEngineConcurrent exercises the claim that one Engine is safe for
// concurrent use: each Run gets a fresh store, so parallel runs (including
// host-call paths through distinct cgo handles) must not interfere. Run under
// -race in CI.
func TestEngineConcurrent(t *testing.T) {
	e := New()
	defer e.Close()

	fib := mustDecode(t, fibWasmHex)
	ledgerSqn := mustDecode(t, ledgerSqnWasmHex)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := e.Run(fib, "fib", []Param{I32(10)}, nil, nil, GasUnlimited); err != nil || r.Result != 55 || r.Cost != 1137 {
				t.Errorf("concurrent fib = %+v err=%v", r, err)
			}
			r, err := e.Run(ledgerSqn, "finish", nil, []Import{ImportGetLedgerSqn(33)}, stubHost{seq: 0}, 1_000_000)
			if err != nil || r.Result != 0 || r.Cost != 151 {
				t.Errorf("concurrent ledgerSqn = %+v err=%v", r, err)
			}
		}()
	}
	wg.Wait()
}
