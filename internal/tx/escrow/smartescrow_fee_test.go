package escrow

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

// A trivial finish function: (module (func (export "finish") (result i32) (i32.const 1))).
const feeTestFinishFn = "0061736d010000000105016000017f03020100070a010666696e69736800000a0601040041010b"

// TestEscrowCreateBaseFee_FinishFunctionSurcharge verifies the SmartEscrow
// FinishFunction surcharge: base*(1+signers) + 9*base + 5*size.
// Reference: rippled EscrowCreate.cpp calculateBaseFee lines 120-131.
func TestEscrowCreateBaseFee_FinishFunctionSurcharge(t *testing.T) {
	const base = uint64(10)
	cfg := tx.EngineConfig{BaseFee: base}

	ffBytes, err := hex.DecodeString(feeTestFinishFn)
	if err != nil {
		t.Fatalf("decode finish fn: %v", err)
	}
	ff := feeTestFinishFn

	withFF := &EscrowCreate{
		BaseTx:         *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
		Amount:         tx.NewXRPAmount(1000000000),
		Destination:    "rBob",
		CancelAfter:    ptrUint32(700000000),
		FinishFunction: &ff,
	}
	want := base + 9*base + 5*uint64(len(ffBytes))
	if got := withFF.CalculateBaseFee(nil, cfg); got != want {
		t.Fatalf("with FinishFunction: got %d, want %d", got, want)
	}

	// Without a FinishFunction the surcharge is absent (plain base fee).
	plain := &EscrowCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
		Amount:      tx.NewXRPAmount(1000000000),
		Destination: "rBob",
		FinishAfter: ptrUint32(700000000),
	}
	if got := plain.CalculateBaseFee(nil, cfg); got != base {
		t.Fatalf("without FinishFunction: got %d, want %d", got, base)
	}
}

// TestEscrowFinishBaseFee_ComputationAllowance verifies the SmartEscrow finish
// fee: base + (allowance*gasPrice/microDropsPerDrop) + 1, using the default gas
// price (1,000,000 micro-drops). Reference: rippled EscrowFinish.cpp lines
// 167-174.
func TestEscrowFinishBaseFee_ComputationAllowance(t *testing.T) {
	const base = uint64(10)
	cfg := tx.EngineConfig{BaseFee: base}

	cases := []struct {
		allowance uint32
		want      uint64
	}{
		{allowance: 1_000_000, want: base + 1_000_001}, // 1e6*1e6/1e6 + 1
		{allowance: 2, want: base + 3},                 // 2*1e6/1e6 + 1
		{allowance: 0, want: base + 1},                 // 0 + 1 (round-up term)
	}
	for _, c := range cases {
		a := c.allowance
		ef := &EscrowFinish{
			BaseTx:               *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
			Owner:                "rAlice",
			OfferSequence:        5,
			ComputationAllowance: &a,
		}
		if got := ef.CalculateBaseFee(nil, cfg); got != c.want {
			t.Fatalf("allowance=%d: got %d, want %d", c.allowance, got, c.want)
		}
	}

	// Without a ComputationAllowance the finish pays only the base fee.
	plain := &EscrowFinish{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
		Owner:         "rAlice",
		OfferSequence: 5,
	}
	if got := plain.CalculateBaseFee(nil, cfg); got != base {
		t.Fatalf("without ComputationAllowance: got %d, want %d", got, base)
	}
}
