package depositpreauth

import (
	"bytes"
	"errors"
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type faultView struct {
	data        map[[32]byte][]byte
	readErrors  map[[32]byte]error
	insertError map[[32]byte]error
	updateError map[[32]byte]error
	eraseError  map[[32]byte]error
	rules       *amendment.Rules
}

func newFaultView() *faultView {
	return &faultView{
		data:        make(map[[32]byte][]byte),
		readErrors:  make(map[[32]byte]error),
		insertError: make(map[[32]byte]error),
		updateError: make(map[[32]byte]error),
		eraseError:  make(map[[32]byte]error),
		rules:       amendment.AllSupportedRules(),
	}
}

func (v *faultView) Read(k keylet.Keylet) ([]byte, error) {
	if err := v.readErrors[k.Key]; err != nil {
		return nil, err
	}
	return bytes.Clone(v.data[k.Key]), nil
}

func (v *faultView) Exists(k keylet.Keylet) (bool, error) {
	if err := v.readErrors[k.Key]; err != nil {
		return false, err
	}
	_, ok := v.data[k.Key]
	return ok, nil
}

func (v *faultView) Insert(k keylet.Keylet, data []byte) error {
	if err := v.insertError[k.Key]; err != nil {
		return err
	}
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *faultView) Update(k keylet.Keylet, data []byte) error {
	if err := v.updateError[k.Key]; err != nil {
		return err
	}
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *faultView) Erase(k keylet.Keylet) error {
	if err := v.eraseError[k.Key]; err != nil {
		return err
	}
	delete(v.data, k.Key)
	return nil
}

func (v *faultView) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	staged := newFaultView()
	staged.rules = v.rules
	staged.readErrors = v.readErrors
	staged.insertError = v.insertError
	staged.updateError = v.updateError
	staged.eraseError = v.eraseError
	for key, data := range v.data {
		staged.data[key] = bytes.Clone(data)
	}
	if err := apply(staged); err != nil {
		return err
	}
	v.data = staged.data
	return nil
}

func (*faultView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*faultView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *faultView) Rules() *amendment.Rules                  { return v.rules }
func (*faultView) LedgerSeq() uint32                          { return 1 }

func (v *faultView) ForEach(fn func([32]byte, []byte) bool) error {
	for key, data := range v.data {
		if !fn(key, bytes.Clone(data)) {
			break
		}
	}
	return nil
}

func (v *faultView) Succ(after [32]byte) ([32]byte, []byte, bool, error) {
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

func TestAuthorizationDirectoryFailuresAreClassified(t *testing.T) {
	ownerID := [20]byte{1}
	owner, err := state.EncodeAccountID(ownerID)
	require.NoError(t, err)
	targetID := [20]byte{2}
	target, err := state.EncodeAccountID(targetID)
	require.NoError(t, err)

	for _, form := range []struct {
		name string
		make func() *DepositPreauth
	}{
		{
			name: "account",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.Authorize = target
				return txn
			},
		},
		{
			name: "credentials",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.AuthorizeCredentials = []CredentialWrapper{{Credential: CredentialSpec{
					Issuer: target, CredentialType: "61",
				}}}
				return txn
			},
		},
	} {
		for _, failure := range []struct {
			name string
			err  error
			want ter.Result
		}{
			{name: "capacity", err: state.ErrDirFull, want: ter.TecDIR_FULL},
			{name: "storage", err: errors.New("storage failure"), want: ter.TefINTERNAL},
		} {
			t.Run(form.name+"/"+failure.name, func(t *testing.T) {
				view := newFaultView()
				view.readErrors[keylet.OwnerDir(ownerID).Key] = failure.err
				ctx := applyContext(view, ownerID, 0)

				require.Equal(t, failure.want, form.make().Apply(ctx))
				require.Equal(t, uint32(0), ctx.Account.OwnerCount)
			})
		}
	}
}

