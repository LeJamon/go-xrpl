package adaptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/shamap"
)

const maxRecoveryAncestry = 4096

const (
	maxPivotAcquisitionAttempts = 5
	pivotRecoveryRetryBase      = 250 * time.Millisecond
	pivotRecoveryRetryMaximum   = 2 * time.Second
)

var errPivotAcquisitionUnavailable = errors.New("recovery acquisition unavailable")

type recoveryAcquisitionFamily struct{ *acquisitionStoreScope }

func (*recoveryAcquisitionFamily) FullBelowCache() *shamap.FullBelowCache { return nil }

func (f *recoveryAcquisitionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	promoted, retired := f.promoted, f.retired
	f.mu.Unlock()
	if promoted {
		return f.acquisitionStoreScope.Fetch(ctx, hash)
	}
	if !retired {
		if data, ok := f.lane.fetchPending(f.id, hash); ok {
			return data, nil
		}
	}
	return f.lane.FetchDurable(ctx, hash)
}

func (f *recoveryAcquisitionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.lane.FetchDurable(ctx, hash)
}

type recoveryAcquisitionResult struct {
	header              header.LedgerHeader
	state, transactions *shamap.SHAMap
	err                 error
}

type pivotRecoveryAcquisition struct {
	ledger *inbound.Ledger
	done   chan recoveryAcquisitionResult
}

type recoveryAuthentication struct {
	Quorum   json.RawMessage       `json:"quorum"`
	Ancestry []header.LedgerHeader `json:"ancestry"`
}

type pivotRecoveryBackend struct {
	coordinator              *catchupReplayCoordinator
	mu                       sync.Mutex
	headers                  map[[32]byte]header.LedgerHeader
	reused, fetched, missing uint64
}

// RestoreRecoveryProgress seeds counters recovered from the durable recovery
// journal. Acquisition snapshots are monotonic, so a restart must not make
// already reported work appear to disappear.
func (b *pivotRecoveryBackend) RestoreRecoveryProgress(reused, fetched, missing uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if reused > b.reused {
		b.reused = reused
	}
	if fetched > b.fetched {
		b.fetched = fetched
	}
	if missing > b.missing {
		b.missing = missing
	}
}

func (b *pivotRecoveryBackend) PivotRecoveryLag(frontier uint32) uint32 {
	seq, _, _ := b.coordinator.bestCatchupTarget()
	if seq > frontier {
		return seq - frontier
	}
	return 0
}

func (b *pivotRecoveryBackend) PivotRecoveryProgress() (reused, fetched, missing uint64) {
	c := b.coordinator
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	b.mu.Lock()
	reused, fetched, missing = b.reused, b.fetched, b.missing
	b.mu.Unlock()
	if job := c.pivotRecovery; job != nil {
		snapshot := job.ledger.Snapshot()
		reused += snapshot.StateDurableReads + snapshot.TxDurableReads
		fetched += snapshot.StateUseful + snapshot.TxUseful
		missing += snapshot.StateMissingDiscovered
	}
	return
}

func (b *pivotRecoveryBackend) SelectPivot(ctx context.Context, after uint32) (service.RecoveryTarget, error) {
	for {
		if b.coordinator.stoppedForShutdown() {
			return service.RecoveryTarget{}, context.Canceled
		}
		seq, hash, _ := b.coordinator.bestCatchupTarget()
		if seq > after {
			proof, err := b.coordinator.adaptor.CaptureValidationQuorumEvidence(seq, hash)
			if err == nil {
				h, err := b.acquireHeader(ctx, seq, hash)
				if err != nil {
					return service.RecoveryTarget{}, err
				}
				return recoveryTarget(h, proof, []header.LedgerHeader{h})
			}
		}
		if err := waitRecovery(ctx); err != nil {
			return service.RecoveryTarget{}, err
		}
	}
}

func recoveryTarget(h header.LedgerHeader, proof json.RawMessage, ancestry []header.LedgerHeader) (service.RecoveryTarget, error) {
	raw, err := json.Marshal(recoveryAuthentication{Quorum: proof, Ancestry: ancestry})
	return service.RecoveryTarget{Header: h, Evidence: raw}, err
}

