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
