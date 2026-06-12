package batch

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	xtesting "github.com/LeJamon/go-xrpl/internal/testing"
)

// TestBuilderSortsNestedSignersByBinaryAccountID guards the builder against the
// base58-vs-binary ordering hazard: the batch verifier requires nested signers
// in strictly-increasing binary AccountID order, but base58 address order does
// not always agree. "acct0" and "acct2" are a deterministic divergent pair —
// base58 orders acct0 after acct2, binary orders it before — so a base58 sort
// would emit them in the order the verifier rejects.
func TestBuilderSortsNestedSignersByBinaryAccountID(t *testing.T) {
	master := xtesting.NewAccount("master")
	a := xtesting.NewAccount("acct0")
	b := xtesting.NewAccount("acct2")

	// Confirm the fixture actually diverges, otherwise the test proves nothing.
	addrLess := a.Address < b.Address
	binLess := bytes.Compare(a.ID[:], b.ID[:]) < 0
	require.NotEqual(t, addrLess, binLess,
		"fixture accounts must have divergent base58/binary order")

	// Pass them in base58-descending order so a string sort would be a no-op.
	signers := []*xtesting.Account{a, b}
	if a.Address < b.Address {
		signers = []*xtesting.Account{b, a}
	}

	batch := NewBatchBuilder(master, 1, 100, 0x00000001).
		AddInnerTx(MakeFakeInnerTx()).
		AddInnerTx(MakeFakeInnerTx()).
		AddMultiSignBatchSigner(master, signers).
		Build()

	require.Len(t, batch.BatchSigners, 1)
	nested := batch.BatchSigners[0].BatchSigner.Signers
	require.Len(t, nested, 2)

	// The nested signers must be strictly increasing by binary AccountID.
	var last [20]byte
	for i, sw := range nested {
		id, err := state.DecodeAccountID(sw.Signer.Account)
		require.NoError(t, err)
		if i > 0 {
			require.Negative(t, bytes.Compare(last[:], id[:]),
				"nested signers must be strictly increasing by binary AccountID")
		}
		last = id
	}
}
