package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestValidVaultZeroWithdrawalScope(t *testing.T) {
	owner, pseudo, ownerAddr, pseudoAddr, shares := vvTestIDs()
	issuer, issuerAddr := vvTestAccount(t, 0x33)
	_, destination := vvTestAccount(t, 0x44)
	vault := func(total string) []byte {
		return vvCraftVaultAsset(t, ownerAddr, pseudoAddr, map[string]any{"currency": "USD", "issuer": issuerAddr}, shares, total, "0", "", "10")
	}
	for _, tc := range []struct {
		name        string
		cleanup     bool
		beforeTotal string
		afterTotal  string
		ownerAfter  uint64
		extra       string
		destination string
		want        string
	}{
		{name: "fully impaired", cleanup: true},
		{name: "legacy", want: "withdrawal must change vault balance"},
		{name: "positive effective assets", cleanup: true, beforeTotal: "11", afterTotal: "11", want: "withdrawal must change vault balance"},
		{name: "unfunded recipient credit", cleanup: true, extra: "recipient", want: "withdrawal must change vault and destination balance by equal amount"},
		{name: "sender credit with distinct destination", cleanup: true, extra: "recipient", destination: destination, want: "withdrawal must change one destination balance"},
		{name: "unchanged holder shares", cleanup: true, ownerAfter: 2, want: "withdrawal must decrease depositor shares"},
		{name: "changed vault totals", cleanup: true, afterTotal: "11", want: "withdrawal and assets outstanding must add up"},
		{name: "vault credited", cleanup: true, extra: "vault", want: "withdrawal must decrease vault balance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after, ownerAfter := tc.beforeTotal, tc.afterTotal, tc.ownerAfter
			if before == "" {
				before = "10"
			}
			if after == "" {
				after = "10"
			}
			if ownerAfter == 0 {
				ownerAfter = 1
			}
			entries := make([]InvariantEntry, 0, 4)
			entries = append(entries,
				InvariantEntry{EntryType: entry.TypeVault, Before: vault(before), After: vault(after)},
				vvIssuanceEntry(t, pseudo, shares, 10, 9),
				vvMPTokenEntry(t, owner, shares, 2, ownerAfter),
			)
			if tc.extra != "" {
				account := owner
				if tc.extra == "vault" {
					account = pseudo
				}
				entries = append(entries, vvIOULineEntry(t, account, issuer, "0", "1"))
			}
			rules := savFixedRules()
			if tc.cleanup {
				rules = amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault, amendment.FeatureFixCleanup3_1_3, amendment.FeatureFixCleanup3_2_0, amendment.FeatureFixCleanup3_4_0})
			}
			transaction := vvTx{txType: protocol.TxTypeVaultWithdraw, acct: ownerAddr, flat: map[string]any{}}
			if tc.destination != "" {
				transaction.flat["Destination"] = tc.destination
			}
			violation := checkValidVault(transaction, TesSUCCESS, 0, entries, stubView{}, rules)
			if tc.want == "" {
				require.Nil(t, violation)
			} else {
				require.NotNil(t, violation)
				require.Equal(t, tc.want, violation.Message)
			}
		})
	}
}
