package postgres

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/publication"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

const maxPublicationKindBytes = 4 << 10

const publicationCommitLockNamespace int64 = 0x5354524d // STRM

var _ relationaldb.StreamingLedgerPublisher = (*RepositoryManager)(nil)

type publicationReceipt struct {
	ledgerHash      relationaldb.Hash
	ledgerSequence  relationaldb.LedgerIndex
	publicationKind string
	digest          [32]byte
}

const publicationStreamIndexTable = `
CREATE TEMP TABLE IF NOT EXISTS xrpl_publication_stream_indexes (
	txn_seq BIGINT PRIMARY KEY CHECK(txn_seq BETWEEN 0 AND 4294967295),
	trans_id BYTEA NOT NULL UNIQUE CHECK(octet_length(trans_id) = 32)
) ON COMMIT DELETE ROWS`

// PersistLedgerStream atomically publishes a ledger from a bounded callback.
// The callback's true result supplies one transaction; false ends the stream.
func (rm *RepositoryManager) PersistLedgerStream(
	ctx context.Context,
	ledger relationaldb.LedgerInfo,
	commitID [32]byte,
	publicationKind string,
	next relationaldb.IndexedTransactionStream,
) error {
	if err := validatePublicationRequest(commitID, publicationKind, next); err != nil {
		return err
	}
	end, err := rm.gate.Begin()
	if err != nil {
		return err
	}
	defer end()

	tx, err := rm.db.BeginTx(ctx, nil)
	if err != nil {
		return relationaldb.NewTransactionError("persist_ledger_stream", "begin transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	sequenceLock := persistLedgerLockNamespace<<32 | int64(ledger.Sequence)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, sequenceLock); err != nil {
		return relationaldb.NewTransactionError("persist_ledger_stream", "acquire ledger persistence lock", err)
	}
	commitLock := publicationCommitLockNamespace ^ int64(binary.BigEndian.Uint64(commitID[:8]))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, commitLock); err != nil {
		return relationaldb.NewTransactionError("persist_ledger_stream", "acquire publication persistence lock", err)
	}

	receipt, found, err := lookupPublicationReceipt(ctx, tx, commitID)
	if err != nil {
		return relationaldb.NewQueryError("persist_ledger_stream", "look up publication receipt", err)
	}
	if found && (receipt.ledgerHash != ledger.Hash || receipt.ledgerSequence != ledger.Sequence || receipt.publicationKind != publicationKind) {
		return publicationConflict("publication descriptor or intent differs from committed receipt")
	}
	if !found {
		if err := validatePublicationLedger(ledger); err != nil {
			return err
		}
	}

	if err := initializePublicationIndexes(ctx, tx); err != nil {
		return relationaldb.NewTransactionError("persist_ledger_stream", "initialize stream indexes", err)
	}
	if !found {
		if err := deleteLedgerPublication(ctx, tx, ledger.Sequence); err != nil {
			return err
		}
	}

	digest := publication.NewDigest(ledger, commitID, publicationKind)
	count, err := streamTransactions(ctx, tx, ledger, next, digest, !found, rm.persistHook)
	if err != nil {
		return err
	}
	if err := finishStreamValidation(ctx, ledger, tx, count); err != nil {
		return err
	}
	if found {
		if digest.Sum() != receipt.digest {
			return publicationConflict("transaction stream differs from committed receipt")
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return relationaldb.NewTransactionError("persist_ledger_stream", "commit duplicate publication", err)
		}
		committed = true
		return nil
	}

	if err := newLedgerRepository(tx).SaveValidatedLedger(ctx, ledger); err != nil {
		return err
	}
	if rm.persistHook != nil {
		if err := rm.persistHook("ledger", count); err != nil {
			return err
		}
	}
	digestValue := digest.Sum()
	if _, err := tx.ExecContext(ctx, `INSERT INTO ledger_publication_receipts
		(commit_id, ledger_hash, ledger_seq, publication_kind, digest)
		VALUES ($1, $2, $3, $4, $5)`, commitID[:], ledger.Hash[:], ledger.Sequence, publicationKind, digestValue[:]); err != nil {
		return relationaldb.NewQueryError("persist_ledger_stream", "save publication receipt", err)
	}
	if rm.persistHook != nil {
		if err := rm.persistHook("commit", count); err != nil {
			return err
		}
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return relationaldb.NewTransactionError("persist_ledger_stream", "commit publication", err)
	}
	committed = true
	return nil
}

