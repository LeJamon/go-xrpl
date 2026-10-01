package engine

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type pseudoStorageFamily map[[32]byte][]byte

func (f pseudoStorageFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
	return f[hash], nil
}

func (f pseudoStorageFamily) StoreBatch(_ context.Context, entries []shamap.FlushEntry) error {
	for _, entry := range entries {
		f[entry.Hash] = entry.Data
	}
	return nil
}

type ignoredPseudoReadTx struct {
	txcore.Transaction
	missingKey keylet.Keylet
	writeKey   keylet.Keylet
	data       []byte
	readErr    error
}

func (tx *ignoredPseudoReadTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	_, tx.readErr = ctx.View.Read(tx.missingKey)
	if err := ctx.View.Update(tx.writeKey, tx.data); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

func TestApplyPseudoIgnoredStorageErrorDoesNotCommitSnapshot(t *testing.T) {
	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	require.NoError(t, err)
	accountKey := keylet.Account(accountID)
	var missingKey keylet.Keylet
	for branch := byte(0); branch < 16; branch++ {
		if branch != accountKey.Key[0]>>4 && branch != keylet.Amendments().Key[0]>>4 {
			missingKey.Key[0] = branch << 4
			break
		}
	}
	account := &state.AccountRoot{Account: recoveryTestAccount, Balance: 1_000_000, Sequence: 1}
	originalData, err := state.SerializeAccountRoot(account)
	require.NoError(t, err)
	intact := shamap.New(shamap.TypeState)
	require.NoError(t, intact.Put(accountKey.Key, originalData))
	require.NoError(t, intact.Put(missingKey.Key, []byte("missing state")))
	rootHash, err := intact.Hash()
	require.NoError(t, err)
	family := make(pseudoStorageFamily)
	require.NoError(t, intact.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	root, err := shamap.DeserializeFromPrefix(family[rootHash])
	require.NoError(t, err)
	missingHash, err := root.(shamap.InnerNodeReader).ChildHash(int(missingKey.Key[0] >> 4))
	require.NoError(t, err)
	delete(family, missingHash)
	stateMap, err := shamap.NewFromRootHash(shamap.TypeState, rootHash, family)
	require.NoError(t, err)
	view, err := ledger.NewOpenWithHeader(header.LedgerHeader{LedgerIndex: 100}, stateMap, shamap.New(shamap.TypeTransaction), drops.Fees{})
	require.NoError(t, err)
	account.Balance++
	changedData, err := state.SerializeAccountRoot(account)
	require.NoError(t, err)
	txn := &ignoredPseudoReadTx{
		Transaction: newApplyPanicAmendment(100),
		missingKey:  missingKey,
		writeKey:    accountKey,
		data:        changedData,
	}
	engine := pseudoRecoveryEngine(view, 100)
	result := engine.ApplyPseudo(txn)
	assertNonAppliedResult(t, result, ter.TefEXCEPTION)
	require.ErrorIs(t, txn.readErr, shamap.ErrNodeNotInStore)
	var missing *shamap.MissingNodeError
	require.ErrorAs(t, result.Cause, &missing)
	require.Equal(t, missingHash, missing.Hash)
	data, err := view.Read(accountKey)
	require.NoError(t, err)
	require.Equal(t, originalData, data)
	require.Zero(t, engine.TxCount())
}
