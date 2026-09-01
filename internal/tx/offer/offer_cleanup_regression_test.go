package offer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type sponsoredOfferFixture struct {
	owner    [20]byte
	sponsor  [20]byte
	offer    *state.LedgerOffer
	offerKey keylet.Keylet
}

func putOfferCleanupAccount(
	t *testing.T,
	view *offerMPTLedgerView,
	account [20]byte,
	counts tx.OwnerCounts,
) *state.AccountRoot {
	t.Helper()
	root := &state.AccountRoot{
		Account:              state.EncodeAccountIDSafe(account),
		Balance:              1_000_000_000,
		Sequence:             2,
		OwnerCount:           counts.Owner,
		SponsoredOwnerCount:  counts.Sponsored,
		SponsoringOwnerCount: counts.Sponsoring,
	}
	data, err := state.SerializeAccountRoot(root)
	require.NoError(t, err)
	view.data[keylet.Account(account).Key] = data
	return root
}

func putSponsoredOffer(
	t *testing.T,
	view *offerMPTLedgerView,
	owner, sponsor [20]byte,
) sponsoredOfferFixture {
	t.Helper()

	putOfferCleanupAccount(t, view, owner, tx.OwnerCounts{Owner: 1, Sponsored: 1})
	putOfferCleanupAccount(t, view, sponsor, tx.OwnerCounts{Sponsoring: 1})

	gateway := [20]byte{9}
	putOfferCleanupAccount(t, view, gateway, tx.OwnerCounts{})
	offerKey := keylet.Offer(owner, 1)
	ownerDir := keylet.OwnerDir(owner)
	ownerResult, err := state.DirInsert(view, ownerDir, offerKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = owner
	})
	require.NoError(t, err)

	bookDir := keylet.Keylet{Key: [32]byte{0x55, 1}}
	bookResult, err := state.DirInsert(view, bookDir, offerKey.Key, true, func(dir *state.DirectoryNode) {
		dir.TakerPaysCurrency = keylet.CurrencyBytes("USD")
		dir.TakerPaysIssuer = gateway
	})
	require.NoError(t, err)

	offer := &state.LedgerOffer{
		Account:       state.EncodeAccountIDSafe(owner),
		Sequence:      1,
		TakerPays:     tx.NewIssuedAmountFromFloat64(100, "USD", state.EncodeAccountIDSafe(gateway)),
		TakerGets:     tx.NewXRPAmount(100_000_000),
		BookDirectory: bookDir.Key,
		BookNode:      bookResult.Page,
		OwnerNode:     ownerResult.Page,
		Sponsor:       state.EncodeAccountIDSafe(sponsor),
	}
	offerData, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err)
	view.data[offerKey.Key] = offerData

	return sponsoredOfferFixture{
		owner:    owner,
		sponsor:  sponsor,
		offer:    offer,
		offerKey: offerKey,
	}
}

func readOfferCleanupAccount(t *testing.T, view tx.LedgerView, account [20]byte) *state.AccountRoot {
	t.Helper()
	data, err := view.Read(keylet.Account(account))
	require.NoError(t, err)
	require.NotNil(t, data)
	root, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	return root
}

func TestRemoveRemovableOffersPropagatesCleanupFailure(t *testing.T) {
	view := newOfferMPTLedgerView()
	offerKey := [32]byte{1}
	view.data[offerKey] = []byte{1}
	sb := payment.NewPaymentSandbox(view)
	sbCancel := payment.NewPaymentSandbox(view)

	err := removeRemovableOffers(sb, sbCancel, map[[32]byte]bool{offerKey: true}, nil)

	require.Error(t, err)
	require.Equal(t, ter.TefINTERNAL, cleanupResult(err))
	data, readErr := sb.Read(keylet.Keylet{Key: offerKey})
	require.NoError(t, readErr)
	require.Equal(t, []byte{1}, data)
}