func (b *pivotRecoveryBackend) Authenticate(ctx context.Context, target service.RecoveryTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil || b.coordinator == nil || b.coordinator.adaptor == nil {
		return errors.New("recovery authentication coordinator is unavailable")
	}
	if len(target.Evidence) == 0 || len(target.Evidence) > 8<<20 {
		return errors.New("recovery authentication evidence size is invalid")
	}
	var proof recoveryAuthentication
	if err := json.Unmarshal(target.Evidence, &proof); err != nil {
		return fmt.Errorf("decode recovery authentication: %w", err)
	}
	if len(proof.Ancestry) == 0 || len(proof.Ancestry) > maxRecoveryAncestry {
		return errors.New("recovery ancestry length is invalid")
	}
	if proof.Ancestry[0].Hash != target.Header.Hash || header.CalculateHash(target.Header) != target.Header.Hash {
		return errors.New("recovery evidence does not bind target header")
	}
	for i, h := range proof.Ancestry {
		if h.LedgerIndex == 0 || header.CalculateHash(h) != h.Hash {
			return errors.New("recovery ancestry header hash mismatch")
		}
		if i > 0 {
			parent := proof.Ancestry[i-1]
			if parent.LedgerIndex == ^uint32(0) || h.LedgerIndex != parent.LedgerIndex+1 || h.ParentHash != parent.Hash {
				return errors.New("recovery ancestry is not linked")
			}
		}
	}
	anchor := proof.Ancestry[len(proof.Ancestry)-1]
	err := b.coordinator.adaptor.AuthenticateValidationQuorumEvidence(anchor.LedgerIndex, anchor.Hash, proof.Quorum)
	if errors.Is(err, ErrRecoveryProofUnavailable) {
		return fmt.Errorf("%w: %w", service.ErrRecoveryUnavailable, err)
	}
	return err
}

func (b *pivotRecoveryBackend) Acquire(ctx context.Context, target service.RecoveryTarget, parent *ledger.Ledger) (*ledger.Ledger, error) {
	if err := b.Authenticate(ctx, target); err != nil {
		return nil, err
	}
	result, err := b.acquire(ctx, target.Header.LedgerIndex, target.Header.Hash, parent != nil)
	if err != nil {
		return nil, err
	}
	if result.header.Hash != target.Header.Hash || header.CalculateHash(result.header) != target.Header.Hash {
		return nil, errors.New("acquired recovery header changed")
	}
	fees := drops.Fees{}
	if parent != nil {
		if target.Header.ParentHash != parent.Hash() || parent.Sequence() == ^uint32(0) || target.Header.LedgerIndex != parent.Sequence()+1 {
			return nil, errors.New("recovery target is not a linked successor")
		}
		fees = parent.Fees()
		result.state, err = parent.StateMapSnapshot()
		if err != nil {
			return nil, err
		}
	}
	return ledger.NewFromHeader(result.header, result.state, result.transactions, fees)
}

func (b *pivotRecoveryBackend) Next(ctx context.Context, parent header.LedgerHeader) (service.RecoveryTarget, error) {
	if parent.LedgerIndex == ^uint32(0) {
		return service.RecoveryTarget{}, errors.New("recovery sequence exhausted")
	}
	for {
		if b.coordinator.stoppedForShutdown() {
			return service.RecoveryTarget{}, context.Canceled
		}
		seq, hash, _ := b.coordinator.bestCatchupTarget()
		if seq <= parent.LedgerIndex {
			if err := waitRecovery(ctx); err != nil {
				return service.RecoveryTarget{}, err
			}
			continue
		}
		proof, err := b.coordinator.adaptor.CaptureValidationQuorumEvidence(seq, hash)
		if err != nil {
			if err = waitRecovery(ctx); err != nil {
				return service.RecoveryTarget{}, err
			}
			continue
		}
		if seq-parent.LedgerIndex > maxRecoveryAncestry {
			return service.RecoveryTarget{}, fmt.Errorf("recovery ancestry exceeds bounded window of %d headers", maxRecoveryAncestry)
		}
		reverse := make([]header.LedgerHeader, 0, seq-parent.LedgerIndex)
		for index := seq; index > parent.LedgerIndex; index-- {
			h, err := b.acquireHeader(ctx, index, hash)
			if err != nil {
				return service.RecoveryTarget{}, err
			}
			reverse = append(reverse, h)
			hash = h.ParentHash
		}
		if hash != parent.Hash {
			return service.RecoveryTarget{}, errors.New("trusted successor ancestry conflicts with recovery frontier")
		}
		for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
			reverse[i], reverse[j] = reverse[j], reverse[i]
		}
		return recoveryTarget(reverse[0], proof, reverse)
	}
}

