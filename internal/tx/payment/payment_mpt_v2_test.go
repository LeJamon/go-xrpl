package payment

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

func paymentMPTV2Rules() *amendment.Rules {
	return amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureMPTokensV2).
		Build()
}

func TestPaymentMPTokensV2Preflight(t *testing.T) {
	var issuerA, issuerB, source, destination [20]byte
	issuerA[19] = 1
	issuerB[19] = 2
	source[19] = 3
	destination[19] = 4
	idA := keylet.MakeMPTID(1, issuerA)
	idB := keylet.MakeMPTID(2, issuerB)
	amount := state.NewMPTAmountWithIssuanceID(10, state.EncodeAccountIDSafe(issuerA), mptutil.EncodeID(idA))
	sendMax := state.NewMPTAmountWithIssuanceID(20, state.EncodeAccountIDSafe(issuerB), mptutil.EncodeID(idB))
	p := NewPayment(state.EncodeAccountIDSafe(source), state.EncodeAccountIDSafe(destination), amount)
	p.SendMax = &sendMax
	p.Paths = [][]PathStep{{{MPTIssuanceID: mptutil.EncodeID(idA)}}}
	p.SetFlags(PaymentFlagLimitQuality | PaymentFlagNoDirectRipple)

	require.NoError(t, p.ValidateRules(paymentMPTV2Rules()))
	require.ErrorContains(t, p.Validate(), "temMALFORMED")
	require.Equal(t, tfPaymentMask, p.GetFlagsMask(paymentMPTV2Rules()))
	require.Equal(t, tfMPTPaymentMask, p.GetFlagsMask(amendment.AllSupportedRules()))
}

func TestPaymentMPTPathFlatten(t *testing.T) {
	var issuer [20]byte
	issuer[19] = 1
	id := keylet.MakeMPTID(1, issuer)
	idHex := mptutil.EncodeID(id)
	p := NewPayment("rAlice", "rBob", state.NewMPTAmountWithIssuanceID(10, state.EncodeAccountIDSafe(issuer), idHex))
	p.Paths = [][]PathStep{{{MPTIssuanceID: idHex}}}

	flat, err := p.Flatten()
	require.NoError(t, err)
	paths := requirePathSet(t, flat, 1)
	steps, ok := paths[0].([]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"mpt_issuance_id": idHex}, steps[0])
	require.NoError(t, p.ValidateRules(paymentMPTV2Rules()))
}

func TestPaymentMPTokensV2RejectsZeroIssuer(t *testing.T) {
	var id [24]byte
	id[3] = 1
	p := NewPayment("rAlice", "rBob", state.NewMPTAmountWithIssuanceID(10, "", mptutil.EncodeID(id)))
	require.ErrorContains(t, p.ValidateRules(paymentMPTV2Rules()), "temBAD_CURRENCY")
}

func TestPaymentMPTokensV1PreclaimChecksDestination(t *testing.T) {
	var issuer [20]byte
	issuer[19] = 1
	id := keylet.MakeMPTID(1, issuer)
	p := NewPayment("rAlice", state.EncodeAccountIDSafe([20]byte{19: 2}),
		state.NewMPTAmountWithIssuanceID(10, state.EncodeAccountIDSafe(issuer), mptutil.EncodeID(id)))

	result := p.Preclaim(newPaymentMockLedgerView(), tx.EngineConfig{
		Rules: amendment.AllSupportedRules(),
	})
	require.Equal(t, ter.TecNO_DST, result)
}

func TestPaymentApplyRoutesMPTokensV2ThroughFlow(t *testing.T) {
	var issuer, source, destination [20]byte
	issuer[19] = 1
	source[19] = 2
	destination[19] = 3
	id := keylet.MakeMPTID(1, issuer)
	idHex := mptutil.EncodeID(id)
	rules := paymentMPTV2Rules()
	view := newPaymentMockLedgerView()
	view.rules = rules
	for _, account := range [][20]byte{issuer, source, destination} {
		view.createAccount(account, 100_000_000, 0)
	}
	putMPTIssuance(t, view, id, 100, 0)
	putMPTHolding(t, view, id, source, 100)
	putMPTHolding(t, view, id, destination, 0)

	accountRaw, err := view.Read(keylet.Account(source))
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(accountRaw)
	require.NoError(t, err)
	amount := state.NewMPTAmountWithIssuanceID(10, state.EncodeAccountIDSafe(issuer), idHex)
	p := NewPayment(state.EncodeAccountIDSafe(source), state.EncodeAccountIDSafe(destination), amount)
	p.Paths = [][]PathStep{{{Account: state.EncodeAccountIDSafe(issuer)}}}
	p.SetNoDirectRipple()
	require.NoError(t, p.ValidateRules(rules))

	ctx := &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: source,
		Config: tx.EngineConfig{
			ReserveBase:      10_000_000,
			ReserveIncrement: 2_000_000,
			LedgerSequence:   1,
			Rules:            rules,
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}
	result := p.Apply(ctx)
	require.Equal(t, ter.TesSUCCESS, result)
	outstanding, balances := readMPTAmounts(t, view, id, source, destination)
	require.Equal(t, uint64(100), outstanding)
	require.Equal(t, []uint64{90, 10}, balances)

	noPath := NewPayment(state.EncodeAccountIDSafe(source), state.EncodeAccountIDSafe(destination), amount)
	noPath.SetNoDirectRipple()
	require.NoError(t, noPath.ValidateRules(rules))
	require.Equal(t, ter.TemRIPPLE_EMPTY, noPath.Apply(ctx))
	outstanding, balances = readMPTAmounts(t, view, id, source, destination)
	require.Equal(t, uint64(100), outstanding)
	require.Equal(t, []uint64{90, 10}, balances)
}
