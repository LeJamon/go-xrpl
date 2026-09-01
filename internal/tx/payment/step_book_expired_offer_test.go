package payment

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type offerCleanupFaultView struct {
	*paymentMockLedgerView
	faultKey [32]byte
	fault    bool
}

func (v *offerCleanupFaultView) Read(k keylet.Keylet) ([]byte, error) {
	if v.fault && k.Key == v.faultKey {
		return nil, errors.New("injected offer cleanup read failure")
	}
	return v.paymentMockLedgerView.Read(k)
}

type expiredOfferFixture struct {
	base       *paymentMockLedgerView
	view       *offerCleanupFaultView
	sandbox    *PaymentSandbox
	step       *BookStep
	offer      *state.LedgerOffer
	owner      [20]byte
	offerKey   [32]byte
	ownerDir   [32]byte
	bookDir    [32]byte
	accountKey [32]byte
	sponsor    [20]byte
}

func newExpiredOfferFixture(t *testing.T) *expiredOfferFixture {
	t.Helper()

	owner := [20]byte{1}
	gateway := [20]byte{2}
	source := [20]byte{3}
	destination := [20]byte{4}
	base := newPaymentMockLedgerView()
	base.createAccount(owner, 1_000_000_000, 1)

	step := NewBookStep(
		Issue{Currency: "USD", Issuer: gateway},
		Issue{Currency: "XRP"},
		source,
		destination,
		nil,
		false,
	)
	step.parentCloseTime = 1_000
	bookDir := step.bookBaseKey()
	binary.BigEndian.PutUint64(bookDir[24:], 0x5500000000000000)
	offerKey := keylet.Offer(owner, 1).Key

	ownerDir := keylet.OwnerDir(owner).Key
	ownerResult, err := state.DirInsert(
		base,
		keylet.Keylet{Key: ownerDir},
		offerKey,
		false,
		func(dir *state.DirectoryNode) { dir.Owner = owner },
	)
	require.NoError(t, err)
	bookResult, err := state.DirInsert(
		base,
		keylet.Keylet{Key: bookDir},
		offerKey,
		true,
		func(dir *state.DirectoryNode) {
			dir.TakerPaysCurrency = keylet.CurrencyBytes("USD")
			dir.TakerPaysIssuer = gateway
		},
	)
	require.NoError(t, err)

	offer := &state.LedgerOffer{
		Account:       state.EncodeAccountIDSafe(owner),
		Sequence:      1,
		TakerPays:     tx.NewIssuedAmountFromFloat64(100, "USD", state.EncodeAccountIDSafe(gateway)),
		TakerGets:     tx.NewXRPAmount(100_000_000),
		BookDirectory: bookDir,
		BookNode:      bookResult.Page,
		OwnerNode:     ownerResult.Page,
		Expiration:    step.parentCloseTime - 1,
	}
	offerData, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err)
	base.data[offerKey] = offerData

	view := &offerCleanupFaultView{paymentMockLedgerView: base}
	return &expiredOfferFixture{
		base:       base,
		view:       view,
		sandbox:    NewPaymentSandbox(view),
		step:       step,
		offer:      offer,
		owner:      owner,
		offerKey:   offerKey,
		ownerDir:   ownerDir,
		bookDir:    bookDir,
		accountKey: keylet.Account(owner).Key,
	}
}

func (f *expiredOfferFixture) walk() (map[[32]byte]bool, error) {
	removals := make(map[[32]byte]bool)
	_, _, err := f.step.getNextOfferSkipVisited(
		f.sandbox,
		NewChildSandbox(f.sandbox),
		removals,
		make(map[[32]byte]bool),
		true,
	)
	return removals, err
}

func requireDirectoryEntry(t *testing.T, sb *PaymentSandbox, key, offerKey [32]byte, present bool) {
	t.Helper()
	data, err := sb.Read(keylet.Keylet{Key: key})
	require.NoError(t, err)
	if !present {
		require.Nil(t, data)
		return
	}
	require.NotNil(t, data)
	dir, err := state.ParseDirectoryNode(data)
	require.NoError(t, err)
	require.Contains(t, dir.Indexes, offerKey)
}

