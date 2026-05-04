package service

import (
	"context"
	"encoding/hex"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// persistLedger writes the ledger state to storage backends. The
// caller-supplied ctx is forwarded to every storage backend call so
// shutdown / request cancellation propagates through the persistence
// layer.
//
// Caller contract: chain-advance call sites must log and discard the
// returned error, mirroring rippled's LedgerMaster::setFullLedger ->
// pendSaveValidated which discards the bool return
// (rippled/src/xrpld/app/ledger/detail/LedgerMaster.cpp:831,972).
// Treating persistence failure as fatal would diverge from rippled
// and risk forks on transient storage issues.
//
// Atomicity boundaries:
//
//   - NodeStore is the durable ledger store; relational DB is a
//     supplementary index. NodeStore is persisted (and synced) FIRST,
//     so a relational failure leaves the canonical ledger durable and
//     the index can be rebuilt.
//   - Within NodeStore, state nodes are written before the header.
//     A mid-write failure leaves orphaned (unreferenced) state nodes
//     rather than a header pointing at missing state — readers see
//     the ledger as ABSENT, not CORRUPT.
//   - The per-tx relational writes (SaveTransaction +
//     SaveAccountTransaction) run inside a single WithTransaction
//     call, matching rippled's soci::transaction over the per-tx
//     INSERT loop in
//     rippled/src/xrpld/app/rdb/backend/detail/Node.cpp:272-349.
//     Caveat: on SQLite, SaveValidatedLedger writes the ledger row
//     on a separate ledger DB connection that is non-transactional
//     (see storage/relationaldb/sqlite/transaction_context.go), so
//     a tx-loop rollback can leave an already-written ledger row in
//     place. This split mirrors rippled's two-DB layout
//     (Node.cpp:264 uses ldgDB outside the txnDB transaction) and is
//     a backend limitation, not introduced here.
func (s *Service) persistLedger(ctx context.Context, l *ledger.Ledger) error {
	seq := l.Sequence()

	if s.nodeStore != nil {
		if err := s.persistToNodeStore(ctx, l, seq); err != nil {
			return err
		}
	}

	if s.relationalDB != nil {
		if err := s.persistToRelationalDB(ctx, l); err != nil {
			return err
		}
	}

	return nil
}

// persistToNodeStore writes ledger state to the nodestore
func (s *Service) persistToNodeStore(ctx context.Context, l *ledger.Ledger, seq uint32) error {
	// Collect nodes to store in batch
	var nodes []*nodestore.Node

	// Persist state map entries
	err := l.ForEach(func(key [32]byte, data []byte) bool {
		node := &nodestore.Node{
			Type:      nodestore.NodeAccount,
			Hash:      nodestore.Hash256(key),
			Data:      data,
			LedgerSeq: seq,
		}
		nodes = append(nodes, node)
		return true
	})
	if err != nil {
		return err
	}

	// Store nodes in batch for efficiency
	if len(nodes) > 0 {
		if err := s.nodeStore.StoreBatch(ctx, nodes); err != nil {
			return err
		}
	}

	// Persist ledger header
	headerData := l.SerializeHeader()
	headerNode := &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      nodestore.Hash256(l.Hash()),
		Data:      headerData,
		LedgerSeq: seq,
	}
	if err := s.nodeStore.Store(ctx, headerNode); err != nil {
		return err
	}

	// Single fsync once both state nodes and header are durable.
	// Sync is uninterruptible at the backend; ctx cancellation only
	// unblocks the caller (see DatabaseImpl.Sync).
	return s.nodeStore.Sync(ctx)
}

