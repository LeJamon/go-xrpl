package host

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

type mockView struct {
	seq        uint32
	parentTime uint32
	baseFee    uint32
	parentHash [32]byte
	enabled    map[[32]byte]bool
	tx         []byte
	obj        []byte
	sles       map[[32]byte][]byte
	nftURIs    map[[20]byte][]byte
}

func (m *mockView) LedgerSeq() uint32                 { return m.seq }
func (m *mockView) ParentCloseTime() uint32           { return m.parentTime }
func (m *mockView) ParentHash() [32]byte              { return m.parentHash }
func (m *mockView) BaseFee() uint32                   { return m.baseFee }
func (m *mockView) AmendmentEnabled(id [32]byte) bool { return m.enabled[id] }
func (m *mockView) TxBytes() []byte                   { return m.tx }
func (m *mockView) CurrentObjBytes() ([]byte, bool)   { return m.obj, m.obj != nil }

func (m *mockView) ReadSLE(index [32]byte) ([]byte, bool) {
	b, ok := m.sles[index]
	return b, ok
}

func (m *mockView) FindNFTURI(account [20]byte, _ [32]byte) (uri []byte, found, hasURI bool) {
	b, ok := m.nftURIs[account]
	return b, ok, ok && len(b) > 0
}

func TestLedgerHeaderQueries(t *testing.T) {
	var ph [32]byte
	for i := range ph {
		ph[i] = byte(0xA0 + i)
	}
	v := &mockView{seq: 42, parentTime: 800000000, baseFee: 10, parentHash: ph}
	e := New(v)

	if got, herr := e.GetLedgerSqn(); herr != wasm.HfSuccess || got != 42 {
		t.Errorf("GetLedgerSqn = %d, %d", got, herr)
	}
	if got, herr := e.GetParentLedgerTime(); herr != wasm.HfSuccess || got != 800000000 {
		t.Errorf("GetParentLedgerTime = %d, %d", got, herr)
	}
	if got, herr := e.GetBaseFee(); herr != wasm.HfSuccess || got != 10 {
		t.Errorf("GetBaseFee = %d, %d", got, herr)
	}
	if got, herr := e.GetParentLedgerHash(); herr != wasm.HfSuccess || !bytes.Equal(got, ph[:]) {
		t.Errorf("GetParentLedgerHash = %x, %d", got, herr)
	}
}

func TestLedgerHeaderNoView(t *testing.T) {
	e := New(nil)
	if _, herr := e.GetLedgerSqn(); herr != wasm.HfNoRuntime {
		t.Errorf("nil view GetLedgerSqn herr = %d, want HfNoRuntime", herr)
	}
}

func TestIsAmendmentEnabled(t *testing.T) {
	id := amendment.FeatureID("SmartEscrow")
	v := &mockView{enabled: map[[32]byte]bool{id: true}}
	e := New(v)

	// by id (32 bytes)
	if got, herr := e.IsAmendmentEnabled(id[:]); herr != wasm.HfSuccess || got != 1 {
		t.Errorf("by id = %d, %d, want 1", got, herr)
	}
	// by name
	if got, herr := e.IsAmendmentEnabled([]byte("SmartEscrow")); herr != wasm.HfSuccess || got != 1 {
		t.Errorf("by name = %d, %d, want 1", got, herr)
	}
	// unknown name -> 0
	if got, _ := e.IsAmendmentEnabled([]byte("NoSuchAmendment")); got != 0 {
		t.Errorf("unknown = %d, want 0", got)
	}
	// too large
	if _, herr := e.IsAmendmentEnabled(make([]byte, 65)); herr != wasm.HfDataFieldTooLarge {
		t.Errorf("oversized herr = %d, want HfDataFieldTooLarge", herr)
	}
}

func TestComputeSha512Half(t *testing.T) {
	e := New(nil)
	data := []byte("hello smart escrow")
	got, herr := e.ComputeSha512Half(data)
	if herr != wasm.HfSuccess {
		t.Fatalf("herr %d", herr)
	}
	want := common.Sha512Half(data)
	if !bytes.Equal(got, want[:]) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestUpdateData(t *testing.T) {
	e := New(nil)
	data := []byte("some state")
	n, herr := e.UpdateData(data)
	if herr != wasm.HfSuccess || int(n) != len(data) {
		t.Errorf("UpdateData = %d, %d", n, herr)
	}
	if !bytes.Equal(e.Data(), data) {
		t.Errorf("Data() = %q, want %q", e.Data(), data)
	}
	if _, herr := e.UpdateData(make([]byte, maxWasmDataLength+1)); herr != wasm.HfDataFieldTooLarge {
		t.Errorf("oversized herr = %d, want HfDataFieldTooLarge", herr)
	}
}

func TestCheckSignatureRejectsGarbage(t *testing.T) {
	e := New(nil)
	if got, _ := e.CheckSignature([]byte("msg"), []byte("sig"), []byte{0x00}); got != 0 {
		t.Errorf("garbage signature = %d, want 0", got)
	}
}

func TestTraceReturnsZero(t *testing.T) {
	e := New(nil)
	checks := []struct {
		name string
		got  int32
	}{
		{"trace", first(e.Trace([]byte("m"), []byte("d"), false))},
		{"trace_num", first(e.TraceNum([]byte("m"), 7))},
		{"trace_account", first(e.TraceAccount([]byte("m"), make([]byte, 20)))},
		{"trace_float", first(e.TraceFloat([]byte("m"), make([]byte, 8)))},
		{"trace_amount", first(e.TraceAmount([]byte("m"), make([]byte, 8)))},
	}
	for _, c := range checks {
		if c.got != 0 {
			t.Errorf("%s returned %d, want 0", c.name, c.got)
		}
	}
}

func first(v int32, _ wasm.HostFunctionError) int32 { return v }
