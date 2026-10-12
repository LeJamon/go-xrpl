package service

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/rootlease"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

const ledgerPublication = "validated-ledger"

type flatRootPins map[rootlease.Root]func()

func flatDescriptor(h header.LedgerHeader, epoch uint64) flatstate.Descriptor {
	return flatstate.Descriptor{LedgerHash: h.Hash, ParentHash: h.ParentHash, Sequence: h.LedgerIndex,
		StateRoot: h.AccountHash, TxRoot: h.TxHash, Schema: 1, Epoch: epoch}
}

func flatMatchesHeader(d flatstate.Descriptor, h header.LedgerHeader) bool {
	return d == flatDescriptor(h, d.Epoch)
}

func (s *Service) refreshFlatRootPins(ctx context.Context) error {
	if s.config.FlatState == nil {
		return nil
	}
	wanted := make(map[rootlease.Root]struct{})
	headers := make(map[[32]byte]struct{})
	add := func(d flatstate.Descriptor) {
		if d.LedgerHash != ([32]byte{}) {
			headers[d.LedgerHash] = struct{}{}
		}
		if d.StateRoot != ([32]byte{}) {
			wanted[rootlease.Root{Hash: d.StateRoot, Kind: shamap.TypeState}] = struct{}{}
		}
		if d.TxRoot != ([32]byte{}) {
			wanted[rootlease.Root{Hash: d.TxRoot, Kind: shamap.TypeTransaction}] = struct{}{}
		}
	}
	d, found, err := s.config.FlatState.Current()
	if err != nil && !errors.Is(err, flatstate.ErrUnavailable) {
		return err
	}
	if found {
		add(d)
	}
	marker, advancing, err := s.config.FlatState.Marker()
	if err != nil {
		return err
	}
	if advancing {
		add(marker.Source)
		add(marker.Target)
	}
	outbox, err := s.config.FlatState.PendingOutbox(ctx, 0)
	if err != nil {
		return err
	}
	for _, record := range outbox {
		add(record.Descriptor)
	}
	if s.flatPins == nil {
		s.flatPins = make(flatRootPins)
	}
	for root := range wanted {
		if _, exists := s.flatPins[root]; exists {
			continue
		}
		release, err := s.config.Execution.AcquireRoot(root)
		if err != nil {
			return err
		}
		s.flatPins[root] = release
	}
	retained := make(map[[32]byte]*nodestore.Node, len(headers))
	for hash := range headers {
		node, err := s.nodeStore.Fetch(ctx, nodestore.Hash256(hash))
		if err != nil {
			return err
		}
		if node == nil || node.Type != nodestore.NodeLedger {
			return errors.New("flat version header is unavailable")
		}
		retained[hash] = node
	}
	s.flatHeadersMu.Lock()
	s.flatHeaders = retained
	s.flatHeadersMu.Unlock()
	for root, release := range s.flatPins {
		if _, exists := wanted[root]; !exists {
			release()
			delete(s.flatPins, root)
		}
	}
	return nil
}

type flatChanges struct {
	next func() (shamap.Change, error, bool)
	stop func()
}

func (s *Service) flatDiff(ctx context.Context, source, target [32]byte) *flatChanges {
	reader := shamap.NewRootReader(shamap.TypeState, source, s.shamapFamily,
		shamap.WithCommitmentBudget(s.config.PersistenceMemory))
	next, stop := iter.Pull2(func(yield func(shamap.Change, error) bool) {
		stopped := errors.New("flat diff iteration stopped")
		err := reader.Diff(ctx, target, func(change shamap.Change) error {
			if !yield(change, nil) {
				return stopped
			}
			return nil
		})
		if err != nil && !errors.Is(err, stopped) {
			yield(shamap.Change{}, err)
		}
	})
	return &flatChanges{next: next, stop: stop}
}

func (c *flatChanges) Next(ctx context.Context) (flatstate.Change, bool, error) {
	if err := ctx.Err(); err != nil {
		return flatstate.Change{}, false, err
	}
	change, err, ok := c.next()
	return flatstate.Change{Key: change.Key, Value: change.Data, Delete: change.Delete}, ok && err == nil, err
}

func (c *flatChanges) Close() error { c.stop(); return nil }

