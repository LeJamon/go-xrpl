package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

var errStateRetentionChanged = errors.New("live state changed after retention preparation")

const (
	stateRetentionCatchupNodes = 4096
	stateRetentionCatchupTime  = 250 * time.Millisecond
)

type retainedStateRoot struct {
	hash [32]byte
	kind shamap.Type
}

// PrepareStateRetention preserves owned trees, then supplies a short admission
// guard to run under the node store's destructive-mutation lock. Detached work
// must hold a durable snapshot until its result is published into these owners.
func (s *Service) PrepareStateRetention(
	ctx context.Context,
	minimumSeq uint32,
	checkpoint func(context.Context, time.Duration) error,
) (uint32, func(context.Context) (func(), error), error) {
	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return 0, nil, errors.New("state retention requires durable snapshots")
	}
	fingerprint, release, err := durable.AcquireDurableSnapshot(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer release()
	if err := s.flushPersists(ctx); err != nil {
		return 0, nil, err
	}
	preserved := make(map[retainedStateRoot]struct{})
	previous := make(map[shamap.Type][32]byte)
	var sequence uint32
	for pass := range 8 {
		unlock, err := s.lockStateRetention(ctx)
		if err != nil {
			return 0, nil, err
		}
		if s.validatedLedger == nil || s.validatedLedger.Sequence() < minimumSeq {
			unlock()
			return 0, nil, fmt.Errorf("validated ledger is behind rotation target %d", minimumSeq)
		}
		sequence = s.validatedLedger.Sequence()
		roots, err := s.stateRetentionRootsLocked(ctx, true)
		openRoots, openErr := s.openStateRetentionRootsLocked()
		if err == nil {
			err = openErr
		}
		unlock()
		if err != nil {
			return 0, nil, err
		}
		changed := false
		for _, root := range roots {
			if _, found := preserved[root]; found {
				continue
			}
			if err := s.preserveStateDifference(ctx, root, previous[root.kind], sequence, checkpoint, 0); err != nil {
				s.recordStateRetentionFailure(ctx, root, err)
				return 0, nil, fmt.Errorf("preserve live %s tree %x: %w", root.kind, root.hash, err)
			}
			preserved[root] = struct{}{}
			previous[root.kind] = root.hash
			changed = true
		}
		if err := s.nodeStore.Sync(ctx); err != nil {
			return 0, nil, err
		}
		if !changed || pass == 7 {
			return sequence, s.stateRetentionGuard(fingerprint, sequence, preserved, openRoots), nil
		}
		for _, root := range openRoots {
			previous[root.kind] = root.hash
		}
	}
	return 0, nil, errStateRetentionChanged
}

func (s *Service) recordStateRetentionFailure(ctx context.Context, root retainedStateRoot, cause error) {
	s.mu.RLock()
	validated := s.validatedLedger
	if validated == nil {
		s.mu.RUnlock()
		return
	}
	h := validated.Header()
	s.mu.RUnlock()
	if root.kind == shamap.TypeState && root.hash == h.AccountHash ||
		root.kind == shamap.TypeTransaction && root.hash == h.TxHash {
		s.recordStateBaseRecertificationFailure(ctx, h, root.kind, cause)
	}
}

