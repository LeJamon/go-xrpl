package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"sync"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

const validatedLedgerPublicationKind = "validated-ledger"

// prepareIndexedTransaction validates and copies one transaction-tree leaf.
// The ledger iterator borrows txData until its callback returns, so the
// returned payloads own their bytes before the stream yields the record.
func prepareIndexedTransaction(
	txHashBytes [32]byte,
	txData []byte,
	seq relationaldb.LedgerIndex,
) (relationaldb.IndexedTransaction, error) {
	txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(txData)
	if err != nil {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("split transaction %x: %w", txHashBytes[:8], err)
	}

	parsedTransaction, err := tx.ParseFromBinary(txBlob)
	if err != nil {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("decode transaction %x: %w", txHashBytes[:8], err)
	}
	computedHash, err := tx.ComputeTransactionHash(parsedTransaction)
	if err != nil {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("hash transaction %x: %w", txHashBytes[:8], err)
	}
	if computedHash != txHashBytes {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("transaction hash %x does not match map key %x", computedHash, txHashBytes)
	}

	var accountID relationaldb.AccountID
	account := parsedTransaction.GetCommon().Account
	if account != "" {
		_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
		if err != nil || len(accountBytes) != len(accountID) {
			return relationaldb.IndexedTransaction{}, fmt.Errorf("decode transaction %x Account: %w", txHashBytes[:8], errors.Join(err, relationaldb.ErrInvalidData))
		}
		copy(accountID[:], accountBytes)
	}

	affected := map[relationaldb.AccountID]struct{}{}
	txnSeq, ok := tx.TransactionIndexFromMetadata(metaBlob)
	if !ok {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("decode transaction metadata %x: missing or invalid TransactionIndex", txHashBytes[:8])
	}
	metaJSON, err := binarycodec.Decode(hex.EncodeToString(metaBlob))
	if err != nil {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("decode transaction metadata %x: %w", txHashBytes[:8], err)
	}
	if result, ok := metaJSON["TransactionResult"].(string); !ok || result == "" {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("decode transaction metadata %x: missing or invalid TransactionResult", txHashBytes[:8])
	}
	if err := addMetaAffectedAccounts(metaJSON, affected); err != nil {
		return relationaldb.IndexedTransaction{}, fmt.Errorf("decode transaction metadata %x: %w", txHashBytes[:8], err)
	}

	return relationaldb.IndexedTransaction{
		Transaction: relationaldb.TransactionInfo{
			Hash:      relationaldb.Hash(txHashBytes),
			LedgerSeq: seq,
			TxnSeq:    txnSeq,
			Status:    "validated",
			RawTxn:    append([]byte(nil), txBlob...),
			TxnMeta:   append([]byte(nil), metaBlob...),
			Account:   accountID,
		},
		Accounts: sortedAccountIDs(affected),
	}, nil
}

func collectIndexedTransactions(ctx context.Context, l *ledger.Ledger, seq relationaldb.LedgerIndex) ([]relationaldb.IndexedTransaction, error) {
	indexed := make([]relationaldb.IndexedTransaction, 0)
	var loopErr error
	err := l.ForEachTransactionContext(ctx, func(txHashBytes [32]byte, txData []byte) bool {
		item, err := prepareIndexedTransaction(txHashBytes, txData, seq)
		if err != nil {
			loopErr = err
			return false
		}
		indexed = append(indexed, item)
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("iterate transaction map: %w", err)
	}
	if loopErr != nil {
		return nil, loopErr
	}
	return indexed, nil
}

type ledgerTransactionPull struct {
	nextRaw    func() ([32]byte, []byte, bool)
	stopRaw    func()
	cancel     context.CancelFunc
	once       sync.Once
	errMu      sync.Mutex
	iterateErr error
	sequence   relationaldb.LedgerIndex
}

func newLedgerTransactionPull(ctx context.Context, l *ledger.Ledger, seq relationaldb.LedgerIndex) *ledgerTransactionPull {
	if ctx == nil {
		ctx = context.Background()
	}
	// #nosec G118 -- The returned pull owns cancellation; its close method is deferred by the consumer.
	sourceCtx, cancel := context.WithCancel(ctx)
	pull := &ledgerTransactionPull{cancel: cancel, sequence: seq}
	pull.nextRaw, pull.stopRaw = iter.Pull2(func(yield func([32]byte, []byte) bool) {
		err := l.ForEachTransactionContext(sourceCtx, func(txHashBytes [32]byte, txData []byte) bool {
			if sourceCtx.Err() != nil {
				return false
			}
			return yield(txHashBytes, txData)
		})
		if err == nil {
			err = sourceCtx.Err()
		}
		pull.errMu.Lock()
		pull.iterateErr = err
		pull.errMu.Unlock()
	})
	return pull
}

func (p *ledgerTransactionPull) close() {
	p.once.Do(func() {
		p.cancel()
		p.stopRaw()
	})
}

func (p *ledgerTransactionPull) next(ctx context.Context) (relationaldb.IndexedTransaction, bool, error) {
	if ctx == nil {
		p.close()
		return relationaldb.IndexedTransaction{}, false, errors.New("transaction stream: nil context")
	}
	if err := ctx.Err(); err != nil {
		p.close()
		return relationaldb.IndexedTransaction{}, false, err
	}
	txHashBytes, txData, ok := p.nextRaw()
	if !ok {
		p.errMu.Lock()
		err := p.iterateErr
		p.errMu.Unlock()
		p.close()
		if err != nil {
			return relationaldb.IndexedTransaction{}, false, fmt.Errorf("iterate transaction map: %w", err)
		}
		return relationaldb.IndexedTransaction{}, false, nil
	}
	item, err := prepareIndexedTransaction(txHashBytes, txData, p.sequence)
	if err != nil {
		p.close()
		return relationaldb.IndexedTransaction{}, false, err
	}
	return item, true, nil
}

func persistLedgerTransactionStream(
	ctx context.Context,
	publisher relationaldb.StreamingLedgerPublisher,
	l *ledger.Ledger,
	ledgerInfo relationaldb.LedgerInfo,
) error {
	pull := newLedgerTransactionPull(ctx, l, relationaldb.LedgerIndex(l.Sequence()))
	defer pull.close()
	commitID := flatstate.SemanticCommitID(l.Hash(), validatedLedgerPublicationKind)
	return publisher.PersistLedgerStream(ctx, ledgerInfo, [32]byte(commitID), validatedLedgerPublicationKind, pull.next)
}