func requireExpiredOfferState(t *testing.T, f *expiredOfferFixture, present bool, ownerCount uint32) {
	t.Helper()
	offerData, err := f.sandbox.Read(keylet.Keylet{Key: f.offerKey})
	require.NoError(t, err)
	if present {
		require.NotNil(t, offerData)
	} else {
		require.Nil(t, offerData)
	}
	requireDirectoryEntry(t, f.sandbox, f.ownerDir, f.offerKey, present)
	requireDirectoryEntry(t, f.sandbox, f.bookDir, f.offerKey, present)
	accountData, err := f.sandbox.Read(keylet.Keylet{Key: f.accountKey})
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(accountData)
	require.NoError(t, err)
	require.Equal(t, ownerCount, account.OwnerCount)
}

func requireAccountOwnerCounts(t *testing.T, sb *PaymentSandbox, accountID [20]byte, want tx.OwnerCounts) {
	t.Helper()
	data, err := sb.Read(keylet.Account(accountID))
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	require.Equal(t, want.Owner, account.OwnerCount)
	require.Equal(t, want.Sponsored, account.SponsoredOwnerCount)
	require.Equal(t, want.Sponsoring, account.SponsoringOwnerCount)
}

func (f *expiredOfferFixture) addSponsor(t *testing.T) {
	t.Helper()
	f.sponsor = [20]byte{5}
	ownerData := f.base.data[f.accountKey]
	owner, err := state.ParseAccountRoot(ownerData)
	require.NoError(t, err)
	owner.SponsoredOwnerCount = 1
	ownerData, err = state.SerializeAccountRoot(owner)
	require.NoError(t, err)
	f.base.data[f.accountKey] = ownerData

	f.base.createAccount(f.sponsor, 1_000_000_000, 0)
	sponsorKey := keylet.Account(f.sponsor).Key
	sponsorData := f.base.data[sponsorKey]
	sponsor, err := state.ParseAccountRoot(sponsorData)
	require.NoError(t, err)
	sponsor.SponsoringOwnerCount = 1
	sponsorData, err = state.SerializeAccountRoot(sponsor)
	require.NoError(t, err)
	f.base.data[sponsorKey] = sponsorData

	f.offer.Sponsor = state.EncodeAccountIDSafe(f.sponsor)
	offerData, err := state.SerializeLedgerOffer(f.offer)
	require.NoError(t, err)
	f.base.data[f.offerKey] = offerData
}

func TestExpiredOfferCleanupRollsBackFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *expiredOfferFixture)
		code  ter.Result
	}{
		{
			name: "directory deletion error",
			setup: func(_ *testing.T, f *expiredOfferFixture) {
				f.view.faultKey = f.ownerDir
				f.view.fault = true
			},
			code: ter.TefINTERNAL,
		},
		{
			name: "unsuccessful removal",
			setup: func(t *testing.T, f *expiredOfferFixture) {
				f.offer.BookNode = 1
				data, err := state.SerializeLedgerOffer(f.offer)
				require.NoError(t, err)
				f.base.data[f.offerKey] = data
			},
			code: ter.TefBAD_LEDGER,
		},
		{
			name: "owner count error",
			setup: func(_ *testing.T, f *expiredOfferFixture) {
				f.view.faultKey = f.accountKey
				f.view.fault = true
			},
			code: ter.TefINTERNAL,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExpiredOfferFixture(t)
			test.setup(t, fixture)

			removals, err := fixture.walk()

			require.Error(t, err)
			require.Equal(t, test.code, offerCleanupResult(err))
			require.Empty(t, removals)
			require.Empty(t, fixture.step.PermRemovals())
			fixture.view.fault = false
			requireExpiredOfferState(t, fixture, true, 1)
		})
	}
}

func TestExpiredOfferCleanupSuccessAndRetry(t *testing.T) {
	fixture := newExpiredOfferFixture(t)
	fixture.view.faultKey = fixture.ownerDir
	fixture.view.fault = true

	removals, err := fixture.walk()
	require.Error(t, err)
	require.Empty(t, removals)
	require.Empty(t, fixture.step.PermRemovals())

	fixture.view.fault = false
	removals, err = fixture.walk()
	require.NoError(t, err)
	require.True(t, removals[fixture.offerKey])
	require.True(t, fixture.step.PermRemovals()[fixture.offerKey])
	requireExpiredOfferState(t, fixture, false, 0)

	require.NoError(t, offerDeleteInSandbox(fixture.sandbox, fixture.offerKey))
	requireExpiredOfferState(t, fixture, false, 0)
}

