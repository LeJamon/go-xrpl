package account

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
	"github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type accountDeleteDirectoryView struct {
	data       map[[32]byte][]byte
	eraseError map[[32]byte]error
	rules      *amendment.Rules
}

func newAccountDeleteDirectoryView() *accountDeleteDirectoryView {
	return &accountDeleteDirectoryView{
		data:       make(map[[32]byte][]byte),
		eraseError: make(map[[32]byte]error),
		rules:      amendment.AllSupportedRules(),
	}
}

func (v *accountDeleteDirectoryView) Read(k keylet.Keylet) ([]byte, error) {
	return bytes.Clone(v.data[k.Key]), nil
}

func (v *accountDeleteDirectoryView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}

func (v *accountDeleteDirectoryView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *accountDeleteDirectoryView) Update(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = bytes.Clone(data)
	return nil
}

func (v *accountDeleteDirectoryView) Erase(k keylet.Keylet) error {
	if err := v.eraseError[k.Key]; err != nil {
		return err
	}
	delete(v.data, k.Key)
	return nil
}

func (v *accountDeleteDirectoryView) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	staged := newAccountDeleteDirectoryView()
	staged.rules = v.rules
	staged.eraseError = v.eraseError
	staged.data = cloneAccountDeleteDirectoryData(v.data)
	if err := apply(staged); err != nil {
		return err
	}
	v.data = staged.data
	return nil
}

func (*accountDeleteDirectoryView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*accountDeleteDirectoryView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *accountDeleteDirectoryView) Rules() *amendment.Rules                  { return v.rules }
func (*accountDeleteDirectoryView) LedgerSeq() uint32                          { return 300 }

func (v *accountDeleteDirectoryView) ForEach(fn func([32]byte, []byte) bool) error {
	for key, data := range v.data {
		if !fn(key, bytes.Clone(data)) {
			break
		}
	}
	return nil
}

