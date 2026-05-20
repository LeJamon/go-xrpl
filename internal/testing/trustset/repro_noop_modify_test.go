package trustset

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// TestReproNoOpModify_NoGhostModifiedNode verifies that a TrustSet which
// re-asserts the existing trust line with the SAME limit (a "no-op modify"
// of the RippleState) emits exactly the same AffectedNodes as rippled v2.6.2:
// only the sender's AccountRoot, NOT the unchanged RippleState.
//
// Iter14 (soak-mixed-3x2) forked at L10 on tx EF7A1C7D...: the TrustSet's
// apply path called View.Update on the RippleState with bytes identical to
// the existing SLE. The Action was promoted Cache→Modify, applyThreading
// then mutated entry.Current with PreviousTxnID/PreviousTxnLgrSeq, and the
// bytes.Equal(Original, Current) skip in the meta loop no longer fired —
// producing a ghost ModifiedNode (151B vs rippled's no-emission).
//
// Reference for the rippled behavior:
//   rippled/src/xrpld/ledger/detail/ApplyStateTable.cpp:156-157
//     if ((type == &sfModifiedNode) && (*curNode == *origNode))
//       continue;
//
// The goxrpl fix (apply_state_table.go applyThreading ActionModify branch)
// mirrors this by skipping threading when entry.Original == entry.Current.
func TestReproNoOpModify_NoGhostModifiedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	disableAllAmendmentsBD(env)

	const senderSeed = "shutW9X6jm9Uo3eTPkhweAcv8cYeP"
	const issuerSeed = "sa38ZRR4x9dX64iQQh7mcfVn66Ba5"

	sender := jtx.NewAccountFromSeed("sender", senderSeed)
	issuer := jtx.NewAccountFromSeed("issuer", issuerSeed)
	env.FundAmountNoRipple(sender, 10_000_000_000)
	env.FundAmountNoRipple(issuer, 10_000_000_000)
	env.Close()

	limit := tx.NewIssuedAmountFromFloat64(1_000_000, "USD", issuer.Address)

	// 1st TrustSet — creates trust line.
	first := env.Submit(TrustSet(sender, limit).Build())
	jtx.RequireTxSuccess(t, first)
	env.Close()

	// 2nd TrustSet — same limit, same issuer. This is the no-op modify.
	// The RippleState's binary state is unchanged; only the sender's
	// AccountRoot (Sequence/Balance) should appear in meta.
	second := env.Submit(TrustSet(sender, limit).Build())
	jtx.RequireTxSuccess(t, second)

	if second.Metadata == nil {
		t.Fatal("2nd TrustSet has nil Metadata")
	}

	// Inspect AffectedNodes: should be exactly 1 entry (sender AccountRoot),
	// no RippleState ModifiedNode.
	rippleStateMods := 0
	accountRootMods := 0
	for _, n := range second.Metadata.AffectedNodes {
		switch n.LedgerEntryType {
		case "RippleState":
			if n.NodeType == "ModifiedNode" {
				rippleStateMods++
				mb, _ := tx.SerializeMetadata(second.Metadata)
				t.Logf("Ghost RippleState ModifiedNode found. Full meta (%dB): %s",
					len(mb), strings.ToUpper(hex.EncodeToString(mb)))
			}
		case "AccountRoot":
			if n.NodeType == "ModifiedNode" {
				accountRootMods++
			}
		}
	}

	if rippleStateMods != 0 {
		t.Errorf("no-op TrustSet modify emitted %d RippleState ModifiedNode(s); "+
			"expected 0 (matches rippled v2.6.2 ApplyStateTable.cpp:156-157)",
			rippleStateMods)
	}
	if accountRootMods != 1 {
		t.Errorf("expected exactly 1 AccountRoot ModifiedNode (sender, fee/Sequence), got %d",
			accountRootMods)
	}

	t.Logf("2nd TrustSet (no-op) meta has %d AffectedNodes (RippleState mods=%d, AccountRoot mods=%d)",
		len(second.Metadata.AffectedNodes), rippleStateMods, accountRootMods)

	mb, err := tx.SerializeMetadata(second.Metadata)
	if err != nil {
		t.Fatalf("SerializeMetadata: %v", err)
	}
	t.Logf("META_BLOB (%dB): %s", len(mb), strings.ToUpper(hex.EncodeToString(mb)))
}
