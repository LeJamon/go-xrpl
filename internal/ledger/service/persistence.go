package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"sort"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
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
// Both backends are best-effort at the rippled-equivalent boundary:
//
//   - NodeStore failures are logged and swallowed inside
//     persistToNodeStore, mirroring rippled's
//     NodeStore::Database::store / ::sync void returns
//     (rippled/src/xrpld/nodestore/detail/DatabaseNodeImp.h:109-124).
//     A nodestore failure must NOT short-circuit the relational
//     persist — rippled's saveValidatedLedger calls store(...) and
//     unconditionally proceeds to the SQL writes
//     (rippled/src/xrpld/app/rdb/backend/detail/Node.cpp:228-229).
//   - Relational failures bubble up so the call site can log them,
//     but the call site discards the error (chain advance continues).
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
		s.persistToNodeStore(ctx, l, seq)
	}

	if s.relationalDB != nil {
		if err := s.persistToRelationalDB(ctx, l); err != nil {
			return err
		}
	}

	return nil
}

// persistJob is one unit of persistence work: a ledger to persist, or a
// barrier (nil ledger + done) that flushes the FIFO queue for callers that
// need persistence to be observable (tests, shutdown paths).
type persistJob struct {
	l    *ledger.Ledger
	done chan struct{}
}

// enqueuePersist hands a closed/adopted ledger to the persistence worker.
// Persistence walks the ENTIRE state map (seconds at 15k tx/ledger); running it
// inline under s.mu froze consensus ticks, RPC, and inbound dispatch. rippled
// runs the equivalent pendSaveValidated on its job queue. Best-effort: a full
// queue drops with a loud log and the chain advances (the ledger stays servable
// from the in-memory history window).
func (s *Service) enqueuePersist(l *ledger.Ledger) {
	if l == nil {
		return
	}
	if s.persistCh == nil {
		// Not started: persist inline.
		if err := s.persistLedger(context.Background(), l); err != nil {
			s.logger.Error("failed to persist ledger inline", "seq", l.Sequence(), "err", err)
		}
		return
	}
	select {
	case s.persistCh <- persistJob{l: l}:
	default:
		s.logger.Error("persist queue full — dropping ledger persist; chain advance continues",
			"seq", l.Sequence(), "depth", cap(s.persistCh))
	}
}

// FlushPersists blocks until every ledger enqueued before the call has been
// persisted. No-op when the worker isn't running.
func (s *Service) FlushPersists() {
	if s.persistCh == nil {
		return
	}
	done := make(chan struct{})
	s.persistCh <- persistJob{done: done}
	<-done
}

// runPersistWorker drains the persist queue in FIFO order, keeping
// nodestore/relational writes ordered by enqueue. Runs for the process
// lifetime.
func (s *Service) runPersistWorker() {
	for job := range s.persistCh {
		if job.l != nil {
			if err := s.persistLedger(context.Background(), job.l); err != nil {
				s.logger.Error("failed to persist ledger; chain advance continues",
					"seq", job.l.Sequence(), "err", err)
			}
		}
		if job.done != nil {
			close(job.done)
		}
	}
}