func (b *pivotRecoveryBackend) CaughtUp(ctx context.Context, h header.LedgerHeader) bool {
	if b == nil || b.coordinator == nil || b.coordinator.adaptor == nil ||
		h.LedgerIndex == 0 || h.Hash == ([32]byte{}) || header.CalculateHash(h) != h.Hash ||
		ctx.Err() != nil || b.coordinator.stoppedForShutdown() {
		return false
	}
	if !b.liveRecoveryNetwork() {
		return false
	}
	seq, hash, _ := b.coordinator.bestCatchupTarget()
	if seq > h.LedgerIndex || (seq == h.LedgerIndex && hash != h.Hash) {
		return false
	}
	signTime, result := b.coordinator.adaptor.recheckFullyValidated(h.LedgerIndex, h.Hash)
	if result != validationRecheckAccepted || signTime.IsZero() {
		return false
	}
	if !rcl.IsCurrent(b.coordinator.adaptor.Now(), signTime, time.Time{}) {
		return false
	}
	return b.liveValidationQuorum(h.LedgerIndex, h.Hash)
}

// liveRecoveryNetwork applies the same connected-peer requirement used by
// ordinary acquisition. A retained quorum target is not live network evidence
// after every source peer has gone away.
func (b *pivotRecoveryBackend) liveRecoveryNetwork() bool {
	c := b.coordinator
	if c == nil || c.peerSessions == nil || c.adaptor == nil ||
		c.adaptor.GetOperatingMode() == consensus.OpModeDisconnected {
		return false
	}
	if peers, ok := c.peerSessions.(peerCountView); ok && peers.PeerCount() < networkPeerQuorum {
		return false
	}
	_, ok := c.selectAcquisitionPeer(0)
	return ok
}

// liveValidationQuorum is deliberately stricter than durable proof
// authentication. Handoff requires the currently trusted validations for the
// exact frontier to remain current under the normal sign/seen-time gate.
func (b *pivotRecoveryBackend) liveValidationQuorum(seq uint32, hash [32]byte) bool {
	a := b.coordinator.adaptor
	if a == nil || a.IsQuorumUnavailable() {
		return false
	}
	a.mu.Lock()
	historian := a.validationHistorian
	a.mu.Unlock()
	records, ok := historian.(consensus.ValidationHistorian)
	if !ok {
		return false
	}
	_, quorum, negative := a.GetValidationConfig()
	if quorum <= 0 || a.IsQuorumUnavailable() {
		return false
	}
	negativeSet := make(map[consensus.NodeID]struct{}, len(negative))
	for _, nodeID := range negative {
		negativeSet[nodeID] = struct{}{}
	}
	now := a.Now()
	seen := make(map[consensus.NodeID]struct{}, quorum)
	for _, validation := range records.GetTrustedFullValidations(consensus.LedgerID(hash), seq) {
		if validation == nil || validation.LedgerSeq != seq || validation.LedgerID != consensus.LedgerID(hash) ||
			!validation.Full || !rcl.IsCurrent(now, validation.SignTime, validation.SeenTime) {
			continue
		}
		if _, disabled := negativeSet[validation.NodeID]; disabled {
			continue
		}
		seen[validation.NodeID] = struct{}{}
	}
	return len(seen) >= quorum
}

func (b *pivotRecoveryBackend) Handoff(ctx context.Context, h header.LedgerHeader) error {
	c := b.coordinator
	for {
		if !b.CaughtUp(ctx, h) {
			return errors.New("recovery frontier is not at the live trusted head")
		}
		if c.engine == nil {
			return errors.New("consensus engine unavailable for recovery handoff")
		}
		eligible, err := c.engine.CanAcceptLedger(consensus.LedgerID(h.Hash))
		if err != nil {
			return err
		}
		if !eligible {
			if err := waitRecovery(ctx); err != nil {
				return err
			}
			continue
		}
		result, err := c.engine.TrySwitchToLedger(consensus.LedgerID(h.Hash))
		if err != nil {
			return err
		}
		if result == consensus.LedgerSwitchAccepted {
			if !b.CaughtUp(ctx, h) {
				return errors.New("recovery frontier lost live trust after ledger switch")
			}
			if c.adaptor.GetOperatingMode() < consensus.OpModeTracking {
				c.adaptor.SetOperatingMode(consensus.OpModeTracking)
			}
			c.recordAcquiredSeqHash(h.LedgerIndex, h.Hash, h.ParentHash)
			return nil
		}
		if result != consensus.LedgerSwitchBusy {
			return errors.New("consensus rejected recovery frontier")
		}
		if err := waitRecovery(ctx); err != nil {
			return err
		}
	}
}

