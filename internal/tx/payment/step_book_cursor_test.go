package payment

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type bookCursorView struct {
	*paymentMockLedgerView
	keys                          [][32]byte
	directories                   map[[32]byte]bool
	offers                        map[[32]byte]bool
	successors, pages, offerReads int
}

func (v *bookCursorView) Read(k keylet.Keylet) ([]byte, error) {
	if v.directories[k.Key] {
		v.pages++
	}
	if v.offers[k.Key] {
		v.offerReads++
	}
	return v.paymentMockLedgerView.Read(k)
}

func (v *bookCursorView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	v.successors++
	i, found := slices.BinarySearchFunc(v.keys, key, func(a, b [32]byte) int {
		return bytes.Compare(a[:], b[:])
	})
	if found {
		i++
	}
	if i == len(v.keys) {
		return [32]byte{}, nil, false, nil
	}
	k := v.keys[i]
	if v.directories[k] {
		v.pages++
	}
	return k, v.data[k], true, nil
}

func newBookCursorFixture(t testing.TB, count, perQuality int) (*BookStep, *bookCursorView, [][32]byte) {
	t.Helper()
	owner, gateway := [20]byte{1}, [20]byte{2}
	view := &bookCursorView{
		paymentMockLedgerView: newPaymentMockLedgerView(),
		directories:           make(map[[32]byte]bool),
		offers:                make(map[[32]byte]bool),
	}
	view.createAccount(owner, 1_000_000_000_000, uint32(count))
	step := NewBookStep(Issue{Currency: "USD", Issuer: gateway}, Issue{Currency: "XRP"}, owner, owner, nil, false)
	step.defaultPath = true
	step.qualityLimit = &Quality{Value: ^uint64(0)}
	step.maxOffersToConsume = uint32(count + 1)
	keys := make([][32]byte, count)
	for i := range count {
		pays := tx.NewIssuedAmountFromFloat64(float64(i/perQuality+1), "USD", state.EncodeAccountIDSafe(gateway))
		gets := tx.NewXRPAmount(1_000_000)
		quality := QualityFromAmounts(ToEitherAmount(pays), ToEitherAmount(gets))
		directory := keylet.Quality(keylet.Keylet{Key: step.bookBaseKey()}, quality.Value)
		keys[i] = keylet.Offer(owner, uint32(i+1)).Key
		ownerNode, err := state.DirInsert(view.paymentMockLedgerView, keylet.OwnerDir(owner), keys[i], false, func(dir *state.DirectoryNode) { dir.Owner = owner })
		require.NoError(t, err)
		bookNode, err := state.DirInsert(view.paymentMockLedgerView, directory, keys[i], true, func(dir *state.DirectoryNode) {
			dir.TakerPaysCurrency = keylet.CurrencyBytes("USD")
			dir.TakerPaysIssuer = gateway
		})
		require.NoError(t, err)
		view.directories[keylet.DirPage(directory.Key, bookNode.Page).Key] = true
		offer := &state.LedgerOffer{
			Account: state.EncodeAccountIDSafe(owner), Sequence: uint32(i + 1),
			TakerPays: pays, TakerGets: gets, BookDirectory: directory.Key,
			BookNode: bookNode.Page, OwnerNode: ownerNode.Page,
		}
		data, err := state.SerializeLedgerOffer(offer)
		require.NoError(t, err)
		view.data[keys[i]] = data
		view.offers[keys[i]] = true
	}
	for key := range view.data {
		view.keys = append(view.keys, key)
	}
	slices.SortFunc(view.keys, func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })
	return step, view, keys
}

func TestBookOfferTraversalLinearReads(t *testing.T) {
	for _, perQuality := range []int{1, 32, 512} {
		t.Run(fmt.Sprint(perQuality), func(t *testing.T) {
			const count = 512
			step, view, keys := newBookCursorFixture(t, count, perQuality)
			removals := make(map[[32]byte]bool)
			for pass := range 2 {
				step.offersUsed = 0
				view.successors, view.pages, view.offerReads = 0, 0, 0
				sb := NewPaymentSandbox(view)
				cursor := bookOfferCursor{root: step.bookBaseKey()}
				visited := make(map[[32]byte]bool)
				for _, key := range keys {
					offer, got, err := step.getNextOfferSkipVisited(sb, sb, removals, visited, true, &cursor)
					require.NoError(t, err)
					require.NotNil(t, offer)
					require.Equal(t, key, got)
					removals[key] = true
				}
				offer, _, err := step.getNextOfferSkipVisited(sb, sb, removals, visited, true, &cursor)
				require.NoError(t, err)
				require.Nil(t, offer)
				require.Equal(t, uint32(count), step.offersUsed)
				require.Len(t, removals, count)
				for _, key := range keys {
					require.True(t, removals[key])
				}
				require.LessOrEqual(t, view.successors, count/perQuality+1)
				require.Equal(t, len(view.directories), view.pages)
				require.Equal(t, count*(pass+1), view.offerReads,
					"a prior pass's removal set may re-yield each own offer once")
			}
		})
	}
}

