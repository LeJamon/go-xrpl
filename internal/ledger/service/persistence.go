package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

var validatedTipKey = nodestore.Hash256(sha512half.Sum([]byte("go-xrpl validated ledger tip")))

// persistLedger stores SHAMap deltas and the ledger header before updating the
// relational index. Callers log failures without stopping chain advancement.
func (s *Service) persistLedger(ctx context.Context, l *ledger.Ledger) error {
	return s.persistValidatedLedger(ctx, l, true)
}

func (s *Service) persistValidatedLedger(ctx context.Context, l *ledger.Ledger, updateTip bool) error {
	seq := l.Sequence()
	var persistErr error

	if s.nodeStore != nil {
		if err := s.persistToNodeStore(ctx, l, seq); err != nil {
			persistErr = err
		}
	}

	if s.relationalDB != nil {
		if err := s.persistToRelationalDB(ctx, l); err != nil {
			persistErr = errors.Join(persistErr, err)
		}
		if persistErr == nil && updateTip && s.nodeStore != nil {
			if err := s.persistValidatedTip(ctx, l); err != nil {
				persistErr = err
			}
		}
	}

	return persistErr
}

// persistJob is one unit of persistence work: a ledger to persist, or a
// barrier (nil ledger + done) that flushes the FIFO queue for callers that
// need persistence to be observable (tests, shutdown paths).
type persistJob struct {
	l          *ledger.Ledger
	done       chan struct{}
	validated  bool
	updatesTip bool
}

func (s *Service) enqueuePersist(l *ledger.Ledger) {
	s.enqueueLedgerPersist(l, true, true)
}

func (s *Service) enqueueValidatedHistoryPersist(l *ledger.Ledger) {
	s.enqueueLedgerPersist(l, true, false)
}

func (s *Service) enqueueNodePersist(l *ledger.Ledger) {
	s.enqueueLedgerPersist(l, false, false)
}

func (s *Service) enqueueLedgerPersist(l *ledger.Ledger, validated, updatesTip bool) {
	if l == nil {
		return
	}
	s.persistMu.Lock()
	if !s.persistStarted {
		s.persistMu.Unlock()
		if err := s.persistLedgerJob(context.Background(), l, validated, updatesTip); err != nil {
			s.logger.Error("failed to persist ledger inline", "seq", l.Sequence(), "err", err)
		}
		return
	}
	if s.persistStopping {
		s.persistMu.Unlock()
		s.logger.Warn("persist skipped: service stopping", "seq", l.Sequence())
		return
	}
	s.persistQueue = append(s.persistQueue, persistJob{l: l, validated: validated, updatesTip: updatesTip})
	s.signalPersistLocked()
	s.persistMu.Unlock()
}

func (s *Service) signalPersistLocked() {
	select {
	case s.persistWake <- struct{}{}:
	default:
	}
}

func (s *Service) FlushPersists() {
	_ = s.flushPersists(context.Background())
}

