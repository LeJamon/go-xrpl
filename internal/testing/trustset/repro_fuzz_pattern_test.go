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

// TestRepro_ManySameAccountTrustSets reproduces the soak-network fuzz pattern
// that caused a consensus fork at seq 8 on 2026-05-19: one account creates 9
// trust lines to 9 different issuers, all committed in the same ledger.
//
// The agreed tx_set hash on both impls was b942ae144047df82, but goxrpl built
// seq 8 hash ED970256FFA1A501... while rippled built 22FAB89DCA2ABCD4... —
// same parent, same tx_set, different state root → apply divergence.
//
// This test:
//   1. Funds Alice + 9 issuers without DefaultRipple
//   2. Submits 9 sequential TrustSets from Alice (one per issuer) in one ledger
//   3. Dumps Alice's AccountRoot + every created RippleState SLE in binary form
//
// Run against goxrpl with `just test-pkg ./internal/testing/trustset/`. Compare
// dumped bytes with the output of replaying the same sequence into standalone
// rippled (docs/issue-470-standalone-replay.py) to localize the divergent SLE.
func TestRepro_ManySameAccountTrustSets(t *testing.T) {
	env := jtx.NewTestEnv(t)

	const N = 9
	alice := jtx.NewAccount("alice")
	issuers := make([]*jtx.Account, N)
	all := []*jtx.Account{alice}
	for i := 0; i < N; i++ {
		issuers[i] = jtx.NewAccount(fmt.Sprintf("issuer%d", i))
		all = append(all, issuers[i])
	}

	env.FundNoRipple(all...)
	env.Close()

	preCloseLedger := env.LastClosedLedger()
	if preCloseLedger == nil {
		t.Fatal("pre-trustset LastClosedLedger nil")
	}
	t.Logf("Pre-trustset ledger seq=%d hash=%x", preCloseLedger.Sequence(), preCloseLedger.Hash())

	// Submit N TrustSets from Alice to each issuer — all in the SAME ledger.
	// The TestEnv auto-assigns sequence numbers; submissions remain open until
	// env.Close() seals them into one ledger.
	for i, issuer := range issuers {
		limit := tx.NewIssuedAmountFromFloat64(1000, "USD", issuer.Address)
		result := env.Submit(TrustSet(alice, limit).Build())
		jtx.RequireTxSuccess(t, result)
		t.Logf("  TrustSet[%d] alice→issuer%d code=%s", i, i, result.Code)
	}
	env.Close()

	closed := env.LastClosedLedger()
	if closed == nil {
		t.Fatal("post-trustset LastClosedLedger nil")
	}
	stateHash, _ := closed.StateMapHash()
	txHash, _ := closed.TxMapHash()
	t.Logf("Closed ledger seq=%d hash=%x", closed.Sequence(), closed.Hash())
	t.Logf("  account_hash=%x", stateHash)
	t.Logf("  tx_hash=%x", txHash)

	fmt.Println("=== REPRO_MULTI_TRUSTSET ALICE ACCOUNT_ROOT ===")
	aliceKL := keylet.Account(alice.ID)
	aliceData, err := closed.Read(aliceKL)
	if err != nil {
		t.Fatalf("Read alice AccountRoot: %v", err)
	}
	fmt.Println("alice address:", alice.Address)
	fmt.Println("alice account_root hex:", strings.ToUpper(hex.EncodeToString(aliceData)))

	fmt.Println("=== REPRO_MULTI_TRUSTSET RIPPLE_STATE PER ISSUER ===")
	for i, issuer := range issuers {
		rsKL := keylet.Line(alice.ID, issuer.ID, "USD")
		data, err := closed.Read(rsKL)
		if err != nil {
			t.Fatalf("Read RippleState[%d]: %v", i, err)
		}
		if data == nil {
			t.Fatalf("RippleState[%d] missing", i)
			continue
		}
		fmt.Printf("[%d] issuer=%s index=%s\n", i, issuer.Address,
			strings.ToUpper(hex.EncodeToString(rsKL.Key[:])))
		fmt.Printf("[%d] ripple_state hex (len=%d): %s\n", i, len(data),
			strings.ToUpper(hex.EncodeToString(data)))
	}

	t.Logf("✓ all %d trust lines persisted; if this ledger's hash %x != rippled standalone's hash for the same operations, the divergent SLE is in the dumps above", N, closed.Hash())
}