func validatePublicationRequest(
	commitID [32]byte,
	publicationKind string,
	next relationaldb.IndexedTransactionStream,
) error {
	if commitID == [32]byte{} {
		return relationaldb.NewDataError("persist_ledger_stream", "commit ID is zero", relationaldb.ErrInvalidData)
	}
	if publicationKind == "" {
		return relationaldb.NewDataError("persist_ledger_stream", "publication kind is empty", relationaldb.ErrInvalidData)
	}
	if len(publicationKind) > maxPublicationKindBytes {
		return relationaldb.NewDataError("persist_ledger_stream", "publication kind is too large", relationaldb.ErrInvalidData)
	}
	if next == nil {
		return relationaldb.NewDataError("persist_ledger_stream", "transaction stream is nil", relationaldb.ErrInvalidData)
	}
	return nil
}

func validatePublicationLedger(ledger relationaldb.LedgerInfo) error {
	if ledger.Hash.IsZero() {
		return relationaldb.NewDataError("persist_ledger_stream", "ledger hash is zero", relationaldb.ErrInvalidData)
	}
	if ledger.AccountHash.IsZero() {
		return relationaldb.NewDataError("persist_ledger_stream", "account state hash is zero", relationaldb.ErrInvalidData)
	}
	return nil
}

func lookupPublicationReceipt(ctx context.Context, tx *sqlutil.Tx, commitID [32]byte) (publicationReceipt, bool, error) {
	var receipt publicationReceipt
	var ledgerHash, digest []byte
	err := tx.QueryRowContext(ctx, `SELECT ledger_hash, ledger_seq, publication_kind, digest
		FROM ledger_publication_receipts WHERE commit_id = $1`, commitID[:]).Scan(
		&ledgerHash, &receipt.ledgerSequence, &receipt.publicationKind, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return publicationReceipt{}, false, nil
	}
	if err != nil {
		return publicationReceipt{}, false, err
	}
	if len(ledgerHash) != len(receipt.ledgerHash) || len(digest) != len(receipt.digest) {
		return publicationReceipt{}, false, fmt.Errorf("%w: malformed publication receipt", relationaldb.ErrInvalidData)
	}
	copy(receipt.ledgerHash[:], ledgerHash)
	copy(receipt.digest[:], digest)
	return receipt, true, nil
}

func initializePublicationIndexes(ctx context.Context, tx *sqlutil.Tx) error {
	if _, err := tx.ExecContext(ctx, publicationStreamIndexTable); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM xrpl_publication_stream_indexes"); err != nil {
		return err
	}
	return nil
}

func deleteLedgerPublication(ctx context.Context, tx *sqlutil.Tx, sequence relationaldb.LedgerIndex) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM account_transactions WHERE ledger_seq = $1", sequence); err != nil {
		return relationaldb.NewQueryError("persist_ledger_stream", "delete account indexes for ledger", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM transactions WHERE ledger_seq = $1", sequence); err != nil {
		return relationaldb.NewQueryError("persist_ledger_stream", "delete transactions for ledger", err)
	}
	return nil
}

func streamTransactions(
	ctx context.Context,
	tx *sqlutil.Tx,
	ledger relationaldb.LedgerInfo,
	next relationaldb.IndexedTransactionStream,
	digest *publication.Digest,
	writeRows bool,
	hook func(string, int) error,
) (int, error) {
	transactionCount := 0
	indexCount := 0
	for {
		if err := contextError(ctx); err != nil {
			return 0, err
		}
		indexed, ok, err := next(ctx)
		if err != nil {
			return 0, err
		}
		if !ok {
			return transactionCount, nil
		}
		if err := validateIndexedTransaction(ledger, indexed); err != nil {
			return 0, err
		}
		accounts := slices.Clone(indexed.Accounts)
		slices.SortFunc(accounts, compareAccountID)
		for i := 1; i < len(accounts); i++ {
			if accounts[i-1] == accounts[i] {
				return 0, relationaldb.NewDataError("persist_ledger_stream", fmt.Sprintf("transaction %d has duplicated account", transactionCount), relationaldb.ErrInvalidData)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO xrpl_publication_stream_indexes(txn_seq, trans_id) VALUES ($1, $2)`, indexed.Transaction.TxnSeq, indexed.Transaction.Hash[:]); err != nil {
			return 0, relationaldb.NewDataError("persist_ledger_stream", fmt.Sprintf("transaction %d has duplicate index or hash", transactionCount), errors.Join(relationaldb.ErrInvalidData, err))
		}
		digest.Add(indexed)
		transactionCount++
		if writeRows {
			if err := ensureTransactionHashAvailable(ctx, tx, indexed.Transaction.Hash); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO transactions
				(trans_id, ledger_seq, status, raw_txn, txn_meta) VALUES ($1, $2, $3, $4, $5)`,
				indexed.Transaction.Hash[:], indexed.Transaction.LedgerSeq, indexed.Transaction.Status,
				indexed.Transaction.RawTxn, indexed.Transaction.TxnMeta); err != nil {
				return 0, relationaldb.NewQueryError("persist_ledger_stream", "save transaction", err)
			}
			indexCount++
			if hook != nil {
				if err := hook("index", indexCount); err != nil {
					return 0, err
				}
			}
			for _, account := range indexed.Accounts {
				if _, err := tx.ExecContext(ctx, `INSERT INTO account_transactions
					(trans_id, account, ledger_seq, txn_seq) VALUES ($1, $2, $3, $4)`,
					indexed.Transaction.Hash[:], account.String(), indexed.Transaction.LedgerSeq, indexed.Transaction.TxnSeq); err != nil {
					return 0, relationaldb.NewQueryError("persist_ledger_stream", "save account transaction", err)
				}
				indexCount++
				if hook != nil {
					if err := hook("index", indexCount); err != nil {
						return 0, err
					}
				}
			}
		}
	}
}