func (s *Service) stateRetentionGuard(fingerprint [32]byte, sequence uint32, preserved map[retainedStateRoot]struct{}, preparedOpen []retainedStateRoot) func(context.Context) (func(), error) {
	return func(ctx context.Context) (func(), error) {
		durable := s.nodeStore.(nodestore.DurableSnapshotDatabase)
		current, err := durable.DurableFingerprint(ctx)
		if err != nil {
			return nil, err
		}
		if current != fingerprint {
			return nil, errStateRetentionChanged
		}
		unlock, err := s.lockStateRetention(ctx)
		if err != nil {
			return nil, err
		}
		admitted := false
		defer func() {
			if !admitted {
				unlock()
			}
		}()
		roots, err := s.stateRetentionRootsLocked(ctx, false)
		if err != nil {
			return nil, err
		}
		for _, l := range s.stateRetentionLedgersLocked() {
			if l.IsOpen() {
				continue
			}
			state, err := l.StateMapHash()
			if err != nil {
				return nil, err
			}
			txs, err := l.TxMapHash()
			if err != nil {
				return nil, err
			}
			for _, root := range []retainedStateRoot{{state, shamap.TypeState}, {txs, shamap.TypeTransaction}} {
				if root.hash == ([32]byte{}) {
					continue
				}
				if _, found := preserved[root]; !found {
					return nil, errStateRetentionChanged
				}
			}
		}
		var pending []retainedStateRoot
		for _, root := range roots {
			if _, found := preserved[root]; found {
				continue
			}
			pending = append(pending, root)
		}
		if len(pending) != 0 {
			// Ingress can keep replacing the open view between preparation and
			// admission. Catch up a bounded delta while publication is paused;
			// a new closed or imported base always requires fresh preparation.
			catchupCtx, cancel := context.WithTimeout(ctx, stateRetentionCatchupTime)
			defer cancel()
			if _, err := s.stateRetentionRootsLocked(catchupCtx, true); err != nil {
				return nil, err
			}
			bases := make(map[shamap.Type][32]byte)
			for _, root := range preparedOpen {
				bases[root.kind] = root.hash
			}
			for _, root := range pending {
				if err := s.preserveStateDifference(catchupCtx, root, bases[root.kind], sequence, nil, stateRetentionCatchupNodes); err != nil {
					return nil, fmt.Errorf("%w: open state reconciliation: %w", errStateRetentionChanged, err)
				}
				bases[root.kind] = root.hash
			}
			if err := s.nodeStore.Sync(catchupCtx); err != nil {
				return nil, err
			}
		}
		admitted = true
		return unlock, nil
	}
}

func (s *Service) openStateRetentionRootsLocked() ([]retainedStateRoot, error) {
	ledgers := []*ledger.Ledger{s.openLedger}
	if s.openLedgerView != nil {
		ledgers = append(ledgers, s.openLedgerView.Current())
	}
	var roots []retainedStateRoot
	for _, l := range ledgers {
		if l == nil {
			continue
		}
		state, err := l.StateMapHash()
		if err != nil {
			return nil, err
		}
		txs, err := l.TxMapHash()
		if err != nil {
			return nil, err
		}
		if state != ([32]byte{}) {
			roots = append(roots, retainedStateRoot{state, shamap.TypeState})
		}
		if txs != ([32]byte{}) {
			roots = append(roots, retainedStateRoot{txs, shamap.TypeTransaction})
		}
	}
	return roots, nil
}

func (s *Service) lockStateRetention(ctx context.Context) (func(), error) {
	if _, err := s.openLedgerMu.LockRoleContext(ctx, openLedgerConsensus); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.historyComponent.mu.Lock()
	s.persistMu.Lock()
	return func() {
		s.persistMu.Unlock()
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		s.openLedgerMu.Unlock()
	}, nil
}

// Query-cache entries have no ownership: admission of a cached ledger must
// re-establish completeness in the current durable generation.
func (s *Service) stateRetentionRootsLocked(ctx context.Context, persist bool) ([]retainedStateRoot, error) {
	ledgers := s.stateRetentionLedgersLocked()
	roots := make([]retainedStateRoot, 0, 2*len(ledgers))
	for _, l := range ledgers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if persist {
			kind := nodestore.NodeAccount
			store := func(entries []shamap.FlushEntry) error {
				for start := 0; start < len(entries); start += 4096 {
					part := entries[start:min(start+4096, len(entries))]
					nodes := make([]*nodestore.Node, len(part))
					for i, entry := range part {
						nodes[i] = &nodestore.Node{Type: kind, Hash: nodestore.Hash256(entry.Hash), Data: entry.Data, LedgerSeq: l.Sequence()}
					}
					if err := s.nodeStore.StoreBatch(ctx, nodes); err != nil {
						return err
					}
				}
				return nil
			}
			if err := l.StoreStateDirty(store); err != nil {
				return nil, err
			}
			kind = nodestore.NodeTransaction
			if err := l.StoreTransactionDirty(store); err != nil {
				return nil, err
			}
			if !l.IsOpen() {
				if err := s.nodeStore.Store(ctx, &nodestore.Node{Type: nodestore.NodeLedger, Hash: nodestore.Hash256(l.Hash()), Data: l.SerializeHeader(), LedgerSeq: s.validatedLedger.Sequence()}); err != nil {
					return nil, err
				}
			}
		}
		state, err := l.StateMapHash()
		if err != nil {
			return nil, err
		}
		txs, err := l.TxMapHash()
		if err != nil {
			return nil, err
		}
		if state != ([32]byte{}) {
			roots = append(roots, retainedStateRoot{state, shamap.TypeState})
		}
		if txs != ([32]byte{}) {
			roots = append(roots, retainedStateRoot{txs, shamap.TypeTransaction})
		}
	}
	return roots, nil
}