func TestBookOfferCursorDeletionAndRewind(t *testing.T) {
	for _, perQuality := range []int{1, 96} {
		t.Run(fmt.Sprint(perQuality), func(t *testing.T) {
			step, view, keys := newBookCursorFixture(t, 96, perQuality)
			afView := NewPaymentSandbox(view)
			for range 2 {
				step.offersUsed = 0
				sb := NewChildSandbox(afView)
				cursor := bookOfferCursor{root: step.bookBaseKey()}
				visited, removals := make(map[[32]byte]bool), make(map[[32]byte]bool)
				for _, key := range keys {
					offer, got, err := step.getNextOfferSkipVisited(sb, afView, removals, visited, true, &cursor)
					require.NoError(t, err)
					require.NotNil(t, offer)
					require.Equal(t, key, got)
					require.NoError(t, step.deleteOffer(sb, offer, [20]byte{1}))
				}
				offer, _, err := step.getNextOfferSkipVisited(sb, afView, removals, visited, true, &cursor)
				require.NoError(t, err)
				require.Nil(t, offer)
				require.Equal(t, uint32(len(keys)), step.offersUsed)
				for key := range view.directories {
					data, err := sb.Read(keylet.Keylet{Key: key})
					require.NoError(t, err)
					require.Nil(t, data, "all consumed book pages must be deleted")
				}
			}
		})
	}
}

func TestBookOfferCursorDeletedEmptyTrailingPage(t *testing.T) {
	step, view, keys := newBookCursorFixture(t, 65, 64)
	first, err := state.ParseLedgerOffer(view.data[keys[0]])
	require.NoError(t, err)
	rootKey := keylet.DirPage(first.BookDirectory, 0)
	root, err := state.ParseDirectoryNode(view.data[rootKey.Key])
	require.NoError(t, err)
	root.SetIndexPrevious(2)
	view.data[rootKey.Key], err = state.SerializeDirectoryNode(root, true)
	require.NoError(t, err)
	pageKey := keylet.DirPage(first.BookDirectory, 1)
	page, err := state.ParseDirectoryNode(view.data[pageKey.Key])
	require.NoError(t, err)
	page.SetIndexNext(2)
	view.data[pageKey.Key], err = state.SerializeDirectoryNode(page, true)
	require.NoError(t, err)
	emptyKey := keylet.DirPage(first.BookDirectory, 2)
	empty := &state.DirectoryNode{RootIndex: first.BookDirectory, IndexPrevious: 1}
	view.data[emptyKey.Key], err = state.SerializeDirectoryNode(empty, true)
	require.NoError(t, err)
	view.keys = append(view.keys, emptyKey.Key)
	slices.SortFunc(view.keys, func(a, b [32]byte) int { return bytes.Compare(a[:], b[:]) })

	sb := NewPaymentSandbox(view)
	cursor := bookOfferCursor{root: step.bookBaseKey()}
	visited, removals := make(map[[32]byte]bool), make(map[[32]byte]bool)
	for _, key := range keys {
		offer, got, err := step.getNextOfferSkipVisited(sb, sb, removals, visited, true, &cursor)
		require.NoError(t, err)
		require.NotNil(t, offer)
		require.Equal(t, key, got)
		require.NoError(t, step.deleteOffer(sb, offer, [20]byte{1}))
	}
	data, err := sb.Read(emptyKey)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, uint32(len(keys)), step.offersUsed)
}

