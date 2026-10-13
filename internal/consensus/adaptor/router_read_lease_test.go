package adaptor

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestLedgerReadSelectorUsesMostSpecificRequestIdentity(t *testing.T) {
	var hash [32]byte
	hash[0] = 0xA5

	selector, ok := ledgerReadSelector(&message.GetLedger{LedgerHash: hash[:], LedgerSeq: 19})
	require.True(t, ok)
	require.Equal(t, protocol.Hash256Hex(hash), selector)

	selector, ok = ledgerReadSelector(&message.GetLedger{LedgerSeq: 19})
	require.True(t, ok)
	require.Equal(t, "19", selector)

	selector, ok = ledgerReadSelector(&message.GetLedger{LType: message.LedgerTypeClosed})
	require.True(t, ok)
	require.Equal(t, "closed", selector)

	_, ok = ledgerReadSelector(&message.GetLedger{LType: message.LedgerTypeAccepted})
	require.False(t, ok)
}

func TestLedgerProviderReadLeaseIsUsedForReplayDelta(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := &panicLedgerLookup{}
	provider := newLedgerProviderForTest(lookup)
	var selectors []string
	provider.acquireRead = func(_ context.Context, selector string) (*service.LedgerReadLease, error) {
		selectors = append(selectors, selector)
		return &service.LedgerReadLease{Ledger: closed}, nil
	}

	hash := closed.Hash()
	headerBytes, _, err := provider.GetReplayDeltaContext(context.Background(), hash[:])
	require.NoError(t, err)
	require.NotEmpty(t, headerBytes)
	require.Equal(t, []string{protocol.Hash256Hex(closed.Hash())}, selectors)
}

func TestLedgerProviderReadLeaseIsUsedForProofPath(t *testing.T) {
	closed := makeGenesisLedger(t)
	var key [32]byte
	found := false
	require.NoError(t, closed.ForEach(func(candidate [32]byte, _ []byte) bool {
		key = candidate
		found = true
		return false
	}))
	require.True(t, found)

	provider := newLedgerProviderForTest(&panicLedgerLookup{})
	var selectors []string
	provider.acquireRead = func(_ context.Context, selector string) (*service.LedgerReadLease, error) {
		selectors = append(selectors, selector)
		return &service.LedgerReadLease{Ledger: closed}, nil
	}

	hash := closed.Hash()
	headerBytes, path, err := provider.GetProofPathContext(
		context.Background(),
		hash[:],
		key[:],
		message.LedgerMapAccountState,
	)
	require.NoError(t, err)
	require.NotEmpty(t, headerBytes)
	require.NotEmpty(t, path)
	require.Equal(t, []string{protocol.Hash256Hex(closed.Hash())}, selectors)
}

func TestLedgerReadLeaseCloseIsIdempotent(t *testing.T) {
	var released int
	lease := &ledgerReadLease{
		release: func() { released++ },
	}
	lease.Close()
	lease.Close()
	require.Equal(t, 1, released)
}

type panicLedgerLookup struct{}

func (*panicLedgerLookup) GetLedgerByHash([32]byte) (*ledger.Ledger, error) {
	panic("provider bypassed AcquireLedgerRead")
}

func (*panicLedgerLookup) EarliestFetch() uint32 { return 0 }
