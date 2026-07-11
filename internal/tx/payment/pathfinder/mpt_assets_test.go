package pathfinder

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestAccountAssetsIncludeEligibleMPTs(t *testing.T) {
	ledger := newMockLedger()
	issuer := testAccountID(0x41)
	holder := testAccountID(0x42)
	zeroHolder := testAccountID(0x43)
	maxedIssuer := testAccountID(0x44)
	for _, account := range [][20]byte{issuer, holder, zeroHolder, maxedIssuer} {
		addAccount(t, ledger, account, 10_000_000, 0)
	}

	liveID := addPathfinderMPTIssuance(t, ledger, issuer, 7, 20, 100)
	addPathfinderMPToken(t, ledger, holder, liveID, 5)
	addPathfinderMPToken(t, ledger, zeroHolder, liveID, 0)
	maxedID := addPathfinderMPTIssuance(t, ledger, maxedIssuer, 8, 100, 100)

	cache := NewRippleLineCache(ledger)
	liveIssue := payment.NewMPTIssue(liveID)
	maxedIssue := payment.NewMPTIssue(maxedID)

	require.True(t, AccountSourceCurrencies(issuer, cache)[liveIssue])
	require.False(t, AccountDestCurrencies(issuer, cache)[liveIssue])
	require.True(t, AccountSourceCurrencies(holder, cache)[liveIssue])
	require.False(t, AccountDestCurrencies(holder, cache)[liveIssue])
	require.False(t, AccountSourceCurrencies(zeroHolder, cache)[liveIssue])
	require.True(t, AccountDestCurrencies(zeroHolder, cache)[liveIssue])
	require.False(t, AccountSourceCurrencies(maxedIssuer, cache)[maxedIssue])
	require.False(t, AccountDestCurrencies(maxedIssuer, cache)[maxedIssue])
}

func TestRippleLineCacheMPTsIgnoreOtherOwnedEntries(t *testing.T) {
	ledger := newMockLedger()
	account := testAccountID(0x51)
	issuer := testAccountID(0x52)
	addAccount(t, ledger, account, 10_000_000, 0)
	addOffer(t, ledger, account, 1,
		state.NewXRPAmountFromInt(1),
		state.NewIssuedAmountFromFloat64(1, "USD", state.EncodeAccountIDSafe(issuer)),
	)
	ensureOwnerDirContains(t, ledger, account, keylet.Offer(account, 1).Key)

	require.Empty(t, NewRippleLineCache(ledger).GetMPTs(account))
}

func TestMPTBookPathStep(t *testing.T) {
	id, err := mptutil.DecodeID("00000004AE123A8556F3CF91154711376AFB0F894F832B3D")
	require.NoError(t, err)
	issue := payment.NewMPTIssue(id)
	step := pathStepForIssue(issue)
	require.Equal(t, 0x60, step.Type)
	require.Equal(t, mptutil.EncodeID(id), step.MPTIssuanceID)
	require.Equal(t, state.EncodeAccountIDSafe(mptutil.Issuer(id)), step.Issuer)
	require.True(t, pathHasSeenBookIssue([]payment.PathStep{step}, issue))
}

func TestSamePathfindingAssetPreservesIOURippling(t *testing.T) {
	var issuerA, issuerB [20]byte
	issuerA[19] = 1
	issuerB[19] = 2

	require.True(t, samePathfindingAsset(
		payment.Issue{Currency: "USD", Issuer: issuerA},
		payment.Issue{Currency: "USD", Issuer: issuerB},
	))
	require.True(t, samePathfindingAsset(
		payment.Issue{},
		payment.Issue{Currency: "XRP"},
	))
	require.False(t, samePathfindingAsset(
		payment.Issue{Currency: "USD", Issuer: issuerA},
		payment.Issue{Currency: "EUR", Issuer: issuerA},
	))

	mptA := payment.NewMPTIssue(keylet.MakeMPTID(1, issuerA))
	mptB := payment.NewMPTIssue(keylet.MakeMPTID(2, issuerA))
	require.True(t, samePathfindingAsset(mptA, mptA))
	require.False(t, samePathfindingAsset(mptA, mptB))
	require.False(t, samePathfindingAsset(mptA, payment.Issue{Currency: "USD", Issuer: issuerA}))
}

type timedPathfinderLedger struct {
	*mockLedgerView
	parentCloseTime time.Time
}

func (l *timedPathfinderLedger) ParentCloseTime() time.Time {
	return l.parentCloseTime
}

func TestPathfinderUsesLedgerParentCloseTimeForMPTAuthorization(t *testing.T) {
	const rippleSeconds = uint32(12345)
	ledger := &timedPathfinderLedger{
		mockLedgerView: newMockLedger(),
		parentCloseTime: time.Unix(
			protocol.RippleEpochUnix+int64(rippleSeconds), 0,
		),
	}
	var src, dst [20]byte
	src[19] = 1
	dst[19] = 2
	id := keylet.MakeMPTID(1, src)
	issue := payment.NewMPTIssue(id)
	amount := state.NewMPTAmountWithIssuanceID(1, state.EncodeAccountIDSafe(src), mptutil.EncodeID(id))

	closeTime := ledgerParentCloseTime(ledger)
	require.Equal(t, rippleSeconds, closeTime)
	pf := NewPathfinderForIssue(
		ledger, NewRippleLineCache(ledger), src, dst,
		amount, amount, issue, false, closeTime,
	)
	require.Equal(t, rippleSeconds, pf.parentCloseTime)
}

func TestIsValidAssetRejectsMPTWithZeroIssuer(t *testing.T) {
	var id [24]byte
	id[3] = 1
	amount := state.NewMPTAmountWithIssuanceID(1, "", mptutil.EncodeID(id))
	require.False(t, IsValidAsset(amount))

	id[23] = 1
	amount = state.NewMPTAmountWithIssuanceID(1, "", mptutil.EncodeID(id))
	require.True(t, IsValidAsset(amount))

	zeroID, ok := ParseSourceMPTID("0")
	require.True(t, ok)
	require.Equal(t, [24]byte{}, zeroID)
}

func addPathfinderMPTIssuance(
	t *testing.T,
	ledger *mockLedgerView,
	issuer [20]byte,
	sequence uint32,
	outstanding, maximum uint64,
) [24]byte {
	t.Helper()
	issuance := &state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          sequence,
		OutstandingAmount: outstanding,
		MaximumAmount:     &maximum,
	}
	data, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	id := keylet.MakeMPTID(sequence, issuer)
	k := keylet.MPTIssuance(id)
	ledger.entries[k.Key] = data
	ensureOwnerDirContains(t, ledger, issuer, k.Key)
	return id
}

func addPathfinderMPToken(
	t *testing.T,
	ledger *mockLedgerView,
	holder [20]byte,
	id [24]byte,
	amount uint64,
) {
	t.Helper()
	token := &state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		MPTAmount:         amount,
	}
	data, err := state.SerializeMPToken(token)
	require.NoError(t, err)
	k := keylet.MPTokenByID(id, holder)
	ledger.entries[k.Key] = data
	ensureOwnerDirContains(t, ledger, holder, k.Key)
}