func TestBookOfferCursorGroomsAcrossPages(t *testing.T) {
	step, view, keys := newBookCursorFixture(t, 96, 96)
	step.parentCloseTime = 100
	for i, key := range keys[:70] {
		switch i % 4 {
		case 0:
			delete(view.data, key)
		case 1, 2, 3:
			offer, err := state.ParseLedgerOffer(view.data[key])
			require.NoError(t, err)
			switch i % 4 {
			case 1:
				offer.Expiration = 99
			case 2:
				offer.TakerGets = tx.NewXRPAmount(0)
			case 3:
				offer.DomainID = [32]byte{1}
			}
			view.data[key], err = state.SerializeLedgerOffer(offer)
			require.NoError(t, err)
		}
	}
	// Legacy hybrid membership checks exercise domain grooming in an open book.
	view.rules = amendment.NewRules(nil)
	afView := NewPaymentSandbox(view)
	sb := NewChildSandbox(afView)
	visited, removals := make(map[[32]byte]bool), make(map[[32]byte]bool)
	cursor := bookOfferCursor{root: step.bookBaseKey()}
	for _, key := range keys[70:] {
		offer, got, err := step.getNextOfferSkipVisited(sb, afView, removals, visited, true, &cursor)
		require.NoError(t, err)
		require.NotNil(t, offer)
		require.Equal(t, key, got)
	}
	require.Equal(t, uint32(len(keys)), step.offersUsed)
	for i, key := range keys[:70] {
		require.True(t, visited[key])
		if i%4 != 0 {
			require.True(t, removals[key])
		}
	}
}

func TestBookOfferCursorReadsLiveFunding(t *testing.T) {
	step, view, keys := newBookCursorFixture(t, 96, 96)
	afView := NewPaymentSandbox(view)
	sb := NewChildSandbox(afView)
	visited, removals := make(map[[32]byte]bool), make(map[[32]byte]bool)
	cursor := bookOfferCursor{root: step.bookBaseKey()}
	offer, got, err := step.getNextOfferSkipVisited(sb, afView, removals, visited, true, &cursor)
	require.NoError(t, err)
	require.NotNil(t, offer)
	require.Equal(t, keys[0], got)
	ownerKey := keylet.Account([20]byte{1})
	owner, err := state.ParseAccountRoot(view.data[ownerKey.Key])
	require.NoError(t, err)
	owner.Balance = 0
	data, err := state.SerializeAccountRoot(owner)
	require.NoError(t, err)
	require.NoError(t, sb.Update(ownerKey, data))
	offer, _, err = step.getNextOfferSkipVisited(sb, afView, removals, visited, true, &cursor)
	require.NoError(t, err)
	require.Nil(t, offer)
	require.Empty(t, removals, "became-unfunded removals must stay in the execution sandbox")
	require.Equal(t, uint32(len(keys)), step.offersUsed)
	for _, key := range keys[1:] {
		data, err := sb.Read(keylet.Keylet{Key: key})
		require.NoError(t, err)
		require.Nil(t, data)
	}
}

func TestBookOfferCursorSkipsVisitedBeforeMarkedRead(t *testing.T) {
	step, view, keys := newBookCursorFixture(t, 96, 96)
	sb := NewPaymentSandbox(view)
	visited, removals := make(map[[32]byte]bool), make(map[[32]byte]bool)
	for _, key := range keys[:95] {
		visited[key], removals[key] = true, true
	}
	cursor := bookOfferCursor{root: step.bookBaseKey()}
	offer, got, err := step.getNextOfferSkipVisited(sb, sb, removals, visited, true, &cursor)
	require.NoError(t, err)
	require.NotNil(t, offer)
	require.Equal(t, keys[95], got)
	require.Equal(t, 1, view.offerReads)
	require.Equal(t, uint32(1), step.offersUsed)
}

func BenchmarkBookOfferTraversal(b *testing.B) {
	for _, count := range []int{32, 128, 512} {
		for _, scenario := range []string{"retained", "self-cross", "qualities"} {
			b.Run(fmt.Sprintf("%s/%d", scenario, count), func(b *testing.B) {
				perQuality := count
				if scenario == "qualities" {
					perQuality = 1
				}
				step, view, _ := newBookCursorFixture(b, count, perQuality)
				if scenario == "retained" {
					step.defaultPath = false
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					step.offersUsed = 0
					sb := NewPaymentSandbox(view)
					step.forEachOffer(sb, sb, make(map[[32]byte]bool), QualityOne, QualityOne,
						func() bool { return false }, func(offerExec) bool { return true })
					if step.offersUsed != uint32(count) {
						b.Fatalf("walked %d offers, want %d", step.offersUsed, count)
					}
				}
				b.ReportMetric(float64(view.successors)/float64(b.N), "succ/op")
				b.ReportMetric(float64(view.pages)/float64(b.N), "directory-loads/op")
				b.ReportMetric(float64(view.offerReads)/float64(b.N), "offer-reads/op")
				b.ReportMetric(float64(count), "offers/op")
			})
		}
	}
}
