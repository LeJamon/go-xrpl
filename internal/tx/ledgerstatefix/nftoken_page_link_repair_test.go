package ledgerstatefix

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

var errNFTokenPageRepairInjected = errors.New("injected NFToken page repair failure")

type nftPageRepairFaults struct {
	succAt      int
	succCalls   int
	readErrors  map[[32]byte]error
	readData    map[[32]byte][]byte
	updateAt    int
	updateCalls int
	eraseAt     int
	eraseCalls  int
	insertAt    int
	insertCalls int
}

type nftPageRepairView struct {
	data   map[[32]byte][]byte
	faults *nftPageRepairFaults
}

func newNFTokenPageRepairView() *nftPageRepairView {
	return &nftPageRepairView{
		data: make(map[[32]byte][]byte),
		faults: &nftPageRepairFaults{
			readErrors: make(map[[32]byte]error),
			readData:   make(map[[32]byte][]byte),
		},
	}
}

func (v *nftPageRepairView) Read(k keylet.Keylet) ([]byte, error) {
	if err := v.faults.readErrors[k.Key]; err != nil {
		return nil, err
	}
	if data, ok := v.faults.readData[k.Key]; ok {
		return bytes.Clone(data), nil
	}
	return bytes.Clone(v.data[k.Key]), nil
}

func (v *nftPageRepairView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}

func (v *nftPageRepairView) Insert(k keylet.Keylet, data []byte) error {
	v.faults.insertCalls++
	if v.faults.insertAt == v.faults.insertCalls {
		return errNFTokenPageRepairInjected
	}
	if _, exists := v.data[k.Key]; exists {
		return errors.New("entry already exists")
	}
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *nftPageRepairView) Update(k keylet.Keylet, data []byte) error {
	v.faults.updateCalls++
	if v.faults.updateAt == v.faults.updateCalls {
		return errNFTokenPageRepairInjected
	}
	if _, exists := v.data[k.Key]; !exists {
		return errors.New("entry not found")
	}
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *nftPageRepairView) Erase(k keylet.Keylet) error {
	v.faults.eraseCalls++
	if v.faults.eraseAt == v.faults.eraseCalls {
		return errNFTokenPageRepairInjected
	}
	if _, exists := v.data[k.Key]; !exists {
		return errors.New("entry not found")
	}
	delete(v.data, k.Key)
	return nil
}

func (v *nftPageRepairView) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	staged := &nftPageRepairView{data: cloneNFTokenPageData(v.data), faults: v.faults}
	if err := apply(staged); err != nil {
		return err
	}
	v.data = staged.data
	return nil
}

func (*nftPageRepairView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*nftPageRepairView) TxExists([32]byte) (bool, error)            { return false, nil }
func (*nftPageRepairView) Rules() *amendment.Rules                    { return amendment.AllSupportedRules() }
func (*nftPageRepairView) LedgerSeq() uint32                          { return 1 }

func (v *nftPageRepairView) ForEach(fn func([32]byte, []byte) bool) error {
	for key, data := range v.data {
		if !fn(key, bytes.Clone(data)) {
			break
		}
	}
	return nil
}