func ensureTransactionHashAvailable(ctx context.Context, tx *sqlutil.Tx, hash relationaldb.Hash) error {
	var sequence relationaldb.LedgerIndex
	err := tx.QueryRowContext(ctx, "SELECT ledger_seq FROM transactions WHERE trans_id = $1", hash[:]).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return relationaldb.NewQueryError("persist_ledger_stream", "check transaction hash", err)
	}
	return relationaldb.NewDataError("persist_ledger_stream", fmt.Sprintf("transaction hash already belongs to ledger %d", sequence), relationaldb.ErrInvalidData)
}

func validateIndexedTransaction(ledger relationaldb.LedgerInfo, indexed relationaldb.IndexedTransaction) error {
	tx := indexed.Transaction
	if tx.LedgerSeq != ledger.Sequence {
		return relationaldb.NewDataError("persist_ledger_stream", fmt.Sprintf("transaction has ledger sequence %d, want %d", tx.LedgerSeq, ledger.Sequence), relationaldb.ErrInvalidData)
	}
	if tx.Hash.IsZero() {
		return relationaldb.NewDataError("persist_ledger_stream", "transaction hash is zero", relationaldb.ErrInvalidData)
	}
	if len(tx.RawTxn) == 0 {
		return relationaldb.NewDataError("persist_ledger_stream", "transaction payload is empty", relationaldb.ErrInvalidData)
	}
	if len(tx.TxnMeta) == 0 {
		return relationaldb.NewDataError("persist_ledger_stream", "transaction metadata is empty", relationaldb.ErrInvalidData)
	}
	for i, account := range indexed.Accounts {
		if account.IsZero() {
			return relationaldb.NewDataError("persist_ledger_stream", fmt.Sprintf("transaction account %d is zero", i), relationaldb.ErrInvalidData)
		}
	}
	return nil
}

func finishStreamValidation(ctx context.Context, ledger relationaldb.LedgerInfo, tx *sqlutil.Tx, count int) error {
	var rows int64
	var minimum, maximum sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), MIN(txn_seq), MAX(txn_seq)
		FROM xrpl_publication_stream_indexes`).Scan(&rows, &minimum, &maximum); err != nil {
		return relationaldb.NewQueryError("persist_ledger_stream", "validate transaction indexes", err)
	}
	if rows != int64(count) {
		return relationaldb.NewDataError("persist_ledger_stream", "stream index count changed", relationaldb.ErrInvalidData)
	}
	if count == 0 {
		if !ledger.TransactionHash.IsZero() {
			return relationaldb.NewDataError("persist_ledger_stream", "empty stream has non-zero transaction hash", relationaldb.ErrInvalidData)
		}
		return nil
	}
	if ledger.TransactionHash.IsZero() {
		return relationaldb.NewDataError("persist_ledger_stream", "non-empty stream has zero transaction hash", relationaldb.ErrInvalidData)
	}
	if !minimum.Valid || !maximum.Valid || minimum.Int64 != 0 || maximum.Int64 != int64(count-1) {
		return relationaldb.NewDataError("persist_ledger_stream", "transaction indexes are not contiguous", relationaldb.ErrInvalidData)
	}
	return nil
}

func compareAccountID(a, b relationaldb.AccountID) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func publicationConflict(message string) error {
	return relationaldb.NewDataError("persist_ledger_stream", message, relationaldb.ErrCommitConflict)
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return relationaldb.NewTransactionError("persist_ledger_stream", "context canceled", err)
	}
	return nil
}
