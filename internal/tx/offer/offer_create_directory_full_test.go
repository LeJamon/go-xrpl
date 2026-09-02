package offer

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

const testDirNodeMaxEntries = 32

type directoryFullOfferFixture struct {
	view      *offerMPTLedgerView
	ctx       *tx.ApplyContext
	offer     *OfferCreate
	takerPays tx.Amount
	takerGets tx.Amount
	rate      uint64
}

func newDirectoryFullOfferFixture(t *testing.T, domainID *[32]byte) directoryFullOfferFixture {
	t.Helper()

	accountID := [20]byte{1}
	issuerID := [20]byte{2}
	view := newOfferMPTLedgerView()
	view.rules = amendment.EmptyRules()
	takerPays := tx.NewIssuedAmountFromFloat64(10, "USD", state.EncodeAccountIDSafe(issuerID))
	takerGets := tx.NewXRPAmount(10_000_000)
	offer := NewOfferCreate(state.EncodeAccountIDSafe(accountID), takerGets, takerPays)
	offer.SetSequence(1)
	offer.DomainID = domainID
	ctx := &tx.ApplyContext{
		View:      view,
		Account:   &state.AccountRoot{Account: offer.Account, Balance: 100_000_000, Sequence: 1},
		AccountID: accountID,
		Common:    offer.GetCommon(),
		Config: tx.EngineConfig{
			LedgerSequence: 2,
			Rules:          view.rules,
		},
		Log: xrpllog.Discard(),
		Ctx: context.Background(),
	}

	return directoryFullOfferFixture{
		view:      view,
		ctx:       ctx,
		offer:     offer,
		takerPays: takerPays,
		takerGets: takerGets,
		rate:      state.GetRateWithNumberContext(takerGets, takerPays, ctx.NumberContext()),
	}
}

func putFullOfferDirectory(
	t *testing.T,
	view *offerMPTLedgerView,
	dirKey keylet.Keylet,
	isBook bool,
	setup func(*state.DirectoryNode),
) {
	t.Helper()

	lastPage := state.DirNodeMaxPages - 1
	root := &state.DirectoryNode{RootIndex: dirKey.Key}
	root.SetIndexNext(lastPage)
	root.SetIndexPrevious(lastPage)
	last := &state.DirectoryNode{
		RootIndex: dirKey.Key,
		Indexes:   make([][32]byte, testDirNodeMaxEntries),
	}
	for i := range last.Indexes {
		last.Indexes[i][0] = byte(i + 1)
	}
	if setup != nil {
		setup(root)
		setup(last)
	}

	rootData, err := state.SerializeDirectoryNode(root, isBook)
	require.NoError(t, err)
	lastData, err := state.SerializeDirectoryNode(last, isBook)
	require.NoError(t, err)
	view.data[dirKey.Key] = rootData
	view.data[keylet.DirPage(dirKey.Key, lastPage).Key] = lastData
}

func (f directoryFullOfferFixture) place(t *testing.T, hybrid bool) (ter.Result, bool) {
	t.Helper()
	sb := payment.NewPaymentSandbox(f.view)
	sb.SetTransactionContext(f.ctx.TxHash, f.ctx.Config.LedgerSequence)
	return f.offer.placeRemainingOffer(
		f.ctx,
		sb,
		f.takerPays,
		f.takerGets,
		f.rate,
		false,
		false,
		hybrid,
	)
}

func TestOfferCreateOwnerDirectoryFull(t *testing.T) {
	fixture := newDirectoryFullOfferFixture(t, nil)
	ownerDir := keylet.OwnerDir(fixture.ctx.AccountID)
	putFullOfferDirectory(t, fixture.view, ownerDir, false, func(dir *state.DirectoryNode) {
		dir.Owner = fixture.ctx.AccountID
	})
	result, applyMain := fixture.place(t, false)

	require.Equal(t, ter.TecDIR_FULL, result)
	require.True(t, applyMain)
	require.Zero(t, fixture.ctx.Account.OwnerCount)
}

func TestOfferCreateBookDirectoryFull(t *testing.T) {
	fixture := newDirectoryFullOfferFixture(t, nil)
	bookBase, err := offerBookBase(fixture.takerPays, fixture.takerGets, nil)
	require.NoError(t, err)
	bookDir := keylet.Quality(bookBase, fixture.rate)
	putFullOfferDirectory(t, fixture.view, bookDir, true, func(dir *state.DirectoryNode) {
		require.NoError(t, setBookDirectoryAssets(dir, fixture.takerPays, fixture.takerGets))
		dir.ExchangeRate = fixture.rate
	})
	result, applyMain := fixture.place(t, false)

	require.Equal(t, ter.TecDIR_FULL, result)
	require.True(t, applyMain)
}

func TestOfferCreateHybridOpenBookDirectoryFull(t *testing.T) {
	domainID := [32]byte{3}
	fixture := newDirectoryFullOfferFixture(t, &domainID)
	openBookBase, err := offerBookBase(fixture.takerPays, fixture.takerGets, nil)
	require.NoError(t, err)
	openBookDir := keylet.Quality(openBookBase, fixture.rate)
	putFullOfferDirectory(t, fixture.view, openBookDir, true, func(dir *state.DirectoryNode) {
		require.NoError(t, setBookDirectoryAssets(dir, fixture.takerPays, fixture.takerGets))
		dir.ExchangeRate = fixture.rate
	})
	result, applyMain := fixture.place(t, true)

	require.Equal(t, ter.TecDIR_FULL, result)
	require.True(t, applyMain)
}

func TestMapOfferDirInsertError(t *testing.T) {
	require.Equal(t, ter.TecDIR_FULL, mapOfferDirInsertError(state.ErrDirFull))
	require.Equal(t, ter.TecDIR_FULL, mapOfferDirInsertError(errors.Join(errors.New("wrapped"), state.ErrDirFull)))
	require.Equal(t, ter.TefINTERNAL, mapOfferDirInsertError(errors.New("storage failure")))
}
