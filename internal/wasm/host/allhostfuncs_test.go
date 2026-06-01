//go:build cgo && wasmi

package host

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

// cannedHost reproduces rippled's TestHostFunctions: fixed, ledger-independent
// return values the all_host_functions contract checks against. Keylets are the
// real derivations (promoted from the embedded *Env). The contract calls a
// fixed sequence regardless of returns, so matching these values reproduces its
// exact cost.
type cannedHost struct {
	*Env
	acct [20]byte
}

func fname(code int32) string {
	n, err := definitions.Get().GetFieldNameByFieldHeader(
		definitions.FieldHeader{TypeCode: code >> 16, FieldCode: code & 0xFFFF})
	if err != nil {
		return ""
	}
	return n
}

func leI64(v int64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(v))
	return b
}

var cannedHash = func() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(0xA0 + i)
	}
	return b
}()

func (c *cannedHost) GetLedgerSqn() (uint32, wasm.HostFunctionError) { return 12345, wasm.HfSuccess }
func (c *cannedHost) GetParentLedgerTime() (uint32, wasm.HostFunctionError) {
	return 67890, wasm.HfSuccess
}
func (c *cannedHost) GetParentLedgerHash() ([]byte, wasm.HostFunctionError) {
	return cannedHash, wasm.HfSuccess
}
func (c *cannedHost) GetBaseFee() (uint32, wasm.HostFunctionError) { return 10, wasm.HfSuccess }
func (c *cannedHost) IsAmendmentEnabled([]byte) (int32, wasm.HostFunctionError) {
	return 1, wasm.HfSuccess
}
func (c *cannedHost) CacheLedgerObj([]byte, int32) (int32, wasm.HostFunctionError) {
	return 1, wasm.HfSuccess
}

func (c *cannedHost) GetTxField(code int32) ([]byte, wasm.HostFunctionError) {
	switch fname(code) {
	case "Account":
		return c.acct[:], wasm.HfSuccess
	case "Fee":
		return leI64(235), wasm.HfSuccess
	case "Sequence":
		v, _ := c.GetLedgerSqn()
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, v)
		return b, wasm.HfSuccess
	}
	return []byte{}, wasm.HfSuccess
}

func (c *cannedHost) GetCurrentLedgerObjField(code int32) ([]byte, wasm.HostFunctionError) {
	switch fname(code) {
	case "Account", "Destination":
		return c.acct[:], wasm.HfSuccess
	case "Data":
		return []byte("10000"), wasm.HfSuccess
	case "FinishAfter":
		return []byte("67890"), wasm.HfSuccess
	}
	return nil, wasm.HfInternal
}

func (c *cannedHost) GetLedgerObjField(_ int32, code int32) ([]byte, wasm.HostFunctionError) {
	switch fname(code) {
	case "Balance":
		return leI64(10000), wasm.HfSuccess
	case "Account":
		return c.acct[:], wasm.HfSuccess
	}
	return []byte("10000"), wasm.HfSuccess
}

func (c *cannedHost) nested(locator []byte) ([]byte, wasm.HostFunctionError) {
	if len(locator) == 4 && int32(binary.LittleEndian.Uint32(locator)) == fieldOrdinal("Account") {
		return c.acct[:], wasm.HfSuccess
	}
	return cannedHash, wasm.HfSuccess
}
func (c *cannedHost) GetTxNestedField(l []byte) ([]byte, wasm.HostFunctionError) { return c.nested(l) }
func (c *cannedHost) GetCurrentLedgerObjNestedField(l []byte) ([]byte, wasm.HostFunctionError) {
	return c.nested(l)
}
func (c *cannedHost) GetLedgerObjNestedField(_ int32, l []byte) ([]byte, wasm.HostFunctionError) {
	return c.nested(l)
}

func (c *cannedHost) GetTxArrayLen(int32) (int32, wasm.HostFunctionError) { return 32, wasm.HfSuccess }
func (c *cannedHost) GetCurrentLedgerObjArrayLen(int32) (int32, wasm.HostFunctionError) {
	return 32, wasm.HfSuccess
}
func (c *cannedHost) GetLedgerObjArrayLen(int32, int32) (int32, wasm.HostFunctionError) {
	return 32, wasm.HfSuccess
}
func (c *cannedHost) GetTxNestedArrayLen([]byte) (int32, wasm.HostFunctionError) {
	return 32, wasm.HfSuccess
}
func (c *cannedHost) GetCurrentLedgerObjNestedArrayLen([]byte) (int32, wasm.HostFunctionError) {
	return 32, wasm.HfSuccess
}
func (c *cannedHost) GetLedgerObjNestedArrayLen(int32, []byte) (int32, wasm.HostFunctionError) {
	return 32, wasm.HfSuccess
}

func (c *cannedHost) UpdateData(data []byte) (int32, wasm.HostFunctionError) {
	return int32(len(data)), wasm.HfSuccess
}
func (c *cannedHost) ComputeSha512Half([]byte) ([]byte, wasm.HostFunctionError) {
	return cannedHash, wasm.HfSuccess
}
func (c *cannedHost) GetNFT(account, nftID []byte) ([]byte, wasm.HostFunctionError) {
	// TestHostFunctions returns an error for a zero account or nft id; the
	// contract passes a zero nft id, so this must fail (matching its branch).
	if len(account) != 20 || len(nftID) != 32 || allZero(account) || allZero(nftID) {
		return nil, wasm.HfInvalidParams
	}
	return []byte("https://ripple.com"), wasm.HfSuccess
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func fieldOrdinal(name string) int32 {
	fi, _ := definitions.Get().GetFieldInstanceByFieldName(name)
	return fi.Ordinal
}

// TestAllHostFunctionsCostParity runs rippled's all_host_functions contract and
// asserts the exact cost (69840), cross-checking the engine, every import
// signature, and the full gas table against rippled.
func TestAllHostFunctionsCostParity(t *testing.T) {
	code, err := hex.DecodeString(kAllHostFunctionsWasmHex)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	_, acctBytes, err := addresscodec.DecodeClassicAddressToAccountID("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	if err != nil {
		t.Fatal(err)
	}
	c := &cannedHost{Env: New(nil)}
	copy(c.acct[:], acctBytes)

	eng := wasm.New()
	defer eng.Close()

	res, err := eng.Run(code, "finish", nil, c, 1_000_000)
	if err != nil {
		t.Fatalf("run all_host_functions: %v", err)
	}
	if res.Result != 1 {
		t.Fatalf("result = %d, want 1 (contract failed a host-function check)", res.Result)
	}
	if res.Cost != 69840 {
		t.Errorf("cost = %d, want 69840 (wasm fuel + host gas must match rippled)", res.Cost)
	}
}