func (s *Service) flushPersists(ctx context.Context) error {
	done := make(chan struct{})
	s.persistMu.Lock()
	if !s.persistStarted || s.persistStopping {
		s.persistMu.Unlock()
		return nil
	}
	s.persistQueue = append(s.persistQueue, persistJob{done: done})
	s.signalPersistLocked()
	s.persistMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop drains the persistence queue and joins the worker, guaranteeing every
// ledger persist enqueued before Stop is durable before the caller
// closes the underlying node/relational stores. Idempotent and safe on a
// never-started Service. Must be called before those stores are closed.
func (s *Service) Stop() {
	s.stopNodeStoreSweeper()

	s.persistMu.Lock()
	persistWasStarted := s.persistStarted
	waitPersist := s.persistStarted && !s.persistStopping
	if waitPersist {
		s.persistStopping = true
		s.signalPersistLocked()
	}
	s.persistMu.Unlock()

	s.mu.Lock()
	var eventQuit chan struct{}
	if !s.ledgerEventStopped {
		s.ledgerEventStopped = true
		eventQuit = s.ledgerEventQuit
	}
	s.mu.Unlock()

	if eventQuit != nil {
		close(eventQuit)
	}
	if persistWasStarted {
		s.persistWG.Wait()
	}
	s.ledgerEventWG.Wait()
}

func (s *Service) runPersistWorker() {
	defer s.persistWG.Done()
	for {
		s.persistMu.Lock()
		if len(s.persistQueue) > 0 {
			job := s.persistQueue[0]
			s.persistQueue[0] = persistJob{}
			s.persistQueue = s.persistQueue[1:]
			s.persistMu.Unlock()
			s.runPersistJob(job)
			continue
		}
		stopping := s.persistStopping
		s.persistMu.Unlock()
		if stopping {
			return
		}
		<-s.persistWake
	}
}

func (s *Service) runPersistJob(job persistJob) {
	if job.l != nil {
		if err := s.persistLedgerJob(context.Background(), job.l, job.validated, job.updatesTip); err != nil {
			s.logger.Error("failed to persist ledger; chain advance continues",
				"seq", job.l.Sequence(), "err", err)
		}
	}
	if job.done != nil {
		close(job.done)
	}
}

func (s *Service) persistLedgerJob(ctx context.Context, l *ledger.Ledger, validated, updatesTip bool) error {
	if validated {
		return s.persistValidatedLedger(ctx, l, updatesTip)
	}
	if s.nodeStore == nil {
		return nil
	}
	return s.persistToNodeStore(ctx, l, l.Sequence())
}

// persistToNodeStore writes state and transaction deltas before the header.
func (s *Service) persistToNodeStore(ctx context.Context, l *ledger.Ledger, seq uint32) error {
	store := func(nodeType nodestore.NodeType) func([]shamap.FlushEntry) error {
		return func(entries []shamap.FlushEntry) error {
			if family, ok := s.shamapFamily.(*shamap.NodeStoreFamily); ok {
				return family.StoreBatch(ctx, entries)
			}
			const batchSize = 4096
			for start := 0; start < len(entries); start += batchSize {
				end := min(start+batchSize, len(entries))
				nodes := make([]*nodestore.Node, end-start)
				for i, entry := range entries[start:end] {
					nodes[i] = &nodestore.Node{
						Type:      nodeType,
						Hash:      nodestore.Hash256(entry.Hash),
						Data:      entry.Data,
						LedgerSeq: entry.LedgerSeq,
					}
				}
				if err := s.nodeStore.StoreBatch(ctx, nodes); err != nil {
					return err
				}
			}
			return nil
		}
	}

	if err := l.StoreStateDirty(store(nodestore.NodeAccount)); err != nil {
		return fmt.Errorf("store state delta for ledger %d: %w", seq, err)
	}
	if err := l.StoreTransactionDirty(store(nodestore.NodeTransaction)); err != nil {
		return fmt.Errorf("store transaction delta for ledger %d: %w", seq, err)
	}

	headerData := l.SerializeHeader()
	headerNode := &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      nodestore.Hash256(l.Hash()),
		Data:      headerData,
		LedgerSeq: seq,
	}
	if err := s.nodeStore.Store(ctx, headerNode); err != nil {
		return fmt.Errorf("store ledger %d header: %w", seq, err)
	}

	// Single fsync once both state nodes and header are durable.
	// Sync is uninterruptible at the backend; ctx cancellation only
	// unblocks the caller (see KVDatabaseImpl.Sync).
	if err := s.nodeStore.Sync(ctx); err != nil {
		return fmt.Errorf("sync ledger %d: %w", seq, err)
	}
	return nil
}

func (s *Service) persistValidatedTip(ctx context.Context, l *ledger.Ledger) error {
	s.validatedTipMu.Lock()
	defer s.validatedTipMu.Unlock()
	hash := l.Hash()
	current, err := s.nodeStore.Fetch(ctx, validatedTipKey)
	if err != nil {
		return fmt.Errorf("fetch validated ledger tip: %w", err)
	}
	if current != nil && current.Type == nodestore.NodeLedger && len(current.Data) == 32 {
		switch {
		case current.LedgerSeq > l.Sequence():
			return nil
		case current.LedgerSeq == l.Sequence():
			if bytes.Equal(current.Data, hash[:]) {
				return nil
			}
			return fmt.Errorf("validated ledger tip %d conflicts with persisted hash", l.Sequence())
		}
	}
	if err := s.nodeStore.Store(ctx, &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      validatedTipKey,
		Data:      append([]byte(nil), hash[:]...),
		LedgerSeq: l.Sequence(),
	}); err != nil {
		return fmt.Errorf("store validated ledger tip %d: %w", l.Sequence(), err)
	}
	if err := s.nodeStore.Sync(ctx); err != nil {
		return fmt.Errorf("sync validated ledger tip %d: %w", l.Sequence(), err)
	}
	return nil
}

// RefreshValidatedState re-stamps the complete live state tree before online
// deletion removes older node-store records.
func (s *Service) RefreshValidatedState(ctx context.Context, minimumSeq uint32) error {
	if err := s.flushPersists(ctx); err != nil {
		return err
	}

	s.mu.RLock()
	validated := s.validatedLedger
	s.mu.RUnlock()
	if validated == nil || validated.Sequence() < minimumSeq {
		return fmt.Errorf("validated ledger is behind rotation target %d", minimumSeq)
	}
	seq := validated.Sequence()
	root, err := validated.StateMapHash()
	if err != nil {
		return err
	}

	const batchSize = 4096
	batch := make([]*nodestore.Node, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.nodeStore.StoreBatch(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	err = s.walkStoredSHAMap(ctx, root, shamap.TypeState, func(hash [32]byte, node *nodestore.Node) error {
		batch = append(batch, &nodestore.Node{
			Type:      nodestore.NodeAccount,
			Hash:      nodestore.Hash256(hash),
			Data:      node.Data,
			LedgerSeq: seq,
		})
		if len(batch) == batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
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
		if err := txCtx.Ledger().SaveValidatedLedger(ctx, ledgerInfo); err != nil {
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

			txnSeq := invalidTransactionIndex
			if len(metaBlob) > 0 {
				if txIndex, ok := tx.TransactionIndexFromMetadata(metaBlob); ok {
					txnSeq = txIndex
				}
				metaHex := hex.EncodeToString(metaBlob)
				if metaJSON, err := binarycodec.Decode(metaHex); err == nil {
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