// persistToNodeStore writes ledger state to the nodestore.
//
// Mirrors rippled's NodeStore::Database::store and ::sync, which
// return void: backend errors are logged and swallowed, never
// propagated to the chain-advance code
// (rippled/src/xrpld/nodestore/detail/DatabaseNodeImp.h:109-124).
// Returning errors here would diverge from rippled and risk forks if
// any caller forgot to log-and-discard.
func (s *Service) persistToNodeStore(ctx context.Context, l *ledger.Ledger, seq uint32) {
	var nodes []*nodestore.Node

	iterErr := l.ForEach(func(key [32]byte, data []byte) bool {
		node := &nodestore.Node{
			Type:      nodestore.NodeAccount,
			Hash:      nodestore.Hash256(key),
			Data:      data,
			LedgerSeq: seq,
		}
		nodes = append(nodes, node)
		return true
	})
	if iterErr != nil {
		s.logger.Error("nodestore persist: state map iteration failed; ledger state not written",
			"seq", seq, "err", iterErr)
		return
	}

	if len(nodes) > 0 {
		if err := s.nodeStore.StoreBatch(ctx, nodes); err != nil {
			s.logger.Error("nodestore persist: StoreBatch failed; chain advance continues",
				"seq", seq, "nodes", len(nodes), "err", err)
			return
		}
	}

	headerData := l.SerializeHeader()
	headerNode := &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      nodestore.Hash256(l.Hash()),
		Data:      headerData,
		LedgerSeq: seq,
	}
	if err := s.nodeStore.Store(ctx, headerNode); err != nil {
		s.logger.Error("nodestore persist: header Store failed; chain advance continues",
			"seq", seq, "err", err)
		return
	}

	// Single fsync once both state nodes and header are durable.
	// Sync is uninterruptible at the backend; ctx cancellation only
	// unblocks the caller (see KVDatabaseImpl.Sync).
	if err := s.nodeStore.Sync(ctx); err != nil {
		s.logger.Error("nodestore persist: Sync failed; chain advance continues",
			"seq", seq, "err", err)
	}
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

			// account_tx must be queryable by every account the transaction
			// affected — not just Account/Destination but offer counterparties,
			// trust-line issuers, and so on — mirroring rippled's
			// TxMeta::getAffectedAccounts (AcceptedLedgerTx.cpp:35).
			affected := map[relationaldb.AccountID]struct{}{}
			if !accountID.IsZero() {
				affected[accountID] = struct{}{}
			}
			if !destinationID.IsZero() {
				affected[destinationID] = struct{}{}
			}

			var txnSeq uint32
			if len(metaBlob) > 0 {
				metaHex := hex.EncodeToString(metaBlob)
				if metaJSON, err := binarycodec.Decode(metaHex); err == nil {
					if v, ok := metaJSON["TransactionIndex"].(float64); ok {
						txnSeq = uint32(v)
					}
					addMetaAffectedAccounts(metaJSON, affected)
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

			for _, acc := range sortedAccountIDs(affected) {
				if err := txCtx.AccountTransaction().SaveAccountTransaction(ctx, acc, txInfo); err != nil {
					loopErr = err
					return false
				}
			}

			return true
		})

		return loopErr
	})
}

// addMetaAffectedAccounts collects every account a transaction's metadata
// affected into `into`, mirroring rippled's TxMeta::getAffectedAccounts: for
// each affected node it reads NewFields (CreatedNode) or FinalFields
// (Modified/DeletedNode) and adds every account-typed field, the issuer of any
// LowLimit/HighLimit/TakerPays/TakerGets amount, and the issuer encoded in any
// MPTokenIssuanceID. In decoded metadata JSON account fields are plain
// classic-address strings and those amounts are objects, so a
// string-decodes-as-address test isolates the account fields.
func addMetaAffectedAccounts(metaJSON map[string]any, into map[relationaldb.AccountID]struct{}) {
	nodes, ok := metaJSON["AffectedNodes"].([]any)
	if !ok {
		return
	}
	addAddr := func(s string) {
		if _, b, err := addresscodec.DecodeClassicAddressToAccountID(s); err == nil && len(b) == 20 {
			var id relationaldb.AccountID
			copy(id[:], b)
			if !id.IsZero() {
				into[id] = struct{}{}
			}
		}
	}
	// An MPTokenIssuanceID is the 24-byte (4-byte sequence ++ 20-byte issuer)
	// hex of an MPT issuance; index its issuer so MPToken activity is queryable
	// by the issuing account.
	addMPTIssuer := func(hexID string) {
		raw, err := hex.DecodeString(hexID)
		if err != nil || len(raw) != 24 {
			return
		}
		var id relationaldb.AccountID
		copy(id[:], raw[4:])
		if !id.IsZero() {
			into[id] = struct{}{}
		}
	}
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		for wrapper, inner := range node {
			im, ok := inner.(map[string]any)
			if !ok {
				continue
			}
			fieldsKey := "FinalFields"
			if wrapper == "CreatedNode" {
				fieldsKey = "NewFields"
			}
			fields, ok := im[fieldsKey].(map[string]any)
			if !ok {
				continue
			}
			for name, val := range fields {
				switch v := val.(type) {
				case string:
					if name == "MPTokenIssuanceID" {
						addMPTIssuer(v)
					} else {
						addAddr(v)
					}
				case map[string]any:
					switch name {
					case "LowLimit", "HighLimit", "TakerPays", "TakerGets":
						if iss, ok := v["issuer"].(string); ok {
							addAddr(iss)
						}
					}
				}
			}
		}
	}
}

// sortedAccountIDs returns the set's account IDs in ascending byte order so
// account_tx rows are persisted deterministically.
func sortedAccountIDs(set map[relationaldb.AccountID]struct{}) []relationaldb.AccountID {
	out := make([]relationaldb.AccountID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}
