package payment

import (
	"encoding/hex"
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestMain(m *testing.M) {
	Register()
	os.Exit(m.Run())
}

func TestPaymentMPTPathBinaryRoundTrip(t *testing.T) {
	p := NewPayment(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH",
		paymentMPTAmount(100, paymentMPTIDA),
	)
	p.Fee = "10"
	p.SetSequence(1)
	p.Paths = [][]PathStep{{{MPTIssuanceID: paymentMPTIDB}}}

	flat, err := p.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	tx.PopulateRequiredWireFields(flat, p.GetCommon())
	encoded, err := binarycodec.Encode(flat)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	blob, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	parsedTx, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	parsed, ok := parsedTx.(*Payment)
	if !ok {
		t.Fatalf("parsed transaction type = %T, want *Payment", parsedTx)
	}
	if !parsed.Amount.IsMPT() || parsed.Amount.MPTIssuanceID() != paymentMPTIDA {
		t.Fatalf("parsed Amount = %#v, want MPT issuance %s", parsed.Amount, paymentMPTIDA)
	}
	if len(parsed.Paths) != 1 || len(parsed.Paths[0]) != 1 ||
		parsed.Paths[0][0].MPTIssuanceID != paymentMPTIDB {
		t.Fatalf("parsed Paths = %#v, want MPT issuance %s", parsed.Paths, paymentMPTIDB)
	}
	rules := paymentMPTV2Rules()
	if mask := parsed.GetFlagsMask(rules); mask != tfPaymentMask {
		t.Fatalf("GetFlagsMask() = 0x%08X, want 0x%08X", mask, tfPaymentMask)
	}
	if err := parsed.PreflightWithRules(rules); err != nil {
		t.Fatalf("PreflightWithRules: %v", err)
	}
}
