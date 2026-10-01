package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// AcquireStateAdmission pins the current durable generation before detached
// ledger work takes a service lifecycle lock. The returned release function is
// safe to call more than once. Nested callers acquire their own reference;
// nodestore shares references for an already admitted generation.
func (s *Service) AcquireStateAdmission(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.nodeStore == nil {
		return func() {}, nil
	}
	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return func() {}, nil
	}
	_, release, err := durable.AcquireDurableSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return release, nil
}

// VerifyDetachedMaps proves that externally supplied maps have no missing
// descendants in the currently pinned generation. It must run before service
// lifecycle locks are acquired. Nil maps are valid for empty transaction trees.
func (s *Service) VerifyDetachedMaps(ctx context.Context, stateMap, txMap *shamap.SHAMap) error {
	if err := s.verifyDetachedMap(ctx, "state", stateMap); err != nil {
		return err
	}
	return s.verifyDetachedMap(ctx, "transaction", txMap)
}

// VerifyDetachedLedger verifies both trees of an externally supplied ledger.
// Ledger snapshots flush dirty backed nodes before the detached completeness
// proof, while leaving the caller's map ownership unchanged.
func (s *Service) VerifyDetachedLedger(ctx context.Context, l *ledger.Ledger) error {
	if l == nil {
		return errors.New("state admission: nil ledger")
	}
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("state admission: snapshot state map: %w", err)
	}
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("state admission: snapshot transaction map: %w", err)
	}
	return s.VerifyDetachedMaps(ctx, stateMap, txMap)
}

func (s *Service) isServiceOwnedLedger(l *ledger.Ledger) bool {
	if l == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return l == s.genesisLedger || l == s.closedLedger || l == s.openLedger || l == s.validatedLedger
}

func (s *Service) verifyDetachedMap(ctx context.Context, name string, source *shamap.SHAMap) error {
	if source == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// StartSync changes map state. Always make that change on a detached
	// snapshot so admission never converts the caller's source map to syncing.
	snapshot, err := source.SnapshotImmutableContext(ctx)
	if err != nil {
		return fmt.Errorf("state admission: snapshot %s map: %w", name, err)
	}
	mapToVerify, err := s.coldDetachedMap(ctx, snapshot)
	if err != nil {
		return fmt.Errorf("state admission: load %s root: %w", name, err)
	}
	if err := mapToVerify.StartSync(); err != nil {
		return fmt.Errorf("state admission: start %s completeness check: %w", name, err)
	}
	missing, err := mapToVerify.GetMissingNodesContext(ctx, 1, nil)
	if err != nil {
		return fmt.Errorf("state admission: walk %s tree: %w", name, err)
	}
	if len(missing) != 0 {
		m := missing[0]
		return fmt.Errorf("state admission: incomplete %s node %x at depth %d: %w", name, m.Hash, m.Depth, shamap.ErrNodeNotInStore)
	}
	if err := mapToVerify.FinishSyncContext(ctx); err != nil {
		return fmt.Errorf("state admission: finish %s completeness check: %w", name, err)
	}
	return nil
}

// coldDetachedMap removes attached warm descendants before verification. A
// warm alias can otherwise satisfy a missing-node walk from memory after its
// durable backing was reclaimed. Durable fetches are used for the fresh root
// and every descendant when the configured family exposes that capability.
func (s *Service) coldDetachedMap(ctx context.Context, source *shamap.SHAMap) (*shamap.SHAMap, error) {
	if !source.IsBacked() || s.shamapFamily == nil {
		return source, nil
	}
	root, err := source.Hash()
	if err != nil {
		return nil, err
	}
	if root == ([32]byte{}) {
		return source, nil
	}
	family := durableAdmissionFamily{Family: s.shamapFamily}
	durable, ok := s.shamapFamily.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	})
	if !ok {
		return source, nil
	}
	family.durable = durable
	if snapshot, ok := s.shamapFamily.(interface {
		AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	}); ok {
		family.snapshot = snapshot
	}
	if provider, ok := s.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	}); ok {
		family.cache = provider.FullBelowCache()
	}
	return shamap.NewFromRootHashContext(ctx, source.Type(), root, family)
}

type durableAdmissionFamily struct {
	shamap.Family
	durable interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}
	snapshot interface {
		AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	}
	cache *shamap.FullBelowCache
}

func (f durableAdmissionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.durable.FetchDurable(ctx, hash)
}

func (f durableAdmissionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.durable.FetchDurable(ctx, hash)
}

func (f durableAdmissionFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	if f.snapshot == nil {
		return [32]byte{}, nil, errors.ErrUnsupported
	}
	return f.snapshot.AcquireDurableSnapshot(ctx)
}

func (f durableAdmissionFamily) FullBelowCache() *shamap.FullBelowCache {
	return f.cache
}
