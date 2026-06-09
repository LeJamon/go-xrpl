package adaptor

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// This file collects test-only helpers that were previously declared in
// production source files. They are exercised solely by the package's
// *_test.go files; keeping them here keeps the production binary free of
// test scaffolding.

// TransactionToMessage wraps a raw transaction blob into a Transaction message.
func TransactionToMessage(txBlob []byte) *message.Transaction {
	return &message.Transaction{
		RawTransaction:   txBlob,
		Status:           message.TxStatusNew,
		ReceiveTimestamp: uint64(time.Now().UnixNano()),
	}
}

// HaveSetToMessage creates a HaveTransactionSet message.
func HaveSetToMessage(id consensus.TxSetID, status message.TxSetStatus) *message.HaveTransactionSet {
	return &message.HaveTransactionSet{
		Status: status,
		Hash:   id[:],
	}
}

// size reports the number of cached fetch-pack nodes.
func (c *fetchPackCache) size() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.nodes)
}

// newLedgerProviderForTest builds a provider over an arbitrary lookup.
func newLedgerProviderForTest(lookup ledgerLookup) *LedgerProvider {
	return &LedgerProvider{svc: lookup}
}

// NewRouterBroadcaster wires the overlay (for peer enumeration + feature
// lookup) and the sender (for per-peer SendToPeer). Passing nil for either
// degrades the broadcaster to a silent no-op so tests without an overlay
// don't crash. Broadcasters built this way carry no hash-suppression
// registry; production routes through Router.NewValidatorListBroadcaster.
func NewRouterBroadcaster(overlay *peermanagement.Overlay, sender NetworkSender) *RouterBroadcaster {
	return &RouterBroadcaster{overlay: overlay, sender: sender}
}

func txSetReplyCapsForTest() (soft, hard int) {
	return txSetSoftMaxReplyNodes, txSetHardMaxReplyNodes
}

func setTxSetReplyCapsForTest(soft, hard int) {
	txSetSoftMaxReplyNodes = soft
	txSetHardMaxReplyNodes = hard
}