func waitRecovery(ctx context.Context) error {
	return waitRecoveryDelay(ctx, 250*time.Millisecond)
}

func waitRecoveryDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (b *pivotRecoveryBackend) acquireHeader(ctx context.Context, seq uint32, hash [32]byte) (header.LedgerHeader, error) {
	b.mu.Lock()
	h, ok := b.headers[hash]
	b.mu.Unlock()
	if ok && h.LedgerIndex == seq {
		return h, nil
	}
	result, err := b.acquire(ctx, seq, hash, true)
	return result.header, err
}

func (b *pivotRecoveryBackend) acquire(ctx context.Context, seq uint32, hash [32]byte, txOnly bool) (recoveryAcquisitionResult, error) {
	for attempt := 0; ; attempt++ {
		result, err := b.acquireOnce(ctx, seq, hash, txOnly)
		if err == nil {
			return result, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if lifecycleErr := b.coordinator.lifecycleContext().Err(); lifecycleErr != nil {
			return result, lifecycleErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, err
		}
		if b.coordinator.stoppedForShutdown() || !b.coordinator.replayFaultBlocked() {
			return result, err
		}
		if errors.Is(err, inbound.ErrHeaderRejected) {
			return result, err
		}
		if attempt+1 >= maxPivotAcquisitionAttempts {
			if pivotAcquisitionContentError(err) {
				return result, fmt.Errorf("recovery acquisition retries exhausted: %w", err)
			}
			return result, fmt.Errorf("%w: recovery acquisition retries exhausted: %v", service.ErrRecoveryUnavailable, err)
		}
		if err := waitRecoveryDelay(ctx, pivotRecoveryRetryDelay(attempt)); err != nil {
			return result, err
		}
	}
}

func pivotAcquisitionContentError(err error) bool {
	return errors.Is(err, inbound.ErrInvalidPeerNode) ||
		errors.Is(err, shamap.ErrInvalidNodeData) ||
		errors.Is(err, shamap.ErrNodeSerialization)
}

func pivotRecoveryRetryDelay(attempt int) time.Duration {
	delay := pivotRecoveryRetryBase
	for i := 0; i < attempt && delay < pivotRecoveryRetryMaximum; i++ {
		delay *= 2
	}
	if delay > pivotRecoveryRetryMaximum {
		return pivotRecoveryRetryMaximum
	}
	return delay
}

func (b *pivotRecoveryBackend) acquireOnce(ctx context.Context, seq uint32, hash [32]byte, txOnly bool) (recoveryAcquisitionResult, error) {
	c := b.coordinator
	if ctx.Err() != nil {
		return recoveryAcquisitionResult{}, ctx.Err()
	}
	if !c.replayFaultBlocked() || c.stoppedForShutdown() {
		return recoveryAcquisitionResult{}, errors.New("recovery acquisition requires an active replay quarantine")
	}
	peer, ok := c.selectAcquisitionPeer(seq)
	if !ok {
		return recoveryAcquisitionResult{}, fmt.Errorf("%w: no peer for ledger %d", errPivotAcquisitionUnavailable, seq)
	}
	opts := c.acquisitionOptions(true)
	if txOnly {
		opts = append(opts, inbound.WithTransactionOnly())
	}
	job := &pivotRecoveryAcquisition{done: make(chan recoveryAcquisitionResult, 1)}
	c.acquisitionMu.Lock()
	if c.pivotRecovery != nil {
		c.acquisitionMu.Unlock()
		return recoveryAcquisitionResult{}, errors.New("another recovery acquisition is active")
	}
	il, created := c.fetchTracker.GetOrCreateWithSequence(hash, seq, func() *inbound.Ledger { return inbound.NewRecovery(hash, seq, peer, c.logger, opts...) })
	if !created || il == nil {
		c.acquisitionMu.Unlock()
		return recoveryAcquisitionResult{}, fmt.Errorf("%w: ledger already has another acquisition owner", errPivotAcquisitionUnavailable)
	}
	job.ledger = il
	c.pivotRecovery = job
	c.acquisitionMu.Unlock()
	promoted := false
	defer func() {
		c.acquisitionMu.Lock()
		if c.pivotRecovery == job {
			snapshot := il.Snapshot()
			b.mu.Lock()
			b.reused += snapshot.StateDurableReads + snapshot.TxDurableReads
			b.fetched += snapshot.StateUseful + snapshot.TxUseful
			b.missing += snapshot.StateMissingDiscovered
			b.mu.Unlock()
			c.pivotRecovery = nil
		}
		c.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), false)
		c.acquisitionMu.Unlock()
		if !promoted {
			c.retireAcquisitionStore(context.Background(), il)
		}
	}()
	c.requestLedgerBase(il, peer, "replay recovery: initial ledger request failed")
	select {
	case <-ctx.Done():
		return recoveryAcquisitionResult{}, ctx.Err()
	case <-c.lifecycleContext().Done():
		return recoveryAcquisitionResult{}, context.Canceled
	case result := <-job.done:
		if result.err != nil {
			return result, result.err
		}
		promoted = true
		b.mu.Lock()
		if b.headers == nil || len(b.headers) >= maxRecoveryAncestry {
			b.headers = make(map[[32]byte]header.LedgerHeader)
		}
		b.headers[result.header.Hash] = result.header
		b.mu.Unlock()
		return result, nil
	}
}

