package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestBookStepMPTBookBase(t *testing.T) {
	var issuer, counterparty [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(counterparty[:], []byte("counterparty123456789"))
	id := keylet.MakeMPTID(7, issuer)
	domain := [32]byte{1, 2, 3}

	step := NewBookStep(
		NewMPTIssue(id),
		Issue{Currency: "USD", Issuer: counterparty},
		[20]byte{}, [20]byte{}, nil, false,
	)
	step.domainID = &domain

	expected := keylet.Quality(keylet.BookBase(
		keylet.MPTSide(id),
		keylet.IssueSide(keylet.CurrencyBytes("USD"), counterparty),
		&domain,
	), 0).Key
	require.Equal(t, expected, step.bookBaseKey())
}

func TestBookStepMPTFundingAndTransferFee(t *testing.T) {
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

	sb := NewPaymentSandbox(view)
	issue := NewMPTIssue(id)
	step := NewBookStep(Issue{Currency: "XRP"}, issue, alice, bob, nil, true)
	offer := &state.LedgerOffer{Account: state.EncodeAccountIDSafe(alice)}

	funds := step.getOfferFundedAmount(sb, offer)
	require.True(t, funds.IsMPT)
	require.Equal(t, int64(200), funds.MPT)
	require.NoError(t, step.transferFundsWithFee(
		sb, alice, bob,
		NewMPTEitherAmount(110, id),
		NewMPTEitherAmount(100, id),
		issue,
	))

	outstanding, balances := readMPTAmounts(t, sb, id, alice, bob)
	require.Equal(t, uint64(190), outstanding)
	require.Equal(t, []uint64{90, 100}, balances)
}

func TestBookStepMPTTransferRateFinalIssuerDelivery(t *testing.T) {
	var issuer, owner, destination [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(owner[:], []byte("owner123456789012345"))
	copy(destination[:], []byte("destination12345678"))
	id := keylet.MakeMPTID(1, issuer)

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("MPTokensV2").Build()
	putMPTIssuance(t, view, id, 0, 2_500)
	sb := NewPaymentSandbox(view)
	issue := NewMPTIssue(id)

	step := NewBookStep(Issue{Currency: "XRP"}, issue, owner, destination, nil, true)
	require.Equal(t, QualityOne+25_000_000, step.assetTransferRate(sb, issue))

	step.strandDst = issuer
	step.strandDeliver = issue
	require.Equal(t, QualityOne, step.assetTransferRate(sb, issue))
}

func TestBookStepMPTDEXTransferPermission(t *testing.T) {
	var issuer, owner, destination [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(owner[:], []byte("owner123456789012345"))
	copy(destination[:], []byte("destination12345678"))
	id := keylet.MakeMPTID(1, issuer)

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("MPTokensV2").Build()
	view.createAccount(issuer, 100_000_000, 1)
	putMPTIssuance(t, view, id, 0, 0)
	raw := view.data[keylet.MPTIssuance(id).Key]
	issuance, err := state.ParseMPTokenIssuance(raw)
	require.NoError(t, err)
	issuance.Flags = entry.LsfMPTCanTrade
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	view.data[keylet.MPTIssuance(id).Key] = raw
	sb := NewPaymentSandbox(view)
	issue := NewMPTIssue(id)

	step := NewBookStep(Issue{Currency: "XRP"}, issue, owner, destination, nil, false)
	require.False(t, step.checkMPTDEX(sb, owner))

	step.strandDst = issuer
	step.strandDeliver = issue
	require.True(t, step.checkMPTDEX(sb, owner))

	inputStep := NewBookStep(issue, Issue{Currency: "XRP"}, owner, destination, nil, false)
	require.True(t, inputStep.checkMPTDEX(sb, owner))

	issuance.Flags = entry.LsfMPTCanTransfer
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, sb.Update(keylet.MPTIssuance(id), raw))
	require.Equal(t, ter.TecNO_PERMISSION, inputStep.Check(sb))
}
