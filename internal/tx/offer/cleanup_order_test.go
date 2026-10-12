package offer

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type cleanupDeletionView struct {
	*offerMPTLedgerView
	deleted map[[32]byte][]byte
}

func (v *cleanupDeletionView) Erase(k keylet.Keylet) error {
	v.deleted[k.Key] = append([]byte(nil), v.data[k.Key]...)
	return v.offerMPTLedgerView.Erase(k)
}

// Ledger 21457985: two adjacent owner pages were erased in reverse order.
// Both surviving states matched, but DeletedNode link fields did not. Exercise
// repeated map iteration and both the crossing and cancel cleanup paths.
func TestRemovableOfferCleanupPreservesCanonicalDeletedPageLinks(t *testing.T) {
	for range 64 {
		view := newOfferMPTLedgerView()
		owner := [20]byte{1}
		putOfferCleanupAccount(t, view, owner, tx.OwnerCounts{Owner: 2})
		root := keylet.OwnerDir(owner).Key
		keys := [][32]byte{{1}, {2}}
		for page := uint64(0); page < 4; page++ {
			dir := &state.DirectoryNode{RootIndex: root, Owner: owner}
			dir.SetIndexPrevious((page + 3) % 4)
			dir.SetIndexNext((page + 1) % 4)
			dir.Indexes = [][32]byte{{0xff, byte(page)}}
			if page == 1 || page == 2 {
				dir.Indexes = [][32]byte{keys[page-1]}
			}
			data, err := state.SerializeDirectoryNode(dir, false)
			require.NoError(t, err)
			view.data[keylet.DirPage(root, page).Key] = data
		}
		for i, k := range keys {
			book := [32]byte{0x80, byte(i)}
			dir := &state.DirectoryNode{RootIndex: book, Indexes: [][32]byte{k}, TakerPaysCurrency: keylet.CurrencyBytes("USD"), TakerPaysIssuer: owner}
			data, err := state.SerializeDirectoryNode(dir, true)
			require.NoError(t, err)
			view.data[book] = data
			o := &state.LedgerOffer{Account: state.EncodeAccountIDSafe(owner), Sequence: uint32(i + 1), OwnerNode: uint64(i + 1), BookDirectory: book,
				TakerPays: tx.NewIssuedAmountFromFloat64(1, "USD", state.EncodeAccountIDSafe(owner)), TakerGets: tx.NewXRPAmount(1)}
			view.data[k], err = state.SerializeLedgerOffer(o)
			require.NoError(t, err)
		}
		sb, cancel := payment.NewPaymentSandbox(view), payment.NewPaymentSandbox(view)
		removable := map[[32]byte]bool{keys[1]: true, keys[0]: true}
		require.NoError(t, removeRemovableOffers(sb, cancel, removable, removable))
		for _, sandbox := range []*payment.PaymentSandbox{sb, cancel} {
			copyView := newOfferMPTLedgerView()
			for k, v := range view.data {
				copyView.data[k] = v
			}
			recorder := &cleanupDeletionView{copyView, make(map[[32]byte][]byte)}
			require.NoError(t, sandbox.ApplyToView(recorder))
			first, err := state.ParseDirectoryNode(recorder.deleted[keylet.DirPage(root, 1).Key])
			require.NoError(t, err)
			second, err := state.ParseDirectoryNode(recorder.deleted[keylet.DirPage(root, 2).Key])
			require.NoError(t, err)
			require.Equal(t, uint64(2), first.IndexNext, "page 1 must be erased first")
			require.Zero(t, second.IndexPrevious, "page 2 must record page 1's unlink")
		}
	}
}