func TestPreclaimReadFailuresDoNotClaimFee(t *testing.T) {
	ownerID := [20]byte{1}
	owner, err := state.EncodeAccountID(ownerID)
	require.NoError(t, err)
	targetID := [20]byte{2}
	target, err := state.EncodeAccountID(targetID)
	require.NoError(t, err)
	credentials := []CredentialWrapper{{Credential: CredentialSpec{
		Issuer: target, CredentialType: "61",
	}}}
	credentialPairs := toKeyletPairs(makeSorted(credentials))
	accountPreauthKey := keylet.DepositPreauth(ownerID, targetID)
	credentialPreauthKey := keylet.DepositPreauthCredentials(ownerID, credentialPairs)
	targetAccountKey := keylet.Account(targetID)

	for _, tc := range []struct {
		name    string
		make    func() *DepositPreauth
		readKey keylet.Keylet
		setup   func(*faultView)
	}{
		{
			name: "authorize target",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.Authorize = target
				return txn
			},
			readKey: targetAccountKey,
		},
		{
			name: "authorize entry",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.Authorize = target
				return txn
			},
			readKey: accountPreauthKey,
			setup: func(view *faultView) {
				view.data[targetAccountKey.Key] = []byte{1}
			},
		},
		{
			name: "unauthorize entry",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.Unauthorize = target
				return txn
			},
			readKey: accountPreauthKey,
		},
		{
			name: "authorize credentials issuer",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.AuthorizeCredentials = credentials
				return txn
			},
			readKey: targetAccountKey,
		},
		{
			name: "authorize credentials entry",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.AuthorizeCredentials = credentials
				return txn
			},
			readKey: credentialPreauthKey,
			setup: func(view *faultView) {
				view.data[targetAccountKey.Key] = []byte{1}
			},
		},
		{
			name: "unauthorize credentials entry",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.UnauthorizeCredentials = credentials
				return txn
			},
			readKey: credentialPreauthKey,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := newFaultView()
			accountData, err := state.SerializeAccountRoot(&state.AccountRoot{
				Account: owner, Balance: 1_000_000_000, Sequence: 1,
			})
			require.NoError(t, err)
			view.data[keylet.Account(ownerID).Key] = accountData
			if tc.setup != nil {
				tc.setup(view)
			}
			before := make(map[[32]byte][]byte, len(view.data))
			for key, data := range view.data {
				before[key] = bytes.Clone(data)
			}
			view.readErrors[tc.readKey.Key] = errors.New("storage failure")

			txn := tc.make()
			txn.Fee = "10"
			txn.SetSequence(1)
			result := engine.NewEngine(view, tx.EngineConfig{
				BaseFee:                   10,
				LedgerSequence:            1,
				ReserveBase:               10_000_000,
				ReserveIncrement:          2_000_000,
				Rules:                     amendment.AllSupportedRules(),
				SkipSignatureVerification: true,
			}).Apply(txn)

			require.Equal(t, ter.TefEXCEPTION, result.Result)
			require.False(t, result.Applied)
			require.Zero(t, result.Fee)
			require.Equal(t, before, view.data)
		})
	}
}

func TestAuthorizationConfinesOwnerCountOverflow(t *testing.T) {
	ownerID := [20]byte{1}
	owner, err := state.EncodeAccountID(ownerID)
	require.NoError(t, err)
	targetID := [20]byte{2}
	target, err := state.EncodeAccountID(targetID)
	require.NoError(t, err)

	for _, form := range []struct {
		name string
		make func() *DepositPreauth
	}{
		{
			name: "account",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.Authorize = target
				return txn
			},
		},
		{
			name: "credentials",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.AuthorizeCredentials = []CredentialWrapper{{Credential: CredentialSpec{
					Issuer: target, CredentialType: "61",
				}}}
				return txn
			},
		},
	} {
		t.Run(form.name, func(t *testing.T) {
			view := newFaultView()
			ctx := applyContext(view, ownerID, math.MaxUint32)

			require.Equal(t, ter.TesSUCCESS, form.make().Apply(ctx))
			require.Equal(t, uint32(math.MaxUint32), ctx.Account.OwnerCount)
		})
	}
}