func (v *nftPageRepairView) Succ(after [32]byte) ([32]byte, []byte, bool, error) {
	v.faults.succCalls++
	if v.faults.succAt == v.faults.succCalls {
		return [32]byte{}, nil, false, errNFTokenPageRepairInjected
	}
	keys := make([][32]byte, 0, len(v.data))
	for key := range v.data {
		if bytes.Compare(key[:], after[:]) > 0 {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return [32]byte{}, nil, false, nil
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	return keys[0], bytes.Clone(v.data[keys[0]]), true, nil
}

func cloneNFTokenPageData(data map[[32]byte][]byte) map[[32]byte][]byte {
	cloned := make(map[[32]byte][]byte, len(data))
	for key, value := range data {
		cloned[key] = bytes.Clone(value)
	}
	return cloned
}

func nftPageRepairOwner() [20]byte {
	return [20]byte{1, 2, 3}
}

func nftPageRepairKey(owner [20]byte, suffix byte) keylet.Keylet {
	page := keylet.NFTokenPageMin(owner)
	page.Key[31] = suffix
	return page
}

func putNFTokenPage(t *testing.T, view *nftPageRepairView, page keylet.Keylet, data *state.NFTokenPageData) {
	t.Helper()
	serialized, err := nftoken.SerializeNFTokenPage(data)
	require.NoError(t, err)
	view.data[page.Key] = serialized
}

func nftPageRepairContext(view tx.LedgerView) *tx.ApplyContext {
	return &tx.ApplyContext{View: view, Log: xrpllog.Discard()}
}

func assertNFTokenPageRepairUnchanged(t *testing.T, before map[[32]byte][]byte, view *nftPageRepairView) {
	t.Helper()
	require.Equal(t, before, view.data)
}

func failNFTokenPageSerializationAt(call int) nftPageSerializer {
	calls := 0
	return func(page *state.NFTokenPageData) ([]byte, error) {
		calls++
		if calls == call {
			return nil, errNFTokenPageRepairInjected
		}
		return nftoken.SerializeNFTokenPage(page)
	}
}

func TestLedgerStateFixNFTokenPageRepairOutcomes(t *testing.T) {
	owner := nftPageRepairOwner()
	ownerAddress, err := state.EncodeAccountID(owner)
	require.NoError(t, err)

	t.Run("no repair", func(t *testing.T) {
		view := newNFTokenPageRepairView()
		txn := NewNFTokenPageLinkFix("rAdmin", ownerAddress)
		require.Equal(t, ter.TecFAILED_PROCESSING, txn.Apply(nftPageRepairContext(view)))
	})

	t.Run("internal failure", func(t *testing.T) {
		view := newNFTokenPageRepairView()
		view.faults.succAt = 1
		txn := NewNFTokenPageLinkFix("rAdmin", ownerAddress)
		require.Equal(t, ter.TefEXCEPTION, txn.Apply(nftPageRepairContext(view)))
	})

	t.Run("repair", func(t *testing.T) {
		view := newNFTokenPageRepairView()
		last := keylet.NFTokenPageMax(owner)
		putNFTokenPage(t, view, last, &state.NFTokenPageData{NextPageMin: nftPageRepairKey(owner, 1).Key})
		txn := NewNFTokenPageLinkFix("rAdmin", ownerAddress)
		require.Equal(t, ter.TesSUCCESS, txn.Apply(nftPageRepairContext(view)))
		page, parseErr := state.ParseNFTokenPage(view.data[last.Key])
		require.NoError(t, parseErr)
		require.Zero(t, page.NextPageMin)
		require.Zero(t, page.PreviousPageMin)
	})
}

func TestRepairNFTokenDirectoryLinksTraversalFailuresAreAtomic(t *testing.T) {
	owner := nftPageRepairOwner()
	first := nftPageRepairKey(owner, 1)
	second := nftPageRepairKey(owner, 2)

	tests := []struct {
		name  string
		setup func(*testing.T, *nftPageRepairView)
	}{
		{
			name: "initial successor",
			setup: func(_ *testing.T, view *nftPageRepairView) {
				view.faults.succAt = 1
			},
		},
		{
			name: "loop successor",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{PreviousPageMin: second.Key})
				view.faults.succAt = 2
			},
		},
		{
			name: "initial parse",
			setup: func(_ *testing.T, view *nftPageRepairView) {
				view.data[first.Key] = []byte{0xff}
			},
		},
		{
			name: "loop parse",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{PreviousPageMin: second.Key})
				view.data[second.Key] = []byte{0xff}
			},
		},
		{
			name: "relocation previous read",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{NextPageMin: second.Key})
				putNFTokenPage(t, view, second, &state.NFTokenPageData{PreviousPageMin: first.Key})
				view.faults.readErrors[first.Key] = errNFTokenPageRepairInjected
			},
		},
		{
			name: "relocation previous parse",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{NextPageMin: second.Key})
				putNFTokenPage(t, view, second, &state.NFTokenPageData{PreviousPageMin: first.Key})
				view.faults.readData[first.Key] = []byte{0xff}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := newNFTokenPageRepairView()
			test.setup(t, view)
			before := cloneNFTokenPageData(view.data)

			repaired, err := repairNFTokenDirectoryLinks(nftPageRepairContext(view), owner)

			require.False(t, repaired)
			require.Error(t, err)
			assertNFTokenPageRepairUnchanged(t, before, view)
		})
	}
}