// persistToRelationalDB writes ledger metadata and transactions to the
// relational database inside a single transaction so the per-tx index
// entries either all commit or all roll back on cancel / DB error.
//
// WithTransaction is invoked directly on RepositoryManager, bypassing
// Manager.ExecuteInTransaction's retry layer. The persist call site
// in service.go logs and discards the error to match rippled's
// fail-soft pendSaveValidated; retrying inside the transactional
// scope would not help if the failure is the chain-advance ordering
// itself, and would lengthen the time the Service mutex is held.
func (s *Service) persistToRelationalDB(ctx context.Context, l *ledger.Ledger) error {
	h := l.Header()

	stateHash, _ := l.StateMapHash()
	txHash, _ := l.TxMapHash()

	ledgerInfo := &relationaldb.LedgerInfo{
		Hash:            relationaldb.Hash(l.Hash()),
		Sequence:        relationaldb.LedgerIndex(h.LedgerIndex),
		ParentHash:      relationaldb.Hash(h.ParentHash),
		AccountHash:     relationaldb.Hash(stateHash),
		TransactionHash: relationaldb.Hash(txHash),
		TotalCoins:      relationaldb.Amount(h.Drops),
		CloseTime:       h.CloseTime,
		ParentCloseTime: h.ParentCloseTime,
		CloseTimeRes:    int32(h.CloseTimeResolution),
		CloseFlags:      uint32(h.CloseFlags),
	}

	seq := relationaldb.LedgerIndex(l.Sequence())

	return s.relationalDB.WithTransaction(ctx, func(txCtx relationaldb.TransactionContext) error {
		if err := txCtx.Ledger().SaveValidatedLedger(ctx, ledgerInfo, true); err != nil {
			return err
		}

		var loopErr error
		_ = l.ForEachTransaction(func(txHashBytes [32]byte, txData []byte) bool {
			if err := ctx.Err(); err != nil {
				loopErr = err
				return false
			}

			txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(txData)
			if err != nil {
				// Bad blob is a data issue, not a DB issue —
				// skip this tx, keep the ledger persist alive.
				s.logger.Warn("failed to split tx+meta blob", "tx", hex.EncodeToString(txHashBytes[:8]), "error", err)
				return true
			}

			var accountID relationaldb.AccountID
			var destinationID relationaldb.AccountID

			txBlobHex := hex.EncodeToString(txBlob)
			if txJSON, decErr := binarycodec.Decode(txBlobHex); decErr == nil {
				if accountStr, ok := txJSON["Account"].(string); ok {
					if _, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(accountStr); err == nil && len(accountBytes) == 20 {
						copy(accountID[:], accountBytes)
					}
				}
				if destStr, ok := txJSON["Destination"].(string); ok {
					if _, destBytes, err := addresscodec.DecodeClassicAddressToAccountID(destStr); err == nil && len(destBytes) == 20 {
						copy(destinationID[:], destBytes)
					}
				}
			}

			var txnSeq uint32
			if len(metaBlob) > 0 {
				metaHex := hex.EncodeToString(metaBlob)
				if metaJSON, err := binarycodec.Decode(metaHex); err == nil {
					if v, ok := metaJSON["TransactionIndex"].(float64); ok {
						txnSeq = uint32(v)
					}
				}
			}

			txInfo := &relationaldb.TransactionInfo{
				Hash:      relationaldb.Hash(txHashBytes),
				LedgerSeq: seq,
				TxnSeq:    txnSeq,
				Status:    "validated",
				RawTxn:    txBlob,
				TxnMeta:   metaBlob,
				Account:   accountID,
			}

			// DB errors propagate so the whole ledger rolls back —
			// partial tx index is worse than a retried persist.
			if err := txCtx.Transaction().SaveTransaction(ctx, txInfo); err != nil {
				loopErr = err
				return false
			}

			if !accountID.IsZero() {
				if err := txCtx.AccountTransaction().SaveAccountTransaction(ctx, accountID, txInfo); err != nil {
					loopErr = err
					return false
				}
			}

			if !destinationID.IsZero() && destinationID != accountID {
				if err := txCtx.AccountTransaction().SaveAccountTransaction(ctx, destinationID, txInfo); err != nil {
					loopErr = err
					return false
				}
			}

			return true
		})

		return loopErr
	})
}