func TestAuthorizationCommitFailureRollsBackAllLedgerChanges(t *testing.T) {
	ownerID := [20]byte{1}
	owner, err := state.EncodeAccountID(ownerID)
	require.NoError(t, err)
	targetID := [20]byte{2}
	target, err := state.EncodeAccountID(targetID)
	require.NoError(t, err)
	credentials := []CredentialWrapper{{Credential: CredentialSpec{
		Issuer: target, CredentialType: "61",
	}}}

	for _, form := range []struct {
		name       string
		make       func() *DepositPreauth
		preauthKey keylet.Keylet
	}{
		{
			name: "account",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.Authorize = target
				return txn
			},
			preauthKey: keylet.DepositPreauth(ownerID, targetID),
		},
		{
			name: "credentials",
			make: func() *DepositPreauth {
				txn := NewDepositPreauth(owner)
				txn.AuthorizeCredentials = credentials
				return txn
			},
			preauthKey: keylet.DepositPreauthCredentials(ownerID, toKeyletPairs(makeSorted(credentials))),
		},
	} {
		t.Run(form.name, func(t *testing.T) {
			view := newFaultView()
			accountData, err := state.SerializeAccountRoot(&state.AccountRoot{
				Account: owner, Balance: 1_000_000_000, Sequence: 1,
			})
			require.NoError(t, err)
			view.data[keylet.Account(ownerID).Key] = accountData
			view.data[keylet.Account(targetID).Key] = []byte{1}
			before := make(map[[32]byte][]byte, len(view.data))
			for key, data := range view.data {
				before[key] = bytes.Clone(data)
			}
			view.insertError[form.preauthKey.Key] = errors.New("storage failure")

			txn := form.make()
			txn.Fee = "10"
			txn.SetSequence(1)
			result := engine.NewEngine(view, tx.EngineConfig{
				BaseFee:                   10,
				LedgerSequence:            1,
				ReserveBase:               10_000_000,
				ReserveIncrement:          2_000_000,
				Rules:                     amendment.AllSupportedRules(),
				SkipSignatureVerification: true,
			}).Apply(txn)

			require.Equal(t, ter.TefINTERNAL, result.Result)
			require.False(t, result.Applied)
			require.Zero(t, result.Fee)
			require.Equal(t, before, view.data)
		})
	}
}

func TestRemovalFailsClosedOnCorruptLedgerState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(*testing.T, *removalFixture)
		want  ter.Result
	}{
		{
			name: "read failure",
			fault: func(_ *testing.T, f *removalFixture) {
				f.view.readErrors[f.preauthKey.Key] = errors.New("storage failure")
			},
			want: ter.TefINTERNAL,
		},
		{
			name: "malformed entry",
			fault: func(_ *testing.T, f *removalFixture) {
				f.view.data[f.preauthKey.Key] = []byte{1, 2, 3}
			},
			want: ter.TefEXCEPTION,
		},
		{
			name: "mismatched owner",
			fault: func(t *testing.T, f *removalFixture) {
				other := [20]byte{9}
				data, err := state.SerializeDepositPreauth(other, f.targetID, f.page)
				require.NoError(t, err)
				f.view.data[f.preauthKey.Key] = data
			},
			want: ter.TefBAD_LEDGER,
		},
		{
			name: "missing directory link",
			fault: func(_ *testing.T, f *removalFixture) {
				delete(f.view.data, f.dirKey.Key)
			},
			want: ter.TefBAD_LEDGER,
		},
		{
			name: "wrong directory page",
			fault: func(t *testing.T, f *removalFixture) {
				data, err := state.SerializeDepositPreauth(f.ownerID, f.targetID, f.page+1)
				require.NoError(t, err)
				f.view.data[f.preauthKey.Key] = data
			},
			want: ter.TefBAD_LEDGER,
		},
		{
			name: "directory erase failure",
			fault: func(_ *testing.T, f *removalFixture) {
				f.view.eraseError[f.dirKey.Key] = errors.New("storage erase failure")
			},
			want: ter.TefBAD_LEDGER,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRemovalFixture(t)
			tc.fault(t, fixture)
			beforeEntry := bytes.Clone(fixture.view.data[fixture.preauthKey.Key])
			beforeDir := bytes.Clone(fixture.view.data[fixture.dirKey.Key])
			beforeCount := fixture.ctx.Account.OwnerCount

			result := removeFromLedger(fixture.ctx, fixture.preauthKey)

			require.Equal(t, tc.want, result)
			require.Equal(t, beforeEntry, fixture.view.data[fixture.preauthKey.Key])
			require.Equal(t, beforeDir, fixture.view.data[fixture.dirKey.Key])
			require.Equal(t, beforeCount, fixture.ctx.Account.OwnerCount)
		})
	}
}

