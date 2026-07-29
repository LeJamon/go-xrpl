package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
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
	var token uint64
	if l.IsValidated() {
		token = s.beginValidatedPersistence(seq, l.Hash())
	}
	return s.persistValidatedLedgerAtToken(ctx, l, updateTip, token, false, nil)
}

func (s *Service) persistValidatedLedgerAtToken(
	ctx context.Context,
	l *ledger.Ledger,
	updateTip bool,
	token uint64,
	allowTipReplacement bool,
	canceled func() bool,
) error {
	seq := l.Sequence()
	var persistErr error

	if s.nodeStore != nil {
		if err := s.persistToNodeStore(ctx, l, seq); err != nil {
			persistErr = err
		}
	}
	if canceled != nil && canceled() {
		return nil
	}

	if s.relationalDB != nil {
		s.canonicalPersistMu.Lock()
		if err := s.persistToRelationalDB(ctx, l); err != nil {
			persistErr = errors.Join(persistErr, err)
		}
		if canceled != nil && canceled() {
			s.canonicalPersistMu.Unlock()
			return nil
		}
		if persistErr == nil && updateTip && s.nodeStore != nil {
			if err := s.persistValidatedTipLocked(ctx, l, allowTipReplacement); err != nil {
				persistErr = err
			}
		}
		s.canonicalPersistMu.Unlock()
	}

	if l.IsValidated() {
		s.recordValidatedPersistence(seq, token, persistErr == nil)
	}
	return persistErr
}

