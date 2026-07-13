package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func putMPTIssuance(t *testing.T, view *paymentMockLedgerView, id [24]byte, outstanding uint64, fee uint16) {
	t.Helper()
	issuance := &state.MPTokenIssuanceData{
		Issuer:            mptIssuer(id),
		Sequence:          1,
		OutstandingAmount: outstanding,
		TransferFee:       fee,
		Flags:             entry.LsfMPTCanTransfer | entry.LsfMPTCanTrade,
	}
	data, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	view.data[keylet.MPTIssuance(id).Key] = data
}

func putMPTHolding(t *testing.T, view *paymentMockLedgerView, id [24]byte, holder [20]byte, amount uint64) {
	t.Helper()
	token := &state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		MPTAmount:         amount,
	}
	data, err := state.SerializeMPToken(token)
	require.NoError(t, err)
	view.data[keylet.MPTokenByID(id, holder).Key] = data
}

func readMPTAmounts(t *testing.T, view state.LedgerView, id [24]byte, holders ...[20]byte) (uint64, []uint64) {
	t.Helper()
	issuanceRaw, err := view.Read(keylet.MPTIssuance(id))
	require.NoError(t, err)
	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	require.NoError(t, err)
	amounts := make([]uint64, len(holders))
	for i, holder := range holders {
		raw, readErr := view.Read(keylet.MPTokenByID(id, holder))
		require.NoError(t, readErr)
		token, parseErr := state.ParseMPToken(raw)
		require.NoError(t, parseErr)
		amounts[i] = token.MPTAmount
	}
	return issuance.OutstandingAmount, amounts
}

func TestEitherAmountMPT(t *testing.T) {
	id := [24]byte{0, 0, 0, 1, 1, 2, 3}
	a := NewMPTEitherAmount(10, id)
	b := NewMPTEitherAmount(3, id)

	require.True(t, a.IsMPT)
	require.Equal(t, int64(13), a.Add(b).MPT)
	require.Equal(t, int64(7), a.Sub(b).MPT)
	require.Equal(t, int64(4), mptMulRatio(10, 1, 3, true))
	require.Equal(t, int64(3), mptMulRatio(10, 1, 3, false))
	require.PanicsWithValue(t, "division by zero", func() { mptMulRatio(10, 1, 0, false) })

	roundTrip := ToEitherAmount(FromEitherAmount(a))
	require.Equal(t, a, roundTrip)
	require.Equal(t, qualityOne.Value, QualityFromAmounts(a, a).Value)
}

func TestMPTEndpointStepIssuerToHolder(t *testing.T) {
	var issuer, holder [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(holder[:], []byte("holder12345678901234"))
	id := keylet.MakeMPTID(1, issuer)

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("MPTokensV2").Build()
	view.createAccount(issuer, 100_000_000, 1)
	view.createAccount(holder, 100_000_000, 1)
	putMPTIssuance(t, view, id, 0, 0)
	putMPTHolding(t, view, id, holder, 0)
	sb := NewPaymentSandbox(view)

	ctx := NewStrandContext(sb, issuer, holder)
	ctx.StrandDeliver = NewMPTIssue(id)
	step, result := NewMPTEndpointStep(ctx, issuer, holder, NewMPTIssue(id), nil, true, true)
	require.Equal(t, ter.TesSUCCESS, result)

	in, out := step.Rev(sb, NewChildSandbox(sb), nil, NewMPTEitherAmount(100, id))
	require.Equal(t, int64(100), in.MPT)
	require.Equal(t, int64(100), out.MPT)

	outstanding, balances := readMPTAmounts(t, sb, id, holder)
	require.Equal(t, uint64(100), outstanding)
	require.Equal(t, []uint64{100}, balances)
}

func TestMPTEndpointStepHolderToHolderChargesTransferFee(t *testing.T) {
	var issuer, alice, bob [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(alice[:], []byte("alice123456789012345"))
	copy(bob[:], []byte("bob12345678901234567"))
	id := keylet.MakeMPTID(1, issuer)

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("MPTokensV2").Build()
	view.createAccount(issuer, 100_000_000, 1)
	view.createAccount(alice, 100_000_000, 1)
	view.createAccount(bob, 100_000_000, 1)
	putMPTIssuance(t, view, id, 200, 10_000)
	putMPTHolding(t, view, id, alice, 200)
	putMPTHolding(t, view, id, bob, 0)

	deliver := state.NewMPTAmountWithIssuanceID(100, state.EncodeAccountIDSafe(issuer), keyletIDHex(id))
	sb := NewPaymentSandbox(view)
	strands, result := ToStrands(sb, alice, bob, deliver, nil, nil, true, false)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Len(t, strands, 1)
	require.Len(t, strands[0], 2)

	flowResult := Flow(sb, strands, NewMPTEitherAmount(100, id), false, nil, nil, nil, false)
	require.Equal(t, ter.TesSUCCESS, flowResult.Result)
	require.Equal(t, int64(110), flowResult.In.MPT)
	require.Equal(t, int64(100), flowResult.Out.MPT)
	require.NoError(t, flowResult.Sandbox.Apply(sb))
	require.NoError(t, sb.ApplyToView(view))

	outstanding, balances := readMPTAmounts(t, view, id, alice, bob)
	require.Equal(t, uint64(190), outstanding)
	require.Equal(t, []uint64{90, 100}, balances)
}

func TestMPTEndpointStepCheckMatchesRippledOrdering(t *testing.T) {
	var issuer, alice, bob [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(alice[:], []byte("alice123456789012345"))
	copy(bob[:], []byte("bob12345678901234567"))
	id := keylet.MakeMPTID(1, issuer)
	issue := NewMPTIssue(id)

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("MPTokensV2").Build()
	view.createAccount(alice, 100_000_000, 1)
	putMPTIssuance(t, view, id, 100, 0)
	raw := view.data[keylet.MPTIssuance(id).Key]
	issuance, err := state.ParseMPTokenIssuance(raw)
	require.NoError(t, err)
	issuance.Flags |= entry.LsfMPTLocked
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	view.data[keylet.MPTIssuance(id).Key] = raw

	sb := NewPaymentSandbox(view)
	ctx := NewStrandContext(sb, alice, bob)
	ctx.StrandDeliver = issue
	_, result := NewMPTEndpointStep(ctx, alice, bob, issue, nil, true, false)
	require.Equal(t, ter.TerLOCKED, result)
}

func TestMPTEndpointStepAllowsSeenBookAfterNonBookStep(t *testing.T) {
	var issuer, holder [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(holder[:], []byte("holder12345678901234"))
	id := keylet.MakeMPTID(1, issuer)
	issue := NewMPTIssue(id)

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("MPTokensV2").Build()
	view.createAccount(issuer, 100_000_000, 1)
	putMPTIssuance(t, view, id, 0, 0)

	sb := NewPaymentSandbox(view)
	ctx := NewStrandContext(sb, issuer, holder)
	ctx.OfferCrossing = true
	ctx.StrandDeliver = issue
	ctx.SeenBookOuts[issue] = true
	_, result := NewMPTEndpointStep(ctx, issuer, holder, issue, &fakeStep{}, false, true)
	require.Equal(t, ter.TesSUCCESS, result)
}

func keyletIDHex(id [24]byte) string {
	return FromEitherAmount(NewMPTEitherAmount(0, id)).MPTIssuanceID()
}

var _ tx.LedgerView = (*paymentMockLedgerView)(nil)