func TestRemoveRemovableOffersAdjustsSponsoredCountersInBothSandboxes(t *testing.T) {
	view := newOfferMPTLedgerView()
	fixture := putSponsoredOffer(t, view, [20]byte{1}, [20]byte{2})
	sb := payment.NewPaymentSandbox(view)
	sbCancel := payment.NewPaymentSandbox(view)

	err := removeRemovableOffers(
		sb,
		sbCancel,
		map[[32]byte]bool{fixture.offerKey.Key: true},
		map[[32]byte]bool{fixture.offerKey.Key: true},
	)
	require.NoError(t, err)

	for _, sandbox := range []*payment.PaymentSandbox{sb, sbCancel} {
		offerData, readErr := sandbox.Read(fixture.offerKey)
		require.NoError(t, readErr)
		require.Nil(t, offerData)
		owner := readOfferCleanupAccount(t, sandbox, fixture.owner)
		require.Zero(t, owner.OwnerCount)
		require.Zero(t, owner.SponsoredOwnerCount)
		sponsor := readOfferCleanupAccount(t, sandbox, fixture.sponsor)
		require.Zero(t, sponsor.SponsoringOwnerCount)
	}
}

func TestOfferCreateRefreshesSenderSponsoredCounters(t *testing.T) {
	view := newOfferMPTLedgerView()
	fixture := putSponsoredOffer(t, view, [20]byte{1}, [20]byte{2})
	account := readOfferCleanupAccount(t, view, fixture.owner)
	sequence := uint32(2)
	cancelSequence := uint32(1)
	create := NewOfferCreate(account.Account, tx.NewXRPAmount(50_000_000), fixture.offer.TakerPays)
	create.Common.Sequence = &sequence
	create.OfferSequence = &cancelSequence
	rules := amendment.AllSupportedRules()
	ctx := &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: fixture.owner,
		Common:    create.GetCommon(),
		Config: tx.EngineConfig{
			ReserveBase:      10_000_000,
			ReserveIncrement: 2_000_000,
			LedgerSequence:   10,
			Rules:            rules,
		},
		Log: xrpllog.Discard(),
		Ctx: context.Background(),
	}

	result := create.ApplyCreate(ctx)

	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, uint32(1), ctx.Account.OwnerCount)
	require.Zero(t, ctx.Account.SponsoredOwnerCount)
	require.Zero(t, ctx.Account.SponsoringOwnerCount)
	sponsor := readOfferCleanupAccount(t, view, fixture.sponsor)
	require.Zero(t, sponsor.SponsoringOwnerCount)
	oldOffer, err := view.Read(fixture.offerKey)
	require.NoError(t, err)
	require.Nil(t, oldOffer)
	newOffer, err := view.Read(keylet.Offer(fixture.owner, sequence))
	require.NoError(t, err)
	require.NotNil(t, newOffer)
}

func TestOfferCancelAdjustsSponsoredCounters(t *testing.T) {
	view := newOfferMPTLedgerView()
	fixture := putSponsoredOffer(t, view, [20]byte{1}, [20]byte{2})
	account := readOfferCleanupAccount(t, view, fixture.owner)
	cancel := NewOfferCancel(account.Account, fixture.offer.Sequence)
	ctx := &tx.ApplyContext{
		View:      view,
		Account:   account,
		AccountID: fixture.owner,
		Common:    cancel.GetCommon(),
		Log:       xrpllog.Discard(),
		Ctx:       context.Background(),
	}

	result := cancel.Apply(ctx)

	require.Equal(t, ter.TesSUCCESS, result)
	require.Zero(t, ctx.Account.OwnerCount)
	require.Zero(t, ctx.Account.SponsoredOwnerCount)
	sponsor := readOfferCleanupAccount(t, view, fixture.sponsor)
	require.Zero(t, sponsor.SponsoringOwnerCount)
	offerData, err := view.Read(fixture.offerKey)
	require.NoError(t, err)
	require.Nil(t, offerData)
}
