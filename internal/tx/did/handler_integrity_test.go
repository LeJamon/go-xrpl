package did

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type didFaultView struct {
	data         map[[32]byte][]byte
	readErrors   map[[32]byte]error
	updateErrors map[[32]byte]error
	rules        *amendment.Rules
}

func newDIDFaultView() *didFaultView {
	return &didFaultView{
		data:         make(map[[32]byte][]byte),
		readErrors:   make(map[[32]byte]error),
		updateErrors: make(map[[32]byte]error),
		rules:        amendment.AllSupportedRules(),
	}
}

func (v *didFaultView) Read(k keylet.Keylet) ([]byte, error) {
	if err := v.readErrors[k.Key]; err != nil {
		return nil, err
	}
	return bytes.Clone(v.data[k.Key]), nil
}

func (v *didFaultView) Exists(k keylet.Keylet) (bool, error) {
	if err := v.readErrors[k.Key]; err != nil {
		return false, err
	}
	_, ok := v.data[k.Key]
	return ok, nil
}

func (v *didFaultView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *didFaultView) Update(k keylet.Keylet, data []byte) error {
	if err := v.updateErrors[k.Key]; err != nil {
		return err
	}
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *didFaultView) Erase(k keylet.Keylet) error {
	delete(v.data, k.Key)
	return nil
}

func (*didFaultView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*didFaultView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *didFaultView) Rules() *amendment.Rules                  { return v.rules }
func (*didFaultView) LedgerSeq() uint32                          { return 1 }

func (v *didFaultView) ForEach(fn func([32]byte, []byte) bool) error {
	for key, data := range v.data {
		if !fn(key, bytes.Clone(data)) {
			break
		}
	}
	return nil
}

func (v *didFaultView) Succ(after [32]byte) ([32]byte, []byte, bool, error) {
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

func TestDIDHandlersDistinguishReadFailureFromAbsence(t *testing.T) {
	accountID := [20]byte{1}
	account, err := state.EncodeAccountID(accountID)
	require.NoError(t, err)
	didKey := keylet.DID(accountID)

	view := newDIDFaultView()
	view.readErrors[didKey.Key] = errors.New("storage read failed")
	ctx := didApplyContext(view, accountID, 1)

	set := NewDIDSet(account)
	set.URI = "4142"
	require.Equal(t, ter.TefINTERNAL, set.Apply(ctx))
	require.Equal(t, uint32(1), ctx.Account.OwnerCount)

	deleteTx := NewDIDDelete(account)
	require.Equal(t, ter.TefINTERNAL, deleteTx.Apply(ctx))
	require.Equal(t, uint32(1), ctx.Account.OwnerCount)

	delete(view.readErrors, didKey.Key)
	require.Equal(t, ter.TecNO_ENTRY, deleteTx.Apply(ctx))
}

func TestDIDSetDirectoryFailuresAreClassifiedAndAtomic(t *testing.T) {
	accountID := [20]byte{2}
	account, err := state.EncodeAccountID(accountID)
	require.NoError(t, err)
	didKey := keylet.DID(accountID)
	dirKey := keylet.OwnerDir(accountID)

	for _, tc := range []struct {
		name       string
		fault      error
		wantResult ter.Result
	}{
		{name: "capacity", fault: state.ErrDirFull, wantResult: ter.TecDIR_FULL},
		{name: "storage", fault: errors.New("storage read failed"), wantResult: ter.TefINTERNAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := newDIDFaultView()
			view.readErrors[dirKey.Key] = tc.fault
			ctx := didApplyContext(view, accountID, 0)
			set := NewDIDSet(account)
			set.URI = "4142"

			require.Equal(t, tc.wantResult, set.Apply(ctx))
			require.NotContains(t, view.data, didKey.Key)
			require.Equal(t, uint32(0), ctx.Account.OwnerCount)
		})
	}
}

func TestDIDDeleteDirectoryFailuresAreAtomic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(t *testing.T, fixture *didDeleteFixture)
	}{
		{
			name: "missing page",
			fault: func(t *testing.T, fixture *didDeleteFixture) {
				fixture.replaceDID(t, 1)
			},
		},
		{
			name: "missing index",
			fault: func(t *testing.T, fixture *didDeleteFixture) {
				delete(fixture.view.data, fixture.dirKey.Key)
				var unrelated [32]byte
				unrelated[0] = 1
				_, err := state.DirInsert(fixture.view, fixture.dirKey, unrelated, false, func(dir *state.DirectoryNode) {
					dir.Owner = fixture.accountID
				})
				require.NoError(t, err)
			},
		},
		{
			name: "storage read failure",
			fault: func(_ *testing.T, fixture *didDeleteFixture) {
				fixture.view.readErrors[fixture.dirKey.Key] = errors.New("storage read failed")
			},
		},
		{
			name: "storage update failure",
			fault: func(_ *testing.T, fixture *didDeleteFixture) {
				fixture.view.updateErrors[fixture.dirKey.Key] = errors.New("storage update failed")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDIDDeleteFixture(t)
			tc.fault(t, fixture)
			beforeDID := bytes.Clone(fixture.view.data[fixture.didKey.Key])
			beforeDir := bytes.Clone(fixture.view.data[fixture.dirKey.Key])

			result := NewDIDDelete(fixture.account).Apply(fixture.ctx)

			require.Equal(t, ter.TefBAD_LEDGER, result)
			require.Equal(t, beforeDID, fixture.view.data[fixture.didKey.Key])
			require.Equal(t, beforeDir, fixture.view.data[fixture.dirKey.Key])
			require.Equal(t, uint32(1), fixture.ctx.Account.OwnerCount)
		})
	}
}

type didDeleteFixture struct {
	view      *didFaultView
	account   string
	accountID [20]byte
	didKey    keylet.Keylet
	dirKey    keylet.Keylet
	ctx       *tx.ApplyContext
}

func newDIDDeleteFixture(t *testing.T) *didDeleteFixture {
	t.Helper()
	accountID := [20]byte{3}
	account, err := state.EncodeAccountID(accountID)
	require.NoError(t, err)
	view := newDIDFaultView()
	didKey := keylet.DID(accountID)
	dirKey := keylet.OwnerDir(accountID)
	dir, err := state.DirInsert(view, dirKey, didKey.Key, false, func(node *state.DirectoryNode) {
		node.Owner = accountID
	})
	require.NoError(t, err)

	fixture := &didDeleteFixture{
		view:      view,
		account:   account,
		accountID: accountID,
		didKey:    didKey,
		dirKey:    dirKey,
		ctx:       didApplyContext(view, accountID, 1),
	}
	fixture.replaceDID(t, dir.Page)
	return fixture
}

func (f *didDeleteFixture) replaceDID(t *testing.T, ownerNode uint64) {
	t.Helper()
	data, err := state.SerializeDID(&state.DIDData{
		Account:   f.accountID,
		OwnerNode: ownerNode,
		URI:       "4142",
	}, f.account)
	require.NoError(t, err)
	require.NoError(t, f.view.Insert(f.didKey, data))
}

func didApplyContext(view tx.LedgerView, accountID [20]byte, ownerCount uint32) *tx.ApplyContext {
	return &tx.ApplyContext{
		View:      view,
		AccountID: accountID,
		Account: &state.AccountRoot{
			Balance:    1_000_000_000,
			OwnerCount: ownerCount,
		},
		Config: tx.EngineConfig{
			ReserveBase:      10_000_000,
			ReserveIncrement: 2_000_000,
			Rules:            amendment.AllSupportedRules(),
		},
		Log: xrpllog.Discard(),
	}
}
