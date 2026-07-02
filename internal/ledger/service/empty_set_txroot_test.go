package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A zero-transaction consensus round must close with tx_root=0 no matter what
// the ingress open ledger accumulated. Closing s.openLedger directly hashes its
// node-local tx map into the header, so an agreed EMPTY round produces per-node
// tx_roots and forks validators carrying different pending traffic.
func TestAcceptConsensusResult_EmptySetClosesWithZeroTxRoot(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Dirty the ingress open ledger's tx map — the node-local traffic no
	// other validator saw.
	svc.mu.Lock()
	require.NoError(t, svc.openLedger.AddTransaction([32]byte{0xD1}, []byte{0x12, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A}))
	svc.mu.Unlock()

	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	_, err = svc.AcceptConsensusResult(context.TODO(), parent, nil, time.Unix(1700000000, 0), true)
	require.NoError(t, err)

	svc.mu.RLock()
	closed := svc.closedLedger
	svc.mu.RUnlock()

	txRoot, err := closed.TxMapHash()
	require.NoError(t, err)
	assert.Equal(t, [32]byte{}, txRoot,
		"an agreed empty set must close with tx_root=0 regardless of ingress traffic")
}
