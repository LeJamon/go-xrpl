package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/LeJamon/go-xrpl/shamap"
)

// DetachedMapVerification retains the traversal state needed to prove a pair
// of detached SHAMaps over several bounded calls.
type DetachedMapVerification struct {
	service *Service
	state   *shamap.SHAMap
	tx      *shamap.SHAMap

	stateSource *shamap.SHAMap
	txSource    *shamap.SHAMap
	initialized bool
	safeStorage bool

	mu sync.Mutex
}

// NewDetachedMapVerification creates a reusable verifier for the supplied
// snapshots. The maps are loaded only on the first Verify call.
func (s *Service) NewDetachedMapVerification(stateMap, txMap *shamap.SHAMap) *DetachedMapVerification {
	return &DetachedMapVerification{
		service:     s,
		stateSource: stateMap,
		txSource:    txMap,
	}
}

// Verify advances both completeness walks. A caller can bound each call with
// shamap.WithTraversalBudget or a deadline and retry the same verifier after a
// bounded error. Missing nodes leave the verifier resumable for a later retry.
func (v *DetachedMapVerification) Verify(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := v.pinStorage(ctx)
	if err != nil {
		return err
	}
	defer release()

	if !v.initialized {
		if err := v.initialize(ctx); err != nil {
			return err
		}
	} else if v.safeStorage {
		if err := v.validateSources(ctx); err != nil {
			return err
		}
	}
	if err := v.verifyMap(ctx, "state", v.state); err != nil {
		return err
	}
	return v.verifyMap(ctx, "transaction", v.tx)
}

func (v *DetachedMapVerification) initialize(ctx context.Context) error {
	stateSource, err := snapshotVerificationSource(ctx, v.stateSource, "state")
	if err != nil {
		return err
	}
	txSource, err := snapshotVerificationSource(ctx, v.txSource, "transaction")
	if err != nil {
		return err
	}
	v.stateSource, v.txSource = stateSource, txSource

	if stateSource != nil {
		state, err := v.service.coldDetachedMap(ctx, stateSource)
		if err != nil {
			return fmt.Errorf("state admission: load state root: %w", err)
		}
		v.state = state
	}
	if txSource != nil {
		tx, err := v.service.coldDetachedMap(ctx, txSource)
		if err != nil {
			return fmt.Errorf("state admission: load transaction root: %w", err)
		}
		v.tx = tx
	}
	v.initialized = true
	return nil
}

func snapshotVerificationSource(ctx context.Context, source *shamap.SHAMap, name string) (*shamap.SHAMap, error) {
	if source == nil {
		return nil, nil
	}
	snapshot, err := source.SnapshotImmutableContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("state admission: snapshot %s map: %w", name, err)
	}
	return snapshot, nil
}

func (v *DetachedMapVerification) validateSources(ctx context.Context) error {
	if v.stateSource != nil && v.stateSource.IsBacked() {
		if _, err := v.service.coldDetachedMap(ctx, v.stateSource); err != nil {
			return fmt.Errorf("state admission: validate state root: %w", err)
		}
	}
	if v.txSource != nil && v.txSource.IsBacked() {
		if _, err := v.service.coldDetachedMap(ctx, v.txSource); err != nil {
			return fmt.Errorf("state admission: validate transaction root: %w", err)
		}
	}
	return nil
}

func (v *DetachedMapVerification) pinStorage(ctx context.Context) (func(), error) {
	if !hasBackedSource(v.stateSource, v.txSource) {
		return func() {}, nil
	}
	if v.initialized && !v.safeStorage {
		return func() {}, nil
	}
	family := detachedVerificationFamily(v.service)
	if family == nil {
		v.safeStorage = false
		return func() {}, nil
	}
	_, release, err := family.AcquireDurableSnapshot(ctx)
	if errors.Is(err, errors.ErrUnsupported) {
		v.safeStorage = false
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state admission: pin SHAMap generation: %w", err)
	}
	v.safeStorage = true
	return release, nil
}

func hasBackedSource(state, tx *shamap.SHAMap) bool {
	return (state != nil && state.IsBacked()) || (tx != nil && tx.IsBacked())
}

func (v *DetachedMapVerification) verifyMap(ctx context.Context, name string, m *shamap.SHAMap) error {
	if m == nil {
		return nil
	}
	if !m.IsBacked() || v.safeStorage {
		if err := m.StartSync(); err != nil {
			return fmt.Errorf("state admission: start %s completeness check: %w", name, err)
		}
		missing, err := m.GetMissingNodesContext(ctx, 1, nil)
		if err != nil {
			return fmt.Errorf("state admission: walk %s tree: %w", name, err)
		}
		if len(missing) != 0 {
			return missingVerificationNodeError(name, missing[0])
		}
		return nil
	}

	result, err := m.CheckComplete(ctx)
	if err != nil {
		return fmt.Errorf("state admission: check %s completeness: %w", name, err)
	}
	if len(result.Missing) != 0 {
		return missingVerificationNodeError(name, result.Missing[0])
	}
	if len(result.Corrupt) != 0 {
		return fmt.Errorf("state admission: corrupt %s tree node %x", name, result.Corrupt[0].Hash[:8])
	}
	return nil
}

func missingVerificationNodeError(name string, missing shamap.MissingNode) error {
	return fmt.Errorf(
		"state admission: incomplete %s tree at depth %d: %w",
		name,
		missing.Depth,
		&shamap.MissingNodeError{Hash: missing.Hash},
	)
}

type detachedVerificationDurableFamily interface {
	FetchDurable(context.Context, [32]byte) ([]byte, error)
	AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	FullBelowCache() *shamap.FullBelowCache
}

func detachedVerificationFamily(s *Service) detachedVerificationDurableFamily {
	if s == nil || s.shamapFamily == nil {
		return nil
	}
	family, _ := s.shamapFamily.(detachedVerificationDurableFamily)
	if family == nil || family.FullBelowCache() == nil {
		return nil
	}
	return family
}
