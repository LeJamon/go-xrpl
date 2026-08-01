package ledger

import (
	"context"

	"github.com/LeJamon/go-xrpl/shamap"
)

func (l *Ledger) AddTransaction(txHash [32]byte, txData []byte) error {
	return l.addTransaction(txHash, txData, shamap.NodeTypeTransactionNoMeta)
}

func (l *Ledger) addTransaction(txHash [32]byte, txData []byte, nodeType shamap.NodeType) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != StateOpen || !l.writable {
		return ErrLedgerImmutable
	}
	exists, err := l.txMap.Has(txHash)
	if err != nil {
		return err
	}
	if exists {
		return ErrEntryExists
	}
	return l.txMap.PutWithNodeType(txHash, txData, nodeType)
}

// AddTransactionWithMeta adds a tx with metadata, using NodeTypeTransactionWithMeta
// for correct tx-tree hashing.
func (l *Ledger) AddTransactionWithMeta(txHash [32]byte, txWithMetaData []byte) error {
	return l.addTransaction(txHash, txWithMetaData, shamap.NodeTypeTransactionWithMeta)
}

func (l *Ledger) GetTransaction(txHash [32]byte) ([]byte, bool, error) {
	return l.GetTransactionContext(context.Background(), txHash)
}

func (l *Ledger) GetTransactionContext(ctx context.Context, txHash [32]byte) ([]byte, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	item, found, err := l.txMap.GetContext(ctx, txHash)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	return item.Data(), true, nil
}

// TxExists reports whether a tx with the given hash is already in this ledger.
func (l *Ledger) TxExists(txHash [32]byte) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.txMap.Has(txHash)
}
