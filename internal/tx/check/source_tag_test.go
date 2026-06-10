package check

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestSerializeCheck_SourceTag pins that a SourceTag-bearing CheckCreate copies
// the tag onto the Check SLE, and that it survives the ParseCheck →
// SerializeCheckFromData round-trip used by the create flow.
// Reference: rippled CreateCheck.cpp:199-200.
func TestSerializeCheck_SourceTag(t *testing.T) {
	var owner, dest [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
	}
	for i := range dest {
		dest[i] = byte(0x40 + i)
	}

	srcTag := uint32(0xDEADBEEF)
	checkTx := &CheckCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
		Destination: "rBob",
		SendMax:     tx.NewXRPAmount(10_000_000),
	}
	checkTx.SourceTag = &srcTag

	data, err := serializeCheck(checkTx, owner, dest, 5, checkTx.SendMax)
	if err != nil {
		t.Fatalf("serializeCheck: %v", err)
	}

	// sfSourceTag is UInt32 nth=3 → field header 0x23, then the big-endian value.
	hexUpper := strings.ToUpper(toHexCheck(data))
	if !strings.Contains(hexUpper, "23DEADBEEF") {
		t.Fatalf("Check SLE blob missing sfSourceTag (23DEADBEEF): %s", hexUpper)
	}

	// The create flow re-parses then re-serializes to thread directory pages;
	// the tag must survive that round-trip (HasSourceTag must be set).
	parsed, err := state.ParseCheck(data)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if !parsed.HasSourceTag || parsed.SourceTag != srcTag {
		t.Fatalf("round-trip lost SourceTag: has=%v val=%#x", parsed.HasSourceTag, parsed.SourceTag)
	}

	reser, err := state.SerializeCheckFromData(parsed)
	if err != nil {
		t.Fatalf("SerializeCheckFromData: %v", err)
	}
	if !strings.Contains(strings.ToUpper(toHexCheck(reser)), "23DEADBEEF") {
		t.Fatalf("re-serialized Check SLE dropped sfSourceTag: %s", strings.ToUpper(toHexCheck(reser)))
	}
}

// TestSerializeCheck_NoSourceTag pins that absent SourceTag stays absent.
func TestSerializeCheck_NoSourceTag(t *testing.T) {
	var owner, dest [20]byte
	checkTx := &CheckCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
		Destination: "rBob",
		SendMax:     tx.NewXRPAmount(10_000_000),
	}

	data, err := serializeCheck(checkTx, owner, dest, 5, checkTx.SendMax)
	if err != nil {
		t.Fatalf("serializeCheck: %v", err)
	}
	parsed, err := state.ParseCheck(data)
	if err != nil {
		t.Fatalf("ParseCheck: %v", err)
	}
	if parsed.HasSourceTag {
		t.Fatalf("SourceTag should be absent, got %#x", parsed.SourceTag)
	}
}

func toHexCheck(b []byte) string {
	const hexits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexits[v>>4]
		out[i*2+1] = hexits[v&0x0f]
	}
	return string(out)
}
