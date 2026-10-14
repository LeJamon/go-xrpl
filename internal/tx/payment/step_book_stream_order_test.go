package payment

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// Ledger 21481628 exposed a distinction between in-band BookTip deletion and
// sorted permanent cleanup: offer-key order can reverse adjacent owner pages.
// A committed strand must retain the former's deletion-time link metadata;
// a discarded strand must still use the latter's canonical cleanup order.
func TestBookStepSelfCrossDeletionFollowsStreamOrder(t *testing.T) {
	for _, direction := range []string{"reverse", "forward"} {
		for _, reset := range []bool{false, true} {
			for _, commit := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/reset=%t/commit=%t", direction, reset, commit), func(t *testing.T) {
					actual, root := streamOrderMetadata(t, direction, reset, commit, false)
					expected, _ := streamOrderMetadata(t, direction, false, commit, true)
					actualBytes, err := tx.SerializeMetadata(actual)
					require.NoError(t, err)
					expectedBytes, err := tx.SerializeMetadata(expected)
					require.NoError(t, err)
					require.Equal(t, expectedBytes, actualBytes, "deleted directory metadata must preserve the selected sandbox's order")
					var deletedPages int
					for _, node := range actual.AffectedNodes {
						if node.NodeType != "DeletedNode" || node.LedgerEntryType != "DirectoryNode" {
							continue
						}
						for page := uint64(1); page <= 2; page++ {
							if node.LedgerIndex == fmt.Sprintf("%X", keylet.DirPage(root, page).Key) {
								deletedPages++
							}
						}
					}
					require.Equal(t, 2, deletedPages)
				})
			}
		}
	}
}

func streamOrderMetadata(t *testing.T, direction string, reset, commit, oracle bool) (*tx.Metadata, [32]byte) {
	t.Helper()
	view := newPaymentMockLedgerView()
	owner := [20]byte{1}
	view.createAccount(owner, 1_000_000_000, 2)
	address := state.EncodeAccountIDSafe(owner)
	step := NewBookStep(Issue{Currency: "XRP"}, Issue{Currency: "USD", Issuer: owner}, owner, owner, nil, false)
	step.defaultPath = true
	step.qualityLimit = &Quality{Value: ^uint64(0)}
	sequences := []uint32{1, 2}
	keys := [][32]byte{keylet.Offer(owner, 1).Key, keylet.Offer(owner, 2).Key}
	if bytes.Compare(keys[0][:], keys[1][:]) < 0 {
		keys[0], keys[1] = keys[1], keys[0]
		sequences[0], sequences[1] = sequences[1], sequences[0]
	}
	root := keylet.OwnerDir(owner).Key
	for page := uint64(0); page < 4; page++ {
		dir := &state.DirectoryNode{RootIndex: root, Owner: owner, Indexes: [][32]byte{{0xff, byte(page)}}}
		dir.SetIndexPrevious((page + 3) % 4)
		dir.SetIndexNext((page + 1) % 4)
		if page == 1 || page == 2 {
			dir.Indexes = [][32]byte{keys[page-1]}
		}
		data, err := state.SerializeDirectoryNode(dir, false)
		require.NoError(t, err)
		view.data[keylet.DirPage(root, page).Key] = data
	}
	for i, key := range keys {
		book := step.bookBaseKey()
		binary.BigEndian.PutUint64(book[24:], uint64(0x5500000000000000+i))
		dir := &state.DirectoryNode{RootIndex: book, Indexes: [][32]byte{key}, TakerGetsCurrency: keylet.CurrencyBytes("USD"), TakerGetsIssuer: owner}
		data, err := state.SerializeDirectoryNode(dir, true)
		require.NoError(t, err)
		view.data[book] = data
		offer := &state.LedgerOffer{Account: address, Sequence: sequences[i], OwnerNode: uint64(i + 1), BookDirectory: book,
			TakerPays: tx.NewXRPAmount(100_000_000), TakerGets: tx.NewIssuedAmountFromFloat64(10, "USD", address)}
		view.data[key], err = state.SerializeLedgerOffer(offer)
		require.NoError(t, err)
	}
	base := NewPaymentSandbox(view)
	table := applystate.NewApplyStateTable(base, [32]byte{9}, 21481628, view.Rules())
	accum := NewPaymentSandbox(table)
	removals := make(map[[32]byte]bool)
	walk := func() *PaymentSandbox {
		sb := NewChildSandbox(accum)
		if oracle {
			for _, key := range keys {
				removals[key] = true
				require.NoError(t, DeleteOfferAtomically(sb, key))
			}
		} else if direction == "reverse" {
			step.Rev(sb, NewChildSandbox(accum), removals, NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(1, "USD", address)))
		} else {
			step.Fwd(sb, NewChildSandbox(accum), removals, NewXRPEitherAmount(1))
		}
		return sb
	}
	if reset {
		walk()
	}
	sb := walk()
	require.Len(t, removals, 2)
	for _, key := range keys {
		data, err := accum.Read(keylet.Keylet{Key: key})
		require.NoError(t, err)
		require.NotNil(t, data, "working-view erasure must not escape an unapplied sandbox")
	}
	if commit {
		require.NoError(t, sb.Apply(accum))
	}
	for _, key := range state.SortedLedgerKeys(removals) {
		require.NoError(t, DeleteOfferAtomically(accum, key))
	}
	require.NoError(t, accum.ApplyToView(table))
	meta, err := table.Preview()
	require.NoError(t, err)
	return meta, root
}