func TestRemovalConfinesCorruptOwnerCount(t *testing.T) {
	fixture := newRemovalFixture(t)
	fixture.ctx.Account.OwnerCount = 0

	require.Equal(t, ter.TesSUCCESS, removeFromLedger(fixture.ctx, fixture.preauthKey))
	require.Equal(t, uint32(0), fixture.ctx.Account.OwnerCount)
	require.NotContains(t, fixture.view.data, fixture.preauthKey.Key)
}

func TestRemovalRequiresOwnerAccount(t *testing.T) {
	fixture := newRemovalFixture(t)
	beforeEntry := bytes.Clone(fixture.view.data[fixture.preauthKey.Key])
	beforeDir := bytes.Clone(fixture.view.data[fixture.dirKey.Key])
	fixture.ctx.Account = nil

	require.Equal(t, ter.TefINTERNAL, removeFromLedger(fixture.ctx, fixture.preauthKey))
	require.Equal(t, beforeEntry, fixture.view.data[fixture.preauthKey.Key])
	require.Equal(t, beforeDir, fixture.view.data[fixture.dirKey.Key])
}

func TestRemovalCommitFailureRollsBackAllLedgerChanges(t *testing.T) {
	fixture := newRemovalFixture(t)
	owner, err := state.EncodeAccountID(fixture.ownerID)
	require.NoError(t, err)
	accountKey := keylet.Account(fixture.ownerID)
	accountData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:    owner,
		Balance:    1_000_000_000,
		Sequence:   1,
		OwnerCount: 1,
	})
	require.NoError(t, err)
	require.NoError(t, fixture.view.Insert(accountKey, accountData))

	beforeAccount := bytes.Clone(fixture.view.data[accountKey.Key])
	beforeEntry := bytes.Clone(fixture.view.data[fixture.preauthKey.Key])
	beforeDir := bytes.Clone(fixture.view.data[fixture.dirKey.Key])
	fixture.view.eraseError[fixture.preauthKey.Key] = errors.New("storage erase failure")

	txn := NewDepositPreauth(owner)
	txn.Unauthorize, err = state.EncodeAccountID(fixture.targetID)
	require.NoError(t, err)
	txn.Fee = "10"
	txn.SetSequence(1)
	result := engine.NewEngine(fixture.view, tx.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            1,
		ReserveBase:               10_000_000,
		ReserveIncrement:          2_000_000,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	}).Apply(txn)

	require.Equal(t, ter.TefINTERNAL, result.Result)
	require.False(t, result.Applied)
	require.Equal(t, beforeAccount, fixture.view.data[accountKey.Key])
	require.Equal(t, beforeEntry, fixture.view.data[fixture.preauthKey.Key])
	require.Equal(t, beforeDir, fixture.view.data[fixture.dirKey.Key])
}

type removalFixture struct {
	view       *faultView
	ctx        *tx.ApplyContext
	ownerID    [20]byte
	targetID   [20]byte
	preauthKey keylet.Keylet
	dirKey     keylet.Keylet
	page       uint64
}

func newRemovalFixture(t *testing.T) *removalFixture {
	t.Helper()
	ownerID := [20]byte{3}
	targetID := [20]byte{4}
	view := newFaultView()
	preauthKey := keylet.DepositPreauth(ownerID, targetID)
	dirKey := keylet.OwnerDir(ownerID)
	dir, err := state.DirInsert(view, dirKey, preauthKey.Key, false, func(node *state.DirectoryNode) {
		node.Owner = ownerID
	})
	require.NoError(t, err)
	data, err := state.SerializeDepositPreauth(ownerID, targetID, dir.Page)
	require.NoError(t, err)
	require.NoError(t, view.Insert(preauthKey, data))
	return &removalFixture{
		view:       view,
		ctx:        applyContext(view, ownerID, 1),
		ownerID:    ownerID,
		targetID:   targetID,
		preauthKey: preauthKey,
		dirKey:     dirKey,
		page:       dir.Page,
	}
}

func applyContext(view tx.LedgerView, accountID [20]byte, ownerCount uint32) *tx.ApplyContext {
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
