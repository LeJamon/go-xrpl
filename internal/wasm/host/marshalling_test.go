//go:build cgo && wasmi

package host

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/wasm"
)

// account_keylet.wat (compiled with wat2wasm): a finish() that calls
// account_keylet on a 20-byte account at memory offset 0, writing the 32-byte
// keylet to offset 32, and returns the byte count.
const accountKeyletWasmHex = "0061736d01000000010d0260047f7f7f7f017f6000017f02160103656e760e6163636f756e745f6b65796c65740000030201010503010001071302066d656d6f727902000666696e69736800010a0e010c00410041144120412010000b0b1a010041000b140102030405060708090a0b0c0d0e0f1011121314"

// spyEnv records the account bytes the engine decodes from linear memory before
// delegating to the real keylet derivation.
type spyEnv struct {
	*Env
	gotAccount []byte
}

func (s *spyEnv) AccountKeylet(a []byte) ([]byte, wasm.HostFunctionError) {
	s.gotAccount = append([]byte(nil), a...)
	return s.Env.AccountKeylet(a)
}

// TestEngineHostMarshalling exercises the engine's generic host-call marshalling
// end to end: the input slice is read out of contract linear memory, the host
// function runs, and its 32-byte result is written back to the caller's output
// buffer (the import returns the number of bytes written).
func TestEngineHostMarshalling(t *testing.T) {
	code, err := hex.DecodeString(accountKeyletWasmHex)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	eng := wasm.New()
	defer eng.Close()

	s := &spyEnv{Env: New(nil)}
	res, err := eng.Run(code, "finish", nil, s, wasm.GasUnlimited)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Result != 32 {
		t.Errorf("account_keylet returned %d bytes written, want 32", res.Result)
	}

	want := make([]byte, 20)
	for i := range want {
		want[i] = byte(i + 1)
	}
	if !bytes.Equal(s.gotAccount, want) {
		t.Errorf("host decoded account %x, want %x", s.gotAccount, want)
	}
}

// ledger.wat: ledgerseq() writes get_ledger_sqn's u32 to memory and returns it;
// checkamd() returns amendment_enabled for the 32-byte id at offset 0 (0x01..0x20).
const ledgerWasmHex = "0061736d01000000010b0260027f7f017f6000017f022e0203656e760e6765745f6c65646765725f73716e000003656e7611616d656e646d656e745f656e61626c6564000003030201010503010001072103066d656d6f72790200096c6564676572736571000208636865636b616d6400030a1b02100041c000412010001a41c0002802000b08004100412010010b0b26010041000b200102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// TestEngineLedgerHostCalls exercises the bufferOut-only u32 path
// (get_ledger_sqn, read back as the actual sequence) and the direct-i32 return
// path (amendment_enabled) end to end through the engine.
func TestEngineLedgerHostCalls(t *testing.T) {
	code, err := hex.DecodeString(ledgerWasmHex)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	eng := wasm.New()
	defer eng.Close()

	var amdID [32]byte
	for i := range amdID {
		amdID[i] = byte(i + 1) // 0x01..0x20, matching the fixture's data section
	}
	e := New(&mockView{seq: 12345, enabled: map[[32]byte]bool{amdID: true}})

	if res, err := eng.Run(code, "ledgerseq", nil, e, wasm.GasUnlimited); err != nil || res.Result != 12345 {
		t.Errorf("ledgerseq = %+v err=%v, want 12345", res, err)
	}
	if res, err := eng.Run(code, "checkamd", nil, e, wasm.GasUnlimited); err != nil || res.Result != 1 {
		t.Errorf("checkamd = %+v err=%v, want 1", res, err)
	}
}