func (c *catchupReplayCoordinator) completePivotRecoveryAcquisition(il *inbound.Ledger) {
	h, state, transactions, err := il.Result()
	if err == nil {
		err = c.promoteAcquisitionStore(c.lifecycleContext(), il)
	}
	result := recoveryAcquisitionResult{state: state, transactions: transactions, err: err}
	if h != nil {
		result.header = *h
	}
	c.finishPivotRecoveryAcquisition(il, result)
}

func (c *catchupReplayCoordinator) failPivotRecoveryAcquisition(il *inbound.Ledger, cause error) {
	if cause == nil {
		cause = errPivotAcquisitionUnavailable
	}
	c.finishPivotRecoveryAcquisition(il, recoveryAcquisitionResult{err: cause})
}

func (c *catchupReplayCoordinator) finishPivotRecoveryAcquisition(il *inbound.Ledger, result recoveryAcquisitionResult) {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	job := c.pivotRecovery
	if job == nil || job.ledger != il {
		return
	}
	c.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), result.err == nil)
	select {
	case job.done <- result:
	default:
	}
}

func (c *catchupReplayCoordinator) rememberReplayHeader(h header.LedgerHeader) {
	if h.Hash == ([32]byte{}) || header.CalculateHash(h) != h.Hash {
		return
	}
	c.seqHashMu.Lock()
	defer c.seqHashMu.Unlock()
	entry, ok := c.seqHash[h.LedgerIndex]
	if ok && entry.hash == h.Hash {
		entry.header = &h
		c.seqHash[h.LedgerIndex] = entry
	}
}

func (b *pivotRecoveryBackend) CaptureReplayEvidence(h header.LedgerHeader) (json.RawMessage, error) {
	c := b.coordinator
	proof, err := c.adaptor.CaptureValidationQuorumEvidence(h.LedgerIndex, h.Hash)
	if err == nil {
		target, encodeErr := recoveryTarget(h, proof, []header.LedgerHeader{h})
		if encodeErr != nil {
			return nil, encodeErr
		}
		if authErr := b.Authenticate(context.Background(), target); authErr != nil {
			return nil, fmt.Errorf("authenticate captured replay evidence: %w", authErr)
		}
		return target.Evidence, nil
	}
	seq, hash, _ := c.bestCatchupTarget()
	if seq <= h.LedgerIndex || seq-h.LedgerIndex >= maxRecoveryAncestry {
		return nil, err
	}
	proof, err = c.adaptor.CaptureValidationQuorumEvidence(seq, hash)
	if err != nil {
		return nil, err
	}
	ancestry := make([]header.LedgerHeader, 1, seq-h.LedgerIndex+1)
	ancestry[0] = h
	c.seqHashMu.Lock()
	for index := h.LedgerIndex + 1; ; index++ {
		entry, ok := c.seqHash[index]
		if !ok || entry.header == nil || entry.header.Hash != entry.hash {
			c.seqHashMu.Unlock()
			return nil, errors.New("full replay authentication ancestry unavailable")
		}
		ancestry = append(ancestry, *entry.header)
		if index == seq {
			break
		}
	}
	c.seqHashMu.Unlock()
	target, err := recoveryTarget(h, proof, ancestry)
	if err != nil {
		return nil, err
	}
	if err := b.Authenticate(context.Background(), target); err != nil {
		return nil, err
	}
	return target.Evidence, nil
}
