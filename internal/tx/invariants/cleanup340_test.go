package invariants

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestPermissionedDEXCleanup340ConsumedOffer(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, deleted := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%t/deleted=%t", enabled, deleted), func(t *testing.T) {
				blob := makeHybridOfferBlob(t, true, true)
				offer, err := state.ParseLedgerOffer(blob)
				require.NoError(t, err)
				offer.TakerPays = state.NewXRPAmountFromInt(0)
				offer.TakerGets = state.NewIssuedAmountFromFloat64(0, "USD", offer.Account)
				final, err := state.SerializeLedgerOffer(offer)
				require.NoError(t, err)
				change := InvariantEntry{EntryType: entry.TypeOffer, Before: blob, After: final}
				if deleted {
					change.IsDelete = true
					change.After = nil
					change.DeleteFinal = final
				}
				domain := [32]byte{9}
				transaction := domainTx{stubTx: stubTx{txType: TypeOfferCreate}, domain: &domain}
				rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
				if enabled {
					rules.Enable(amendment.FeatureFixCleanup3_4_0)
				}
				violation := checkValidPermissionedDEX(transaction, TesSUCCESS, []InvariantEntry{change}, existsView{exists: true}, rules.Build())
				if enabled && deleted {
					require.Nil(t, violation)
				} else {
					require.NotNil(t, violation)
				}
			})
		}
	}
}