func TestRepairNFTokenDirectoryLinksSerializationFailuresAreAtomic(t *testing.T) {
	owner := nftPageRepairOwner()
	first := nftPageRepairKey(owner, 1)
	second := nftPageRepairKey(owner, 2)
	last := keylet.NFTokenPageMax(owner)

	tests := []struct {
		name   string
		failAt int
		setup  func(*testing.T, *nftPageRepairView)
	}{
		{
			name:   "single page",
			failAt: 1,
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, last, &state.NFTokenPageData{NextPageMin: first.Key})
			},
		},
		{
			name:   "multi page after planned update",
			failAt: 2,
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{PreviousPageMin: second.Key})
				putNFTokenPage(t, view, last, &state.NFTokenPageData{})
			},
		},
		{
			name:   "final link",
			failAt: 1,
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{NextPageMin: last.Key})
				putNFTokenPage(t, view, last, &state.NFTokenPageData{
					PreviousPageMin: first.Key,
					NextPageMin:     second.Key,
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := newNFTokenPageRepairView()
			test.setup(t, view)
			before := cloneNFTokenPageData(view.data)

			repaired, err := repairNFTokenDirectoryLinksWithSerializer(
				nftPageRepairContext(view), owner, failNFTokenPageSerializationAt(test.failAt),
			)

			require.False(t, repaired)
			require.ErrorIs(t, err, errNFTokenPageRepairInjected)
			assertNFTokenPageRepairUnchanged(t, before, view)
		})
	}
}

func TestRepairNFTokenDirectoryLinksWriteFailuresAreAtomic(t *testing.T) {
	owner := nftPageRepairOwner()
	first := nftPageRepairKey(owner, 1)
	second := nftPageRepairKey(owner, 2)
	last := keylet.NFTokenPageMax(owner)

	tests := []struct {
		name  string
		setup func(*testing.T, *nftPageRepairView)
	}{
		{
			name: "single page update",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, last, &state.NFTokenPageData{NextPageMin: first.Key})
				view.faults.updateAt = 1
			},
		},
		{
			name: "second multi page update",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{})
				putNFTokenPage(t, view, last, &state.NFTokenPageData{})
				view.faults.updateAt = 2
			},
		},
		{
			name: "relocation previous update",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{NextPageMin: second.Key})
				putNFTokenPage(t, view, second, &state.NFTokenPageData{PreviousPageMin: first.Key})
				view.faults.updateAt = 1
			},
		},
		{
			name: "relocation erase",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{NextPageMin: second.Key})
				putNFTokenPage(t, view, second, &state.NFTokenPageData{PreviousPageMin: first.Key})
				view.faults.eraseAt = 1
			},
		},
		{
			name: "relocation insert",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{NextPageMin: second.Key})
				putNFTokenPage(t, view, second, &state.NFTokenPageData{PreviousPageMin: first.Key})
				view.faults.insertAt = 1
			},
		},
		{
			name: "final link update",
			setup: func(t *testing.T, view *nftPageRepairView) {
				putNFTokenPage(t, view, first, &state.NFTokenPageData{NextPageMin: last.Key})
				putNFTokenPage(t, view, last, &state.NFTokenPageData{
					PreviousPageMin: first.Key,
					NextPageMin:     second.Key,
				})
				view.faults.updateAt = 1
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := newNFTokenPageRepairView()
			test.setup(t, view)
			before := cloneNFTokenPageData(view.data)

			repaired, err := repairNFTokenDirectoryLinks(nftPageRepairContext(view), owner)

			require.False(t, repaired)
			require.ErrorIs(t, err, errNFTokenPageRepairInjected)
			assertNFTokenPageRepairUnchanged(t, before, view)
		})
	}
}
