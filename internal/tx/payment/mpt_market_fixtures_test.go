package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/testing/marketfixtures"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestMPTMarketFeeFixtures(t *testing.T) {
	for _, fixture := range marketfixtures.MPTFees() {
		t.Run(fixture.Name, func(t *testing.T) {
			issuer, alice, bob := [20]byte{1}, [20]byte{2}, [20]byte{3}
			id := keylet.MakeMPTID(1, issuer)
			view := newPaymentMockLedgerView()
			view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureMPTokensV2).Build()
			for _, account := range [][20]byte{issuer, alice, bob} {
				view.createAccount(account, 100_000_000, 1)
			}
			putMPTIssuance(t, view, id, uint64(fixture.Funds), fixture.Fee)
			putMPTHolding(t, view, id, alice, uint64(fixture.Funds))
			putMPTHolding(t, view, id, bob, 0)
			sb := NewPaymentSandbox(view)
			deliver := state.NewMPTAmountWithIssuanceID(fixture.Delivered, state.EncodeAccountIDSafe(issuer), keyletIDHex(id))
			strands, result := ToStrands(sb, alice, bob, deliver, nil, nil, true, false)
			require.Equal(t, ter.TesSUCCESS, result)
			sendMax := NewMPTEitherAmount(fixture.Funds, id)
			flow := Flow(sb, strands, NewMPTEitherAmount(fixture.Delivered, id), false, nil, &sendMax, nil, false)
			require.Equal(t, ter.TesSUCCESS, flow.Result)
			require.Equal(t, fixture.Delivered, flow.Out.MPT)
			require.Equal(t, fixture.Debited, flow.In.MPT)
			require.NoError(t, flow.Sandbox.Apply(sb))
			require.NoError(t, sb.ApplyToView(view))
			outstanding, balances := readMPTAmounts(t, view, id, alice, bob)
			require.Equal(t, []uint64{uint64(fixture.Funds - fixture.Debited), uint64(fixture.Delivered)}, balances)
			require.Equal(t, uint64(fixture.Funds-fixture.Debited+fixture.Delivered), outstanding)
		})
	}
}
