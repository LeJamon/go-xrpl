package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestPaymentSandboxDefersMPTCreditsAndIssuerSelfDebits(t *testing.T) {
	var id [24]byte
	copy(id[4:], []byte("issuer-account-id-123"))
	issuer := mptIssuer(id)
	var alice, bob [20]byte
	copy(alice[:], []byte("alice-account-id-1234"))
	copy(bob[:], []byte("bob-account-id-123456"))

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("MPTokensV2").Build()
	view.createAccount(issuer, 100_000_000, 1)
	view.createAccount(alice, 100_000_000, 1)
	view.createAccount(bob, 100_000_000, 1)
	maximum := uint64(500)
	issuance := &state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          1,
		OutstandingAmount: 200,
		MaximumAmount:     &maximum,
	}
	raw, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	view.data[keylet.MPTIssuance(id).Key] = raw
	putMPTHolding(t, view, id, alice, 200)
	putMPTHolding(t, view, id, bob, 0)

	root := NewPaymentSandbox(view)
	child := NewChildSandbox(root)
	actual, result := mptutil.Send(child, id, alice, bob, 100, true, false)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, int64(100), actual)
	require.NoError(t, child.Apply(root))

	aliceFunds, result := mptutil.Funds(root, id, alice, false)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, int64(100), aliceFunds)
	bobFunds, result := mptutil.Funds(root, id, bob, false)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Zero(t, bobFunds)
	issuerFunds, result := mptutil.Funds(root, id, issuer, false)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, int64(200), issuerFunds)

	require.Equal(t, ter.TesSUCCESS, mptutil.RecordIssuerSelfDebit(root, id, 75))
	selfIssueFunds, result := mptutil.IssuerFundsToSelfIssue(root, id)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, int64(225), selfIssueFunds)
}