// persistJob is one unit of persistence work: a ledger to persist, or a
// barrier (nil ledger + done) that flushes the FIFO queue for callers that
// need persistence to be observable (tests, shutdown paths).
type persistJob struct {
	l               *ledger.Ledger
	done            chan struct{}
	validated       bool
	tipOnly         bool
	canceled        atomic.Bool
	updatesTip      atomic.Bool
	completionToken uint64
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
	seq := l.Sequence()
	s.persistMu.Lock()
	if validated {
		if existing := s.validatedPersistJobs[seq]; existing != nil {
			if existing.l != nil && existing.l.Hash() == l.Hash() {
				if updatesTip {
					existing.updatesTip.Store(true)
				}
				s.persistMu.Unlock()
				return
			}
			if !updatesTip && existing.updatesTip.Load() {
				s.persistMu.Unlock()
				return
			}
			existing.canceled.Store(true)
			delete(s.validatedPersistJobs, seq)
		}
		if s.hasCompleteLedger(l) && !updatesTip {
			s.persistMu.Unlock()
			return
		}
	}
	job := &persistJob{
		l:         l,
		validated: validated,
	}
	job.updatesTip.Store(updatesTip)
	if validated {
		if s.hasCompleteLedger(l) {
			job.tipOnly = true
		} else if l.IsValidated() {
			job.completionToken = s.beginValidatedPersistence(seq, l.Hash())
		}
		s.validatedPersistJobs[seq] = job
	}
	if !s.persistStarted {
		s.persistMu.Unlock()
		s.runPersistJob(job)
		return
	}
	if s.persistStopping {
		if validated {
			job.canceled.Store(true)
			delete(s.validatedPersistJobs, seq)
		}
		s.persistMu.Unlock()
		if validated && !job.tipOnly {
			s.invalidateCompleteLedger(seq)
		}
		s.logger.Warn("persist skipped: service stopping", "seq", seq)
		return
	}
	s.persistQueue = append(s.persistQueue, job)
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
	s.persistQueue = append(s.persistQueue, &persistJob{done: done})
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

	s.ledgerEventMu.Lock()
	if s.ledgerEventStarted && !s.ledgerEventStopping {
		s.ledgerEventStopping = true
		select {
		case s.ledgerEventWake <- struct{}{}:
		default:
		}
	}
	s.ledgerEventMu.Unlock()
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
			s.persistQueue[0] = nil
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

func (s *Service) runPersistJob(job *persistJob) {
	if job == nil {
		return
	}
	if job.l != nil {
		updateTip := job.updatesTip.Load()
		var err error
		if !job.canceled.Load() && job.tipOnly {
			if s.nodeStore != nil && s.relationalDB != nil {
				err = s.persistValidatedTipJob(
					context.Background(),
					job.l,
					true,
					job.canceled.Load,
				)
			}
		} else if !job.canceled.Load() {
			err = s.persistLedgerJob(
				context.Background(),
				job.l,
				job.validated,
				updateTip,
				job.completionToken,
				true,
				job.canceled.Load,
			)
		}
		s.persistMu.Lock()
		lateTip := job.validated && !job.tipOnly && !job.canceled.Load() &&
			!updateTip && job.updatesTip.Load()
		if !lateTip && job.validated && s.validatedPersistJobs[job.l.Sequence()] == job {
			delete(s.validatedPersistJobs, job.l.Sequence())
		}
		s.persistMu.Unlock()
		if err == nil && lateTip && s.nodeStore != nil && s.relationalDB != nil {
			err = s.persistValidatedTipJob(
				context.Background(),
				job.l,
				true,
				job.canceled.Load,
			)
		}
		if lateTip {
			s.persistMu.Lock()
			if s.validatedPersistJobs[job.l.Sequence()] == job {
				delete(s.validatedPersistJobs, job.l.Sequence())
			}
			s.persistMu.Unlock()
		}
		if err != nil {
			s.logger.Error("failed to persist ledger; chain advance continues",
				"seq", job.l.Sequence(), "err", err)
		}
	}
	if job.done != nil {
		close(job.done)
	}
}

func (s *Service) persistLedgerJob(
	ctx context.Context,
	l *ledger.Ledger,
	validated, updatesTip bool,
	completionToken uint64,
	allowTipReplacement bool,
	canceled func() bool,
) error {
	if validated {
		return s.persistValidatedLedgerAtToken(
			ctx,
			l,
			updatesTip,
			completionToken,
			allowTipReplacement,
			canceled,
		)
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
			if family, ok := s.shamapFamily.(*backend.NodeStore); ok {
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
	return s.persistValidatedTipJob(ctx, l, false, nil)
}

func (s *Service) persistValidatedTipJob(
	ctx context.Context,
	l *ledger.Ledger,
	allowSameSequenceReplacement bool,
	canceled func() bool,
) error {
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()
	if canceled != nil && canceled() {
		return nil
	}
	return s.persistValidatedTipLocked(ctx, l, allowSameSequenceReplacement)
}

func (s *Service) persistValidatedTipLocked(
	ctx context.Context,
	l *ledger.Ledger,
	allowSameSequenceReplacement bool,
) error {
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
			if !allowSameSequenceReplacement {
				return fmt.Errorf("validated ledger tip %d conflicts with persisted hash", l.Sequence())
			}
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

func (s *Service) invalidatePersistedValidatedTip(start, end uint32) {
	s.invalidatePersistedValidatedTipMatching(start, end, nil)
}

func (s *Service) invalidatePersistedValidatedTipHash(seq uint32, hash [32]byte) {
	s.invalidatePersistedValidatedTipMatching(seq, seq, &hash)
}

func (s *Service) invalidatePersistedValidatedTipMatching(start, end uint32, expectedHash *[32]byte) {
	if s.nodeStore == nil || start > end {
		return
	}
	s.canonicalPersistMu.Lock()
	defer s.canonicalPersistMu.Unlock()

	ctx := context.Background()
	current, err := s.nodeStore.Fetch(ctx, validatedTipKey)
	if err != nil {
		s.logger.Error("failed to inspect validated ledger tip during invalidation", "err", err)
		return
	}
	if current == nil || current.Type != nodestore.NodeLedger || len(current.Data) != 32 ||
		current.LedgerSeq < start || current.LedgerSeq > end {
		return
	}
	if expectedHash != nil && !bytes.Equal(current.Data, expectedHash[:]) {
		return
	}
	if err := s.nodeStore.Store(ctx, &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      validatedTipKey,
		Data:      make([]byte, 32),
		LedgerSeq: 0,
	}); err != nil {
		s.logger.Error("failed to invalidate validated ledger tip", "seq", current.LedgerSeq, "err", err)
		return
	}
	if err := s.nodeStore.Sync(ctx); err != nil {
		s.logger.Error("failed to sync invalidated validated ledger tip", "seq", current.LedgerSeq, "err", err)
	}
}

const (
	refreshHealthCheckInterval = 1000
	refreshHealthCheckPeriod   = 10 * time.Millisecond
)

// RefreshValidatedState preserves the complete live state tree before online
// deletion retires older node-store records. Rotating stores promote archive
// reads into their writable generation; legacy stores re-stamp records in place.
// It returns the validated sequence whose state was preserved.
func (s *Service) RefreshValidatedState(
	ctx context.Context,
	minimumSeq uint32,
	checkpoint func(time.Duration) error,
) (uint32, error) {
	s.mu.RLock()
	validated := s.validatedLedger
	s.mu.RUnlock()
	if validated == nil || validated.Sequence() < minimumSeq {
		return 0, fmt.Errorf("validated ledger is behind rotation target %d", minimumSeq)
	}
	if err := s.flushPersists(ctx); err != nil {
		return 0, err
	}
	seq := validated.Sequence()

	if generations, ok := s.nodeStore.(nodestore.GenerationDatabase); ok {
		skipRefresh, err := generations.CanRotateWithoutRefresh(ctx)
		if err != nil {
			return 0, err
		}
		if skipRefresh {
			if err := s.nodeStore.Sync(ctx); err != nil {
				return 0, err
			}
			return seq, nil
		}
		root, err := validated.StateMapHash()
		if err != nil {
			return 0, err
		}
		visited := 0
		checkpointStarted := time.Now()
		err = s.walkStoredSHAMapWithFetch(
			ctx,
			root,
			shamap.TypeState,
			generations.FetchForPromotion,
			func(_ [32]byte, _ *nodestore.Node) error {
				visited++
				work := time.Since(checkpointStarted)
				if checkpoint != nil &&
					(visited%refreshHealthCheckInterval == 0 || work >= refreshHealthCheckPeriod) {
					if err := checkpoint(work); err != nil {
						return err
					}
					checkpointStarted = time.Now()
				}
				return nil
			},
		)
		if err != nil {
			return 0, err
		}
		if err := s.nodeStore.Sync(ctx); err != nil {
			return 0, err
		}
		return seq, nil
	}

	root, err := validated.StateMapHash()
	if err != nil {
		return 0, err
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

	visited := 0
	checkpointStarted := time.Now()
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
		visited++
		work := time.Since(checkpointStarted)
		if checkpoint != nil &&
			(visited%refreshHealthCheckInterval == 0 || work >= refreshHealthCheckPeriod) {
			if err := checkpoint(work); err != nil {
				return err
			}
			checkpointStarted = time.Now()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := flush(); err != nil {
		return 0, err
	}
	if err := s.nodeStore.Sync(ctx); err != nil {
		return 0, err
	}
	return seq, nil
}

// persistToRelationalDB materializes a validated ledger and all of its
// transaction indexes before handing the immutable unit to the relational
// backend. The backend owns commit ordering and retry safety.
func (s *Service) persistToRelationalDB(ctx context.Context, l *ledger.Ledger) error {
	h := l.Header()

	stateHash, err := l.StateMapHash()
	if err != nil {
		return fmt.Errorf("compute state map hash: %w", err)
	}
	if stateHash == ([32]byte{}) {
		return errors.New("compute state map hash: empty state root")
	}
	if stateHash != h.AccountHash {
		return fmt.Errorf("state map hash %x does not match ledger header %x", stateHash, h.AccountHash)
	}
	txHash, err := l.TxMapHash()
	if err != nil {
		return fmt.Errorf("compute transaction map hash: %w", err)
	}
	if txHash != h.TxHash {
		return fmt.Errorf("transaction map hash %x does not match ledger header %x", txHash, h.TxHash)
	}

	ledgerInfo := relationaldb.LedgerInfo{
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

	indexed := make([]relationaldb.IndexedTransaction, 0)
	var loopErr error
	err = l.ForEachTransactionContext(ctx, func(txHashBytes [32]byte, txData []byte) bool {
		txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(txData)
		if err != nil {
			loopErr = fmt.Errorf("split transaction %x: %w", txHashBytes[:8], err)
			return false
		}

		var accountID relationaldb.AccountID
		parsedTransaction, err := tx.ParseFromBinary(txBlob)
		if err != nil {
			loopErr = fmt.Errorf("decode transaction %x: %w", txHashBytes[:8], err)
			return false
		}
		computedHash, err := tx.ComputeTransactionHash(parsedTransaction)
		if err != nil {
			loopErr = fmt.Errorf("hash transaction %x: %w", txHashBytes[:8], err)
			return false
		}
		if computedHash != txHashBytes {
			loopErr = fmt.Errorf("transaction hash %x does not match map key %x", computedHash, txHashBytes)
			return false
		}
		account := parsedTransaction.GetCommon().Account
		if account != "" {
			_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
			if err != nil || len(accountBytes) != len(accountID) {
				loopErr = fmt.Errorf("decode transaction %x Account: %w", txHashBytes[:8], errors.Join(err, relationaldb.ErrInvalidData))
				return false
			}
			copy(accountID[:], accountBytes)
		}

		affected := map[relationaldb.AccountID]struct{}{}

		txnSeq, ok := tx.TransactionIndexFromMetadata(metaBlob)
		if !ok {
			loopErr = fmt.Errorf("decode transaction metadata %x: missing or invalid TransactionIndex", txHashBytes[:8])
			return false
		}
		metaJSON, err := binarycodec.Decode(hex.EncodeToString(metaBlob))
		if err != nil {
			loopErr = fmt.Errorf("decode transaction metadata %x: %w", txHashBytes[:8], err)
			return false
		}
		if result, ok := metaJSON["TransactionResult"].(string); !ok || result == "" {
			loopErr = fmt.Errorf("decode transaction metadata %x: missing or invalid TransactionResult", txHashBytes[:8])
			return false
		}
		if err := addMetaAffectedAccounts(metaJSON, affected); err != nil {
			loopErr = fmt.Errorf("decode transaction metadata %x: %w", txHashBytes[:8], err)
			return false
		}

		indexed = append(indexed, relationaldb.IndexedTransaction{
			Transaction: relationaldb.TransactionInfo{
				Hash:      relationaldb.Hash(txHashBytes),
				LedgerSeq: seq,
				TxnSeq:    txnSeq,
				Status:    "validated",
				RawTxn:    txBlob,
				TxnMeta:   metaBlob,
				Account:   accountID,
			},
			Accounts: sortedAccountIDs(affected),
		})

		return true
	})
	if err != nil {
		return fmt.Errorf("iterate transaction map: %w", err)
	}
	if loopErr != nil {
		return loopErr
	}
	return s.relationalDB.PersistValidatedLedger(ctx, relationaldb.ValidatedLedger{
		Ledger:       ledgerInfo,
		Transactions: indexed,
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
func addMetaAffectedAccounts(metaJSON map[string]any, into map[relationaldb.AccountID]struct{}) error {
	rawNodes, exists := metaJSON["AffectedNodes"]
	if !exists {
		return errors.New("missing AffectedNodes")
	}
	nodes, ok := rawNodes.([]any)
	if !ok {
		return errors.New("AffectedNodes is not an array")
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
	addMPTIssuer := func(hexID string) error {
		raw, err := hex.DecodeString(hexID)
		if err != nil || len(raw) != 24 {
			return errors.New("invalid MPTokenIssuanceID")
		}
		var id relationaldb.AccountID
		copy(id[:], raw[4:])
		if !id.IsZero() {
			into[id] = struct{}{}
		}
		return nil
	}
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			return errors.New("affected node is not an object")
		}
		found := false
		for _, wrapper := range []string{"CreatedNode", "ModifiedNode", "DeletedNode"} {
			inner, exists := node[wrapper]
			if !exists {
				continue
			}
			found = true
			im, ok := inner.(map[string]any)
			if !ok {
				return fmt.Errorf("%s is not an object", wrapper)
			}
			fieldsKey := "FinalFields"
			if wrapper == "CreatedNode" {
				fieldsKey = "NewFields"
			}
			rawFields, exists := im[fieldsKey]
			if !exists {
				continue
			}
			fields, ok := rawFields.(map[string]any)
			if !ok {
				return fmt.Errorf("%s %s is not an object", wrapper, fieldsKey)
			}
			for name, val := range fields {
				switch v := val.(type) {
				case string:
					if name == "MPTokenIssuanceID" {
						if err := addMPTIssuer(v); err != nil {
							return err
						}
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
			break
		}
		if !found {
			return errors.New("affected node has no recognized node type")
		}
	}
	return nil
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
