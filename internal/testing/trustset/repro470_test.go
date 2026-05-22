package trustset

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// TestRepro470_TrustSetSLE writes a minimal Alice->Bob USD TrustSet and dumps
// the resulting RippleState SLE in binary form so we can byte-compare with
// rippled's standalone output for the same scenario.
//
// The rippled-standalone reference run produced (with master funding Alice
// and Bob with 100 XRP each, then Alice TrustSet for 1000 USD with Bob as
// issuer):
//
//	Flags:             2162688 (0x00210000)
//	LowNode:           0
//	HighNode:          0
//	LowLimit issuer:   Alice    value 1000  currency USD
//	HighLimit issuer:  Bob      value 0     currency USD
//	Balance:           0 USD    issuer rrrrrrrrrrrrrrrrrrrrBZbvji
//	PreviousTxnLgrSeq: 4
//	index:             F13264AADB615622C37E42C0195237245233E8DC6D15AE898E2D2E3A63D42F73
func TestRepro470_TrustSetSLE(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	// Mirror the fuzz harness: fund WITHOUT enabling DefaultRipple
	// (so the peer's NoRipple flag should be set on the new trust line).
	env.FundNoRipple(alice, bob)
	env.Close()

	// Alice trusts Bob for 1000 USD
	limit := tx.NewIssuedAmountFromFloat64(1000, "USD", bob.Address)
	result := env.Submit(TrustSet(alice, limit).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	closed := env.LastClosedLedger()
	if closed == nil {
		t.Fatal("LastClosedLedger nil")
	}
	stateHash, _ := closed.StateMapHash()
	t.Logf("Closed ledger seq=%d hash=%x account_hash=%x", closed.Sequence(), closed.Hash(), stateHash)

	rsKL := keylet.Line(alice.ID, bob.ID, "USD")
	t.Logf("RippleState index: %s", strings.ToUpper(hex.EncodeToString(rsKL.Key[:])))

	data, err := closed.Read(rsKL)
	if err != nil {
		t.Fatalf("Read RippleState: %v", err)
	}
	if data == nil {
		t.Fatal("RippleState SLE not found")
	}
	t.Logf("RippleState SLE binary (len=%d):", len(data))
	t.Logf("  %s", strings.ToUpper(hex.EncodeToString(data)))

	fmt.Println("=== REPRO470 SLE ===")
	fmt.Println(strings.ToUpper(hex.EncodeToString(data)))
	fmt.Println("alice:", alice.Address)
	fmt.Println("bob:", bob.Address)
}