func (s *Service) authorizeFlatTarget(ctx context.Context, l *ledger.Ledger, source flatstate.Descriptor) (flatstate.RootClosureAuthorization, error) {
	h := l.Header()
	stateRoot, err := l.StateMapHash()
	if err != nil {
		return flatstate.RootClosureAuthorization{}, err
	}
	txRoot, err := l.TxMapHash()
	if err != nil {
		return flatstate.RootClosureAuthorization{}, err
	}
	if stateRoot != h.AccountHash || txRoot != h.TxHash || header.CalculateHash(h) != h.Hash {
		return flatstate.RootClosureAuthorization{}, errors.New("flat target does not match its ledger header")
	}
	// The bounded builder inherits unchanged closure from its imported base and
	// emits every changed node before this ledger's NodeStore sync. Imported
	// legacy roots require the existing full-closure verification once.
	if !l.UsesBoundedExecution() {
		for _, tree := range []struct {
			kind shamap.Type
			root [32]byte
		}{
			{shamap.TypeState, stateRoot}, {shamap.TypeTransaction, txRoot},
		} {
			reader := shamap.NewRootReader(tree.kind, tree.root, s.shamapFamily,
				shamap.WithCommitmentBudget(s.config.PersistenceMemory))
			if _, err := reader.Count(ctx); err != nil {
				return flatstate.RootClosureAuthorization{}, err
			}
		}
	}
	fingerprint := h.Hash
	if durable, ok := s.nodeStore.(nodestore.DurableDatabase); ok {
		fingerprint, err = durable.DurableFingerprint(ctx)
		if err != nil {
			return flatstate.RootClosureAuthorization{}, err
		}
	}
	token := append(append([]byte(nil), h.Hash[:]...), fingerprint[:]...)
	return flatstate.RootClosureAuthorization{SourceRoot: source.StateRoot, TargetRoot: stateRoot, Token: token}, nil
}

func (s *Service) advanceFlatState(ctx context.Context, l *ledger.Ledger) error {
	if s.config.FlatState == nil {
		return nil
	}
	if err := s.recoverFlatState(ctx); err != nil {
		return err
	}
	source, found, err := s.config.FlatState.Current()
	if err != nil {
		return err
	}
	if found && (source.Sequence > l.Sequence() || flatMatchesHeader(source, l.Header())) {
		return nil
	}
	if found && source.Epoch == ^uint64(0) {
		return errors.New("flat state epoch exhausted")
	}
	target := flatDescriptor(l.Header(), source.Epoch+1)
	releaseState, err := s.config.Execution.AcquireRoot(rootlease.Root{Hash: target.StateRoot, Kind: shamap.TypeState})
	if err != nil {
		return err
	}
	defer releaseState()
	releaseTx, err := s.config.Execution.AcquireRoot(rootlease.Root{Hash: target.TxRoot, Kind: shamap.TypeTransaction})
	if err != nil {
		return err
	}
	defer releaseTx()
	authorization, err := s.authorizeFlatTarget(ctx, l, source)
	if err != nil {
		return err
	}
	changes := s.flatDiff(ctx, source.StateRoot, target.StateRoot)
	defer changes.Close()
	var expected *flatstate.Descriptor
	if found {
		expected = &source
	}
	err = s.config.FlatState.Advance(ctx, flatstate.AdvanceRequest{Expected: expected, Target: target,
		Authorization: authorization, PublicationKind: ledgerPublication, Changes: changes})
	return errors.Join(err, s.refreshFlatRootPins(context.WithoutCancel(ctx)))
}

func (s *Service) recoverFlatState(ctx context.Context) error {
	if s.config.FlatState == nil {
		return nil
	}
	if err := s.refreshFlatRootPins(ctx); err != nil {
		return err
	}
	marker, advancing, err := s.config.FlatState.Marker()
	if err != nil {
		return err
	}
	if advancing {
		l, err := s.loadStoredLedgerByHash(ctx, marker.Target.LedgerHash)
		if err != nil {
			return err
		}
		if l == nil || !flatMatchesHeader(marker.Target, l.Header()) {
			return errors.New("flat recovery target descriptor does not match durable header")
		}
		authorization, err := s.authorizeFlatTarget(ctx, l, marker.Source)
		if err != nil {
			return err
		}
		changes := s.flatDiff(ctx, marker.Source.StateRoot, marker.Target.StateRoot)
		defer changes.Close()
		if err := s.config.FlatState.Resume(ctx, flatstate.ResumeRequest{Authorization: authorization,
			PublicationKind: marker.Intent, Changes: changes}); err != nil {
			return err
		}
	}
	return s.replayFlatOutbox(ctx)
}

func (s *Service) replayFlatOutbox(ctx context.Context) error {
	if s.config.FlatState == nil {
		return nil
	}
	records, err := s.config.FlatState.PendingOutbox(ctx, 0)
	if err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Descriptor.Epoch < records[j].Descriptor.Epoch })
	for _, record := range records {
		if record.Intent != ledgerPublication {
			return errors.New("unsupported flat publication intent")
		}
		l, err := s.loadStoredLedgerByHash(ctx, record.Descriptor.LedgerHash)
		if err != nil {
			return err
		}
		if l == nil || !flatMatchesHeader(record.Descriptor, l.Header()) {
			return errors.New("flat outbox header mismatch")
		}
		if s.relationalDB != nil {
			if err := s.persistToRelationalDB(ctx, l); err != nil {
				return fmt.Errorf("flat outbox index publication: %w", err)
			}
		}
		if err := s.persistValidatedTipLocked(ctx, l, true); err != nil {
			return err
		}
		if err := s.config.FlatState.AckOutbox(ctx, record.CommitID); err != nil {
			return err
		}
	}
	return s.refreshFlatRootPins(ctx)
}

func (s *Service) enableExecution(l *ledger.Ledger) error {
	if s.config.Execution == nil || l == nil {
		return nil
	}
	return l.EnableBoundedExecution(context.Background(), s.config.Execution)
}