func TestExpiredOfferCleanupRejectsInvalidOwner(t *testing.T) {
	fixture := newExpiredOfferFixture(t)
	invalid := *fixture.offer
	invalid.Account = "not-an-account"

	err := fixture.step.removeExpiredOffer(fixture.sandbox, &invalid, fixture.offerKey)

	require.ErrorContains(t, err, "decode offer owner")
	require.Equal(t, ter.TefINTERNAL, offerCleanupResult(err))
	requireExpiredOfferState(t, fixture, true, 1)
}

func TestSponsoredExpiredOfferCleanupAdjustsReserveCounts(t *testing.T) {
	fixture := newExpiredOfferFixture(t)
	fixture.addSponsor(t)
	sponsorKey := keylet.Account(fixture.sponsor).Key
	fixture.view.faultKey = sponsorKey
	fixture.view.fault = true

	removals, err := fixture.walk()
	require.Error(t, err)
	require.Empty(t, removals)
	require.Empty(t, fixture.step.PermRemovals())
	fixture.view.fault = false
	requireExpiredOfferState(t, fixture, true, 1)
	requireAccountOwnerCounts(t, fixture.sandbox, fixture.owner, tx.OwnerCounts{Owner: 1, Sponsored: 1})
	requireAccountOwnerCounts(t, fixture.sandbox, fixture.sponsor, tx.OwnerCounts{Sponsoring: 1})

	removals, err = fixture.walk()
	require.NoError(t, err)
	require.True(t, removals[fixture.offerKey])
	require.True(t, fixture.step.PermRemovals()[fixture.offerKey])
	requireExpiredOfferState(t, fixture, false, 0)
	requireAccountOwnerCounts(t, fixture.sandbox, fixture.owner, tx.OwnerCounts{})
	requireAccountOwnerCounts(t, fixture.sandbox, fixture.sponsor, tx.OwnerCounts{})
}

func TestConsumedSponsoredOfferCleanupAdjustsReserveCounts(t *testing.T) {
	fixture := newExpiredOfferFixture(t)
	fixture.addSponsor(t)

	err := fixture.step.deleteOffer(fixture.sandbox, fixture.offer, fixture.owner)

	require.NoError(t, err)
	requireExpiredOfferState(t, fixture, false, 0)
	requireAccountOwnerCounts(t, fixture.sandbox, fixture.owner, tx.OwnerCounts{})
	requireAccountOwnerCounts(t, fixture.sandbox, fixture.sponsor, tx.OwnerCounts{})
}

func TestDeferredOfferCleanupIsAtomic(t *testing.T) {
	fixture := newExpiredOfferFixture(t)
	fixture.view.faultKey = fixture.accountKey
	fixture.view.fault = true

	require.Error(t, offerDeleteInSandbox(fixture.sandbox, fixture.offerKey))
	fixture.view.fault = false
	requireExpiredOfferState(t, fixture, true, 1)

	require.NoError(t, offerDeleteInSandbox(fixture.sandbox, fixture.offerKey))
	requireExpiredOfferState(t, fixture, false, 0)
}

func TestFlowAbortsFatalOfferCleanupFailure(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)
	goodCalls := 0
	failing := &fakeStep{revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
		throwOfferCleanupFailure(errors.New("cleanup failed"))
		return out, out
	}}
	succeeding := &fakeStep{revFn: func(out EitherAmount) (EitherAmount, EitherAmount) {
		goodCalls++
		return out, out
	}}

	result := Flow(
		sandbox,
		[]Strand{{failing}, {succeeding}},
		NewXRPEitherAmount(10),
		false,
		nil,
		nil,
		nil,
		false,
	)

	require.Equal(t, ter.TefINTERNAL, result.Result)
	require.Nil(t, result.Sandbox)
	require.Nil(t, result.RemovableOffers)
	require.Zero(t, goodCalls)
}