func (s *Service) stateRetentionLedgersLocked() []*ledger.Ledger {
	owned := make(map[*ledger.Ledger]struct{})
	add := func(l *ledger.Ledger) {
		if l != nil {
			owned[l] = struct{}{}
		}
	}
	add(s.validatedLedger)
	add(s.closedLedger)
	add(s.openLedger)
	add(s.replayRepairParent)
	add(s.replayRepairTarget)
	if s.openLedgerView != nil {
		add(s.openLedgerView.Current())
	}
	if s.startupReplay != nil {
		add(s.startupReplay.Parent())
	}
	for _, l := range s.ledgerHistory {
		add(l)
	}
	for _, l := range s.validationCandidates {
		add(l)
	}
	for _, job := range s.persistQueue {
		if job != nil {
			add(job.l)
		}
	}
	if s.persistActive != nil {
		add(s.persistActive.l)
	}
	ledgers := make([]*ledger.Ledger, 0, len(owned))
	for l := range owned {
		ledgers = append(ledgers, l)
	}
	sort.Slice(ledgers, func(i, j int) bool {
		if ledgers[i] == s.validatedLedger || ledgers[j] == s.validatedLedger {
			return ledgers[i] == s.validatedLedger && ledgers[j] != s.validatedLedger
		}
		return ledgers[i].Sequence() > ledgers[j].Sequence()
	})
	return ledgers
}

// Equal subtree hashes inherit preservation from the previous complete tree.
// Memory is bounded by tree depth; ordinary successors visit only changed paths.
func (s *Service) preserveStateDifference(ctx context.Context, root retainedStateRoot, previous [32]byte, sequence uint32, checkpoint func(context.Context, time.Duration) error, limit int) error {
	generations, rotating := s.nodeStore.(nodestore.GenerationDatabase)
	if rotating && previous == ([32]byte{}) && root.kind == shamap.TypeState && limit == 0 {
		return s.refreshGenerationState(ctx, root.hash, sequence, generations, checkpoint)
	}
	batch := make([]*nodestore.Node, 0, 4096)
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
	fetch := s.nodeStore.Fetch
	if rotating {
		fetch = generations.FetchForPromotion
	}
	started := time.Now()
	visited := 0
	var walk func([32]byte, [32]byte, int) error
	walk = func(hash, old [32]byte, depth int) error {
		if hash == old || hash == ([32]byte{}) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if checkpoint != nil && time.Since(started) >= refreshHealthCheckPeriod {
			if err := checkpoint(ctx, time.Since(started)); err != nil {
				return err
			}
			started = time.Now()
		}
		visited++
		if limit > 0 && visited > limit {
			return errStateRetentionChanged
		}
		node, stored, err := s.loadStoredSHAMapNodeWithFetch(ctx, storedSHAMapNode{hash, depth}, root.kind, fetch)
		if err != nil {
			return err
		}
		if !rotating {
			copy := *stored
			copy.LedgerSeq = sequence
			batch = append(batch, &copy)
			if len(batch) == cap(batch) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		inner, ok := node.(shamap.InnerNodeReader)
		if !ok {
			return nil
		}
		if depth >= 64 {
			return fmt.Errorf("inner node %x exceeds maximum depth", hash)
		}
		var prior shamap.InnerNodeReader
		if old != ([32]byte{}) {
			oldNode, _, err := s.loadStoredSHAMapNode(ctx, storedSHAMapNode{old, depth}, root.kind)
			if err != nil {
				return err
			}
			prior, _ = oldNode.(shamap.InnerNodeReader)
		}
		for branch := range shamap.BranchFactor {
			child, err := inner.ChildHash(branch)
			if err != nil {
				return err
			}
			var oldChild [32]byte
			if prior != nil {
				oldChild, err = prior.ChildHash(branch)
				if err != nil {
					return err
				}
			}
			if err := walk(child, oldChild, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root.hash, previous, 0); err != nil {
		return err
	}
	return flush()
}
