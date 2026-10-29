package adaptor

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
)

// LedgerWrapper wraps a *ledger.Ledger to implement consensus.Ledger.
type LedgerWrapper struct {
	ledger  *ledger.Ledger
	txSetID consensus.TxSetID
}

// WrapLedger creates a new LedgerWrapper from a ledger.Ledger.
func WrapLedger(l *ledger.Ledger) *LedgerWrapper {
	w := &LedgerWrapper{ledger: l}
	if txHash, err := l.TxMapHash(); err == nil {
		w.txSetID = consensus.TxSetID(txHash)
	}
	return w
}

func (w *LedgerWrapper) ID() consensus.LedgerID {
	return consensus.LedgerID(w.ledger.Hash())
}

func (w *LedgerWrapper) Seq() uint32 {
	return w.ledger.Sequence()
}

func (w *LedgerWrapper) ParentID() consensus.LedgerID {
	return consensus.LedgerID(w.ledger.ParentHash())
}

// HashOfSeq resolves an ancestor hash from the ledger's embedded skip lists.
// A false result means the requested ancestry is not available from this
// ledger; compatibility callers treat that as missing evidence.
func (w *LedgerWrapper) HashOfSeq(seq uint32) (consensus.LedgerID, bool, error) {
	hash, ok, err := w.ledger.HashOfSeq(seq)
	return consensus.LedgerID(hash), ok, err
}

func (w *LedgerWrapper) CloseTime() time.Time {
	return w.ledger.CloseTime()
}

// CloseAgree reports whether this ledger's close time was reached by
// consensus (false when it was defaulted to parentClose+1s).
func (w *LedgerWrapper) CloseAgree() bool {
	h := w.ledger.Header()
	return h.GetCloseAgree()
}

func (w *LedgerWrapper) CloseTimeResolution() time.Duration {
	return time.Duration(w.ledger.CloseTimeResolution()) * time.Second
}

func (w *LedgerWrapper) ParentCloseTime() time.Time {
	return w.ledger.ParentCloseTime()
}

func (w *LedgerWrapper) TxSetID() consensus.TxSetID {
	return w.txSetID
}

func (w *LedgerWrapper) Bytes() []byte {
	return w.ledger.SerializeHeader()
}

// Unwrap returns the underlying *ledger.Ledger.
func (w *LedgerWrapper) Unwrap() *ledger.Ledger {
	return w.ledger
}