func (v *accountDeleteDirectoryView) Succ(after [32]byte) ([32]byte, []byte, bool, error) {
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

func TestAccountDeleteRemovesLegacyEmptyOwnerDirectory(t *testing.T) {
	view, accountDelete, _, ownerDir := accountDeleteDirectoryFixture(t)
	putEmptyOwnerDirPage(t, view, ownerDir, 0, 1, 1)
	putEmptyOwnerDirPage(t, view, ownerDir, 1, 0, 0)
	sourceID, err := state.DecodeAccountID(accountDelete.Account)
	require.NoError(t, err)
	destinationID, err := state.DecodeAccountID(accountDelete.Destination)
	require.NoError(t, err)
	accountDelete.Fee = "200000"
	accountDelete.SetSequence(1)

	result := engine.NewEngine(view, accountDeleteEngineConfig(view.rules)).Apply(accountDelete)
	require.Equal(t, ter.TesSUCCESS, result.Result)
	require.True(t, result.Applied)
	require.Equal(t, uint64(200_000), result.Fee)
	require.NotNil(t, result.Metadata)
	deletedDirectories := 0
	for _, node := range result.Metadata.AffectedNodes {
		if node.NodeType == "DeletedNode" && node.LedgerEntryType == "DirectoryNode" {
			deletedDirectories++
		}
	}
	require.Equal(t, 2, deletedDirectories)
	require.NotContains(t, view.data, ownerDir.Key)
	require.NotContains(t, view.data, keylet.DirPage(ownerDir.Key, 1).Key)
	require.NotContains(t, view.data, keylet.Account(sourceID).Key)
	destinationData := view.data[keylet.Account(destinationID).Key]
	destination, err := state.ParseAccountRoot(destinationData)
	require.NoError(t, err)
	require.Equal(t, uint64(1_800_000), destination.Balance)
}

func TestAccountDeletePropagatesOwnerDirectoryEraseFailure(t *testing.T) {
	view, accountDelete, ctx, ownerDir := accountDeleteDirectoryFixture(t)
	putEmptyOwnerDirPage(t, view, ownerDir, 0, 0, 0)
	view.eraseError[ownerDir.Key] = errors.New("storage erase failure")
	before := cloneAccountDeleteDirectoryData(view.data)

	require.Equal(t, ter.TefINTERNAL, accountDelete.Apply(ctx))
	require.Equal(t, before, view.data)
}

func TestAccountDeleteRejectsMalformedOwnerDirectoryChain(t *testing.T) {
	view, accountDelete, ctx, ownerDir := accountDeleteDirectoryFixture(t)
	putEmptyOwnerDirPage(t, view, ownerDir, 0, 0, 1)
	before := cloneAccountDeleteDirectoryData(view.data)

	require.Equal(t, ter.TefEXCEPTION, accountDelete.Apply(ctx))
	require.Equal(t, before, view.data)
}

func TestAccountDeleteRejectsMismatchedOwnerDirectoryRoot(t *testing.T) {
	view, accountDelete, ctx, ownerDir := accountDeleteDirectoryFixture(t)
	node := &state.DirectoryNode{RootIndex: [32]byte{9}, Owner: [20]byte{1}}
	data, err := state.SerializeDirectoryNode(node, false)
	require.NoError(t, err)
	require.NoError(t, view.Insert(ownerDir, data))
	before := cloneAccountDeleteDirectoryData(view.data)

	require.Equal(t, ter.TecHAS_OBLIGATIONS, accountDelete.Apply(ctx))
	require.Equal(t, before, view.data)
}

func TestAccountDeleteEngineMismatchedOwnerDirectoryRootClearsDeliveredAmount(t *testing.T) {
	view, accountDelete, _, ownerDir := accountDeleteDirectoryFixture(t)
	node := &state.DirectoryNode{RootIndex: [32]byte{9}, Owner: [20]byte{1}}
	directoryData, err := state.SerializeDirectoryNode(node, false)
	require.NoError(t, err)
	require.NoError(t, view.Insert(ownerDir, directoryData))
	sourceID, err := state.DecodeAccountID(accountDelete.Account)
	require.NoError(t, err)
	destinationID, err := state.DecodeAccountID(accountDelete.Destination)
	require.NoError(t, err)
	accountDelete.Fee = "200000"
	accountDelete.SetSequence(1)

	result := engine.NewEngine(view, accountDeleteEngineConfig(view.rules)).Apply(accountDelete)

	require.Equal(t, ter.TecHAS_OBLIGATIONS, result.Result)
	require.True(t, result.Applied)
	require.Equal(t, uint64(200_000), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Nil(t, result.Metadata.DeliveredAmount)
	require.Equal(t, directoryData, view.data[ownerDir.Key])

	source, err := state.ParseAccountRoot(view.data[keylet.Account(sourceID).Key])
	require.NoError(t, err)
	require.Equal(t, uint64(800_000), source.Balance)
	require.Equal(t, uint32(2), source.Sequence)
	destination, err := state.ParseAccountRoot(view.data[keylet.Account(destinationID).Key])
	require.NoError(t, err)
	require.Equal(t, uint64(1_000_000), destination.Balance)
}

func TestAccountDeleteEngineRollsBackLegacyDirectoryEraseFailure(t *testing.T) {
	view, accountDelete, _, ownerDir := accountDeleteDirectoryFixture(t)
	putEmptyOwnerDirPage(t, view, ownerDir, 0, 1, 1)
	putEmptyOwnerDirPage(t, view, ownerDir, 1, 0, 0)
	view.eraseError[ownerDir.Key] = errors.New("storage erase failure")
	before := cloneAccountDeleteDirectoryData(view.data)
	accountDelete.Fee = "200000"
	accountDelete.SetSequence(1)

	result := engine.NewEngine(view, accountDeleteEngineConfig(view.rules)).Apply(accountDelete)

	require.Equal(t, ter.TefINTERNAL, result.Result)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
	require.Equal(t, before, view.data)
}

func accountDeleteEngineConfig(rules *amendment.Rules) tx.EngineConfig {
	return tx.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            300,
		ReserveBase:               100_000,
		ReserveIncrement:          200_000,
		Rules:                     rules,
		SkipSignatureVerification: true,
	}
}

func accountDeleteDirectoryFixture(t *testing.T) (*accountDeleteDirectoryView, *AccountDelete, *tx.ApplyContext, keylet.Keylet) {
	t.Helper()
	view := newAccountDeleteDirectoryView()
	sourceID := [20]byte{1}
	destinationID := [20]byte{2}
	source, err := state.EncodeAccountID(sourceID)
	require.NoError(t, err)
	destination, err := state.EncodeAccountID(destinationID)
	require.NoError(t, err)
	destinationData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  destination,
		Balance:  1_000_000,
		Sequence: 1,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(destinationID), destinationData))
	sourceData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  source,
		Balance:  1_000_000,
		Sequence: 1,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(sourceID), sourceData))

	accountDelete := NewAccountDelete(source, destination)
	ctx := &tx.ApplyContext{
		View:      view,
		AccountID: sourceID,
		Account: &state.AccountRoot{
			Account:  source,
			Balance:  1_000_000,
			Sequence: 1,
		},
		Config: tx.EngineConfig{
			LedgerSequence:   300,
			ReserveIncrement: 200_000,
			Rules:            view.rules,
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
	}
	return view, accountDelete, ctx, keylet.OwnerDir(sourceID)
}

func cloneAccountDeleteDirectoryData(data map[[32]byte][]byte) map[[32]byte][]byte {
	cloned := make(map[[32]byte][]byte, len(data))
	for key, value := range data {
		cloned[key] = bytes.Clone(value)
	}
	return cloned
}

func putEmptyOwnerDirPage(
	t *testing.T,
	view *accountDeleteDirectoryView,
	dir keylet.Keylet,
	page, next, previous uint64,
) {
	t.Helper()
	node := &state.DirectoryNode{RootIndex: dir.Key, Owner: [20]byte{1}}
	node.SetIndexNext(next)
	node.SetIndexPrevious(previous)
	data, err := state.SerializeDirectoryNode(node, false)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.DirPage(dir.Key, page), data))
}
