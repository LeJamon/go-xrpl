package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

var errStoredLedgerUnavailable = errors.New("stored ledger is incomplete or invalid")

func isUnavailableSHAMapNode(err error) bool {
	return errors.Is(err, shamap.ErrNodeNotInStore) || errors.Is(err, shamap.ErrInvalidNodeData)
}

func (s *Service) loadLatestLedger(ctx context.Context) (*ledger.Ledger, error) {
	if s.nodeStore == nil || s.relationalDB == nil || s.shamapFamily == nil {
		return nil, nil
	}
	info, err := s.relationalDB.Ledger().GetNewestLedgerInfo(ctx)
	if err != nil || info == nil {
		return nil, err
	}
	tip, err := s.nodeStore.Fetch(ctx, validatedTipKey)
	if err != nil {
		return nil, err
	}
	if tip == nil {
		return nil, nil
	}
	if tip.Type != nodestore.NodeLedger || tip.LedgerSeq != uint32(info.Sequence) ||
		len(tip.Data) != 32 || !bytes.Equal(tip.Data, info.Hash[:]) {
		return nil, fmt.Errorf("newest relational ledger %d is not the persisted validated tip", info.Sequence)
	}
	loaded, err := s.loadStoredLedgerByHash(ctx, [32]byte(info.Hash))
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		return nil, fmt.Errorf("ledger %d header is missing", info.Sequence)
	}
	h := loaded.Header()
	if !storedHeaderMatchesInfo(h, info) {
		return nil, fmt.Errorf("ledger %d header does not match persisted metadata", info.Sequence)
	}
	accepted, checkpointErr := s.acceptFastLoadCheckpoint(ctx, s.startupFastLoadCheckpoint, h)
	if checkpointErr != nil {
		s.logger.Warn("Fast-load checkpoint rejected; using strict traversal",
			"sequence", h.LedgerIndex,
			"err", checkpointErr,
		)
	}
	if !accepted {
		metrics, nodeFingerprint, baseAvailable, err := s.verifyFastLoadStrictState(ctx, h)
		if err != nil {
			return nil, fmt.Errorf("ledger %d: %w", info.Sequence, err)
		}
		s.fastLoadStrictNodes.Store(metrics.nodes)
		s.fastLoadStrictElapsed.Store(uint64(metrics.elapsed))
		if baseAvailable {
			s.fastLoadBaseStateRoot = h.AccountHash
			s.fastLoadBaseFingerprint = nodeFingerprint
			s.fastLoadBaseVerified = true
		}
		s.markFastLoadCheckpointEligible()
		s.logger.Info("Fast-load strict traversal completed",
			"sequence", h.LedgerIndex,
			"checkpoint_fallback", s.startupFastLoadCheckpoint != nil,
			"checkpoint_relative_base", baseAvailable,
			"nodes_checked", metrics.nodes,
			"elapsed", metrics.elapsed.String(),
		)
	}
	if err := loaded.SetValidated(); err != nil {
		return nil, fmt.Errorf("mark newest ledger %d validated: %w", info.Sequence, err)
	}
	return loaded, nil
}

// verifyFastLoadStrictState verifies the durable startup ledger while pinning
// the NodeStore generation when that capability is available. A successful
// strict traversal is as strong a completeness proof as an accepted startup
// checkpoint, so retain its state root and fingerprint for checkpoint-relative
// pivot discovery instead of forcing the subsequent pivot acquisition to walk
// the entire state again.
func (s *Service) verifyFastLoadStrictState(
	ctx context.Context,
	h header.LedgerHeader,
) (storedSHAMapVerificationMetrics, [32]byte, bool, error) {
	var metrics storedSHAMapVerificationMetrics
	verify := func() error {
		stateMetrics, err := s.verifyStoredSHAMapMeasured(ctx, h.AccountHash, shamap.TypeState)
		if err != nil {
			return fmt.Errorf("state tree: %w", err)
		}
		metrics = stateMetrics
		if h.TxHash == ([32]byte{}) {
			return nil
		}
		txMetrics, err := s.verifyStoredSHAMapMeasured(ctx, h.TxHash, shamap.TypeTransaction)
		if err != nil {
			return fmt.Errorf("transaction tree: %w", err)
		}
		metrics.nodes += txMetrics.nodes
		metrics.elapsed += txMetrics.elapsed
		return nil
	}

	durable, ok := s.nodeStore.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return metrics, [32]byte{}, false, verify()
	}
	fingerprint, release, err := durable.AcquireDurableSnapshot(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return metrics, [32]byte{}, false, ctxErr
		}
		s.logger.Warn("Fast-load strict traversal cannot retain durable state base; continuing without it",
			"err", err,
		)
		return metrics, [32]byte{}, false, verify()
	}
	defer release()
	if err := verify(); err != nil {
		return metrics, [32]byte{}, false, err
	}
	return metrics, fingerprint, true, nil
}

func (s *Service) loadStoredLedgerByHash(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	if s.nodeStore == nil || s.shamapFamily == nil {
		return nil, nil
	}
	stored, err := s.nodeStore.Fetch(ctx, nodestore.Hash256(hash))
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, nil
	}
	if stored.Type != nodestore.NodeLedger {
		return nil, fmt.Errorf("%w: stored object has type %d, not a ledger header", errStoredLedgerUnavailable, stored.Type)
	}
	h, err := header.DeserializeHeader(stored.Data, true)
	if err != nil {
		return nil, fmt.Errorf("%w: deserialize ledger header: %v", errStoredLedgerUnavailable, err)
	}
	if h.Hash != hash || header.CalculateHash(*h) != hash {
		return nil, fmt.Errorf("%w: stored ledger header does not match requested hash", errStoredLedgerUnavailable)
	}
	if stored.LedgerSeq != 0 && stored.LedgerSeq != h.LedgerIndex {
		return nil, fmt.Errorf("%w: stored ledger sequence %d does not match header %d", errStoredLedgerUnavailable, stored.LedgerSeq, h.LedgerIndex)
	}

	stateMap, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, h.AccountHash, s.shamapFamily)
	if err != nil {
		if isUnavailableSHAMapNode(err) {
			return nil, fmt.Errorf("%w: state root: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		return nil, err
	}
	if stateRoot != h.AccountHash {
		return nil, fmt.Errorf("%w: stored state root does not match ledger header", errStoredLedgerUnavailable)
	}
	var txMap *shamap.SHAMap
	if h.TxHash == ([32]byte{}) {
		txMap, err = shamap.NewBacked(shamap.TypeTransaction, s.shamapFamily)
	} else {
		txMap, err = shamap.NewFromRootHashContext(ctx, shamap.TypeTransaction, h.TxHash, s.shamapFamily)
	}
	if err != nil {
		if isUnavailableSHAMapNode(err) {
			return nil, fmt.Errorf("%w: transaction root: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	txRoot, err := txMap.Hash()
	if err != nil {
		return nil, err
	}
	if txRoot != h.TxHash {
		return nil, fmt.Errorf("%w: stored transaction root does not match ledger header", errStoredLedgerUnavailable)
	}
	stateMap.SetLedgerSeq(h.LedgerIndex)
	txMap.SetLedgerSeq(h.LedgerIndex)
	if err := stateMap.SetImmutable(); err != nil {
		return nil, err
	}
	if err := txMap.SetImmutable(); err != nil {
		return nil, err
	}
	rules, err := ledger.LoadAmendmentsFromSHAMapContext(ctx, stateMap)
	if err != nil {
		if isUnavailableSHAMapNode(err) {
			return nil, fmt.Errorf("%w: amendment state: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	fees, err := storedLedgerFees(ctx, stateMap, rules.XRPFeesEnabled(), s.configuredFees)
	if err != nil {
		if isUnavailableSHAMapNode(err) || errors.Is(err, state.ErrInvalidFeeSettings) || errors.Is(err, errStoredLedgerUnavailable) {
			return nil, fmt.Errorf("%w: fee settings: %v", errStoredLedgerUnavailable, err)
		}
		return nil, err
	}
	h.Validated = false
	h.Accepted = true
	loaded, err := ledger.NewClosedFromHeaderContext(ctx, *h, stateMap, txMap, fees)
	if err != nil && isUnavailableSHAMapNode(err) {
		return nil, fmt.Errorf("%w: ledger state: %v", errStoredLedgerUnavailable, err)
	}
	return loaded, err
}

func storedHeaderMatchesInfo(h header.LedgerHeader, info *relationaldb.LedgerInfo) bool {
	return info != nil &&
		h.Hash == [32]byte(info.Hash) && h.LedgerIndex == uint32(info.Sequence) &&
		h.AccountHash == [32]byte(info.AccountHash) && h.TxHash == [32]byte(info.TransactionHash) &&
		h.ParentHash == [32]byte(info.ParentHash) && h.Drops == uint64(info.TotalCoins) &&
		h.CloseTime.Equal(info.CloseTime) && h.ParentCloseTime.Equal(info.ParentCloseTime) &&
		uint32(h.CloseTimeResolution) == uint32(info.CloseTimeRes) && h.CloseFlags == uint8(info.CloseFlags) &&
		header.CalculateHash(h) == h.Hash
}

func storedLedgerFees(ctx context.Context, stateMap *shamap.SHAMap, xrpFeesEnabled bool, fees drops.Fees) (drops.Fees, error) {
	item, found, err := stateMap.GetContext(ctx, keylet.Fees().Key)
	if err != nil {
		return drops.Fees{}, fmt.Errorf("read stored fee settings: %w", err)
	}
	if !found || item == nil {
		return fees, nil
	}
	settings, err := state.ParseFeeSettings(item.Data())
	if err != nil {
		return drops.Fees{}, fmt.Errorf("parse stored fee settings: %w", err)
	}
	if settings.IsUsingModernFees() && !xrpFeesEnabled {
		return drops.Fees{}, fmt.Errorf("%w: XRPFees fields are present before the amendment is enabled", errStoredLedgerUnavailable)
	}
	return mergeFeeSettings(fees, settings), nil
}

func mergeFeeSettings(fees drops.Fees, settings *state.FeeSettings) drops.Fees {
	if settings.HasBaseFeeDrops {
		fees.Base = drops.XRPAmount(settings.BaseFeeDrops)
	}
	if settings.HasReserveBaseDrops {
		fees.Reserve = drops.XRPAmount(settings.ReserveBaseDrops)
	}
	if settings.HasReserveIncrementDrops {
		fees.Increment = drops.XRPAmount(settings.ReserveIncrementDrops)
	}
	if settings.HasBaseFee {
		fees.Base = drops.XRPAmount(settings.BaseFee)
	}
	if settings.HasReserveBase {
		fees.Reserve = drops.XRPAmount(settings.ReserveBase)
	}
	if settings.HasReserveIncrement {
		fees.Increment = drops.XRPAmount(settings.ReserveIncrement)
	}
	return fees
}

func (s *Service) verifyStoredSHAMap(ctx context.Context, root [32]byte, mapType shamap.Type) error {
	_, err := s.verifyStoredSHAMapMeasured(ctx, root, mapType)
	return err
}

type storedSHAMapVerificationMetrics struct {
	nodes   uint64
	elapsed time.Duration
}

func (s *Service) verifyStoredSHAMapMeasured(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
) (storedSHAMapVerificationMetrics, error) {
	startedAt := time.Now()
	ticker := time.NewTicker(storedSHAMapVerificationLogInterval)
	defer ticker.Stop()
	var metrics storedSHAMapVerificationMetrics
	err := s.verifyStoredSHAMapWithTicksReport(
		ctx, root, mapType, startedAt, time.Now, ticker.C,
		func(result storedSHAMapVerificationMetrics) { metrics = result },
	)
	return metrics, err
}

func (s *Service) verifyStoredSHAMapWithTicks(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	startedAt time.Time,
	now func() time.Time,
	ticks <-chan time.Time,
) (err error) {
	return s.verifyStoredSHAMapWithTicksReport(ctx, root, mapType, startedAt, now, ticks, nil)
}

func (s *Service) verifyStoredSHAMapWithTicksReport(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	startedAt time.Time,
	now func() time.Time,
	ticks <-chan time.Time,
	report func(storedSHAMapVerificationMetrics),
) (err error) {
	progress := newStoredSHAMapVerificationProgress(s.logger, s.nodeStore, root, mapType, startedAt)
	defer func() {
		finishedAt := now()
		progress.finish(finishedAt, err)
		if err == nil && report != nil {
			elapsed := finishedAt.Sub(startedAt)
			if elapsed < 0 {
				elapsed = 0
			}
			report(storedSHAMapVerificationMetrics{
				nodes: progress.nodesChecked.Load(), elapsed: elapsed,
			})
		}
	}()

	// The strict startup walk proves these durable subtrees complete. Retain
	// shallow proofs and publish them only after the whole traversal succeeds,
	// so the first inbound-ledger missing-node walk can skip the shared stored
	// state instead of reading it all again. A concurrent generation reset makes
	// Insert reject these old-generation proofs.
	proofs := newStoredSHAMapProofs(s.shamapFamily)
	control := storedSHAMapWalkControl{progress: progress, progressTicks: ticks, now: now}
	if batchFetch := s.storedSHAMapVerificationBatchFetch(); batchFetch != nil {
		control.batchFetch = batchFetch
		control.batchNodes = storedSHAMapVerificationBatchNodes
		control.batchBytes = storedSHAMapVerificationBatchBytes
	}
	err = s.walkStoredSHAMapConcurrentWithFetch(
		ctx,
		root,
		mapType,
		s.storedSHAMapVerificationFetch(),
		resolveStoredSHAMapWorkers(s.config.FastLoadWorkers),
		control,
		proofs.record,
	)
	if err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		} else {
			proofs.publish()
		}
	}
	return err
}

type storedSHAMapProofs struct {
	cache      *shamap.FullBelowCache
	generation uint32
	mu         sync.Mutex
	hashes     [][32]byte
}

func newStoredSHAMapProofs(family shamap.Family) *storedSHAMapProofs {
	proofs := &storedSHAMapProofs{}
	provider, ok := family.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	if !ok || provider.FullBelowCache() == nil {
		return proofs
	}
	proofs.cache = provider.FullBelowCache()
	proofs.generation = proofs.cache.Generation()
	return proofs
}

func (p *storedSHAMapProofs) record(node storedSHAMapNode) {
	if p.cache == nil || node.depth > shamap.FullBelowCacheMaxDepth {
		return
	}
	p.mu.Lock()
	p.hashes = append(p.hashes, node.hash)
	p.mu.Unlock()
}

func (p *storedSHAMapProofs) publish() {
	if p.cache == nil {
		return
	}
	p.mu.Lock()
	hashes := p.hashes
	p.hashes = nil
	p.mu.Unlock()
	for _, hash := range hashes {
		p.cache.Insert(p.generation, hash)
	}
}

type storedSHAMapNode struct {
	hash  [32]byte
	depth int
}

type storedSHAMapTask struct {
	node   storedSHAMapNode
	branch int
}

type storedSHAMapFetch func(context.Context, nodestore.Hash256) (*nodestore.Node, error)
type storedSHAMapBatchFetch func(
	context.Context,
	[]nodestore.Hash256,
	int,
) ([]*nodestore.Node, kvstore.PromotionStats, error)

const (
	maxStoredSHAMapWorkers             = 64
	maxOnlineDeleteRefreshWorkers      = 4
	storedSHAMapFrontierTasksPerWorker = 4
	storedSHAMapPromotionBatchNodes    = 256
	storedSHAMapPromotionBatchBytes    = 4 << 20
	storedSHAMapVerificationBatchNodes = 256
	storedSHAMapVerificationBatchBytes = 4 << 20
)

type storedSHAMapWalkControl struct {
	progress        *storedSHAMapVerificationProgress
	progressTicks   <-chan time.Time
	checkpoint      func(context.Context, time.Duration) error
	checkpointTicks <-chan time.Time
	now             func() time.Time
	batchFetch      storedSHAMapBatchFetch
	batchNodes      int
	batchBytes      int
}

func resolveStoredSHAMapWorkers(configured int) int {
	if configured <= 0 {
		configured = runtime.GOMAXPROCS(0)
	}
	return min(configured, maxStoredSHAMapWorkers)
}

func resolveOnlineDeleteRefreshWorkers() int {
	return min(runtime.GOMAXPROCS(0), maxOnlineDeleteRefreshWorkers)
}

func (s *Service) walkStoredSHAMapConcurrentWithFetch(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
	workers int,
	control storedSHAMapWalkControl,
	visit func(storedSHAMapNode),
) error {
	if root == ([32]byte{}) {
		return fmt.Errorf("zero root")
	}
	if workers <= 0 {
		workers = 1
	}
	if control.now == nil {
		control.now = time.Now
	}

	walkCtx, cancel := context.WithCancelCause(ctx)
	var cancelOnce sync.Once
	var checkpointGate sync.RWMutex
	workStartedAt := control.now()
	var lastCheckpointNodes uint64
	controlledFetch := fetch
	controlledBatchFetch := control.batchFetch
	if control.checkpoint != nil {
		controlledFetch = func(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
			checkpointGate.RLock()
			defer checkpointGate.RUnlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return fetch(ctx, hash)
		}
		if controlledBatchFetch != nil {
			batchFetch := controlledBatchFetch
			controlledBatchFetch = func(
				ctx context.Context,
				hashes []nodestore.Hash256,
				maxBytes int,
			) ([]*nodestore.Node, kvstore.PromotionStats, error) {
				checkpointGate.RLock()
				defer checkpointGate.RUnlock()
				if err := ctx.Err(); err != nil {
					return nil, kvstore.PromotionStats{}, err
				}
				return batchFetch(ctx, hashes, maxBytes)
			}
		}
	}

	runCheckpoint := func() {
		if walkCtx.Err() != nil {
			return
		}
		checkpointGate.Lock()
		defer checkpointGate.Unlock()
		if walkCtx.Err() != nil {
			return
		}
		checkpointAt := control.now()
		work := checkpointAt.Sub(workStartedAt)
		if work < 0 {
			work = 0
		}
		visited := control.progress.nodesChecked.Load()
		if work < refreshHealthCheckPeriod &&
			visited-lastCheckpointNodes < refreshHealthCheckInterval {
			return
		}
		if checkpointErr := control.checkpoint(walkCtx, work); checkpointErr != nil {
			cancelOnce.Do(func() { cancel(checkpointErr) })
			return
		}
		lastCheckpointNodes = visited
		workStartedAt = control.now()
	}

	type checkpointRequest struct {
		done chan struct{}
	}
	var checkpointRequests chan checkpointRequest
	var checkpointDone chan struct{}
	if control.checkpoint != nil {
		checkpointRequests = make(chan checkpointRequest)
		checkpointDone = make(chan struct{})
		go func() {
			defer close(checkpointDone)
			ticks := control.checkpointTicks
			for {
				select {
				case request := <-checkpointRequests:
					runCheckpoint()
					close(request.done)
				case _, open := <-ticks:
					if !open {
						ticks = nil
						continue
					}
					runCheckpoint()
				case <-walkCtx.Done():
					return
				}
			}
		}()
	}
	defer func() {
		cancel(nil)
		if checkpointDone != nil {
			<-checkpointDone
		}
	}()
	requestCheckpoint := func() {
		if checkpointRequests == nil || walkCtx.Err() != nil {
			return
		}
		request := checkpointRequest{done: make(chan struct{})}
		select {
		case checkpointRequests <- request:
		case <-walkCtx.Done():
			return
		}
		select {
		case <-request.done:
		case <-walkCtx.Done():
		}
	}
	onNode := func(node storedSHAMapNode) {
		visited := control.progress.nodesChecked.Add(1)
		if visit != nil {
			visit(node)
		}
		if checkpointRequests != nil && visited%refreshHealthCheckInterval == 0 {
			requestCheckpoint()
		}
	}

	rootNode, _, err := s.loadStoredSHAMapNodeWithFetch(
		walkCtx,
		storedSHAMapNode{hash: root},
		mapType,
		controlledFetch,
	)
	if err != nil {
		if cause := context.Cause(walkCtx); cause != nil {
			return cause
		}
		return err
	}
	onNode(storedSHAMapNode{hash: root})
	inner, ok := rootNode.(shamap.InnerNodeReader)
	if !ok {
		return fmt.Errorf("root node %x is not an inner node", root[:8])
	}

	branches := make([][32]byte, 0, shamap.BranchFactor)
	for branch := range shamap.BranchFactor {
		if inner.IsEmptyBranch(branch) {
			continue
		}
		child, childErr := inner.ChildHash(branch)
		if childErr != nil {
			return childErr
		}
		branches = append(branches, child)
	}
	control.progress.branchesTotal = uint32(len(branches))
	control.progress.configureWorkers(workers, 0, len(branches))
	frontier, outstanding, err := s.buildStoredSHAMapFrontier(
		walkCtx,
		branches,
		workers*storedSHAMapFrontierTasksPerWorker,
		mapType,
		controlledFetch,
		onNode,
	)
	for _, count := range outstanding {
		if count == 0 {
			control.progress.branchesComplete.Add(1)
		}
	}
	if err != nil {
		if cause := context.Cause(walkCtx); cause != nil {
			return cause
		}
		return err
	}

	startedWorkers := min(workers, len(frontier))
	control.progress.configureWorkers(workers, startedWorkers, len(frontier))
	control.progress.start()
	if len(frontier) == 0 {
		requestCheckpoint()
		return context.Cause(walkCtx)
	}

	branchOutstanding := make([]atomic.Int64, len(outstanding))
	for branch, count := range outstanding {
		branchOutstanding[branch].Store(int64(count))
	}
	tasks := make(chan storedSHAMapTask, len(frontier))
	for _, task := range frontier {
		tasks <- task
	}
	close(tasks)

	var workersGroup sync.WaitGroup
	for range startedWorkers {
		workersGroup.Add(1)
		go func() {
			defer workersGroup.Done()
			for {
				var task storedSHAMapTask
				var open bool
				select {
				case task, open = <-tasks:
					if !open {
						return
					}
				case <-walkCtx.Done():
					return
				}

				control.progress.frontierSize.Add(-1)
				control.progress.activeWorkers.Add(1)
				var unreportedNodes uint64
				walk := s.walkStoredSHAMapNodesWithFetch
				if controlledBatchFetch != nil {
					walk = func(
						ctx context.Context,
						stack []storedSHAMapNode,
						mapType shamap.Type,
						_ storedSHAMapFetch,
						visit func(storedSHAMapNode, *nodestore.Node) error,
					) error {
						return s.walkStoredSHAMapNodesWithBatchFetch(
							ctx,
							stack,
							mapType,
							controlledBatchFetch,
							control.batchNodes,
							control.batchBytes,
							visit,
						)
					}
				}
				walkErr := walk(
					walkCtx,
					[]storedSHAMapNode{task.node},
					mapType,
					controlledFetch,
					func(node storedSHAMapNode, _ *nodestore.Node) error {
						if control.checkpoint != nil {
							onNode(node)
							return nil
						}
						unreportedNodes++
						if visit != nil {
							visit(node)
						}
						if unreportedNodes == storedSHAMapNodeCountBatch {
							control.progress.nodesChecked.Add(unreportedNodes)
							unreportedNodes = 0
						}
						return nil
					},
				)
				if unreportedNodes > 0 {
					control.progress.nodesChecked.Add(unreportedNodes)
				}
				control.progress.activeWorkers.Add(-1)
				if walkErr != nil {
					cancelOnce.Do(func() { cancel(walkErr) })
					return
				}
				if branchOutstanding[task.branch].Add(-1) == 0 {
					control.progress.branchesComplete.Add(1)
				}
			}
		}()
	}
	workersDone := make(chan struct{})
	go func() {
		workersGroup.Wait()
		close(workersDone)
	}()

	progressTicks := control.progressTicks
	ctxDone := ctx.Done()
	for {
		select {
		case tick, open := <-progressTicks:
			if !open {
				progressTicks = nil
				continue
			}
			select {
			case <-workersDone:
				requestCheckpoint()
				return context.Cause(walkCtx)
			default:
			}
			control.progress.report(tick)
		case <-ctxDone:
			ctxDone = nil
			cause := context.Cause(ctx)
			if cause == nil {
				cause = ctx.Err()
			}
			cancelOnce.Do(func() { cancel(cause) })
		case <-workersDone:
			requestCheckpoint()
			return context.Cause(walkCtx)
		}
	}
}

func (s *Service) storedSHAMapVerificationFetch() storedSHAMapFetch {
	uncached, ok := s.nodeStore.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	})
	if !ok {
		return s.nodeStore.Fetch
	}
	return func(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
		data, err := uncached.FetchDataUncached(ctx, hash)
		if err != nil || data == nil {
			return nil, err
		}
		return &nodestore.Node{Hash: hash, Data: data}, nil
	}
}

func (s *Service) storedSHAMapVerificationBatchFetch() storedSHAMapBatchFetch {
	uncached, ok := s.nodeStore.(interface {
		FetchBatchUncached(context.Context, []nodestore.Hash256, int, int) ([]*nodestore.Node, error)
	})
	if !ok {
		return nil
	}
	return func(
		ctx context.Context,
		hashes []nodestore.Hash256,
		maxBytes int,
	) ([]*nodestore.Node, kvstore.PromotionStats, error) {
		nodes, err := uncached.FetchBatchUncached(ctx, hashes, len(hashes), maxBytes)
		return nodes, kvstore.PromotionStats{}, err
	}
}

func (s *Service) buildStoredSHAMapFrontier(
	ctx context.Context,
	branches [][32]byte,
	target int,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
	visit func(storedSHAMapNode),
) ([]storedSHAMapTask, []uint32, error) {
	splitRootBranches := target > storedSHAMapFrontierTasksPerWorker
	target = max(target, len(branches))
	frontier := make(
		[]storedSHAMapTask,
		0,
		target+len(branches)*(shamap.BranchFactor-1),
	)
	outstanding := make([]uint32, len(branches))
	for branch, hash := range branches {
		outstanding[branch] = 1
		frontier = append(frontier, storedSHAMapTask{
			node:   storedSHAMapNode{hash: hash, depth: 1},
			branch: branch,
		})
	}
	initialSplits := 0
	if splitRootBranches {
		initialSplits = len(branches)
	}
	for split := 0; len(frontier) > 0 && (split < initialSplits || len(frontier) < target); split++ {
		task := frontier[0]
		copy(frontier, frontier[1:])
		frontier = frontier[:len(frontier)-1]

		node, _, err := s.loadStoredSHAMapNodeWithFetch(ctx, task.node, mapType, fetch)
		if err != nil {
			return frontier, outstanding, err
		}
		var childBuffer [shamap.BranchFactor]storedSHAMapNode
		children, err := appendStoredSHAMapChildren(childBuffer[:0], task.node, node)
		if err != nil {
			return frontier, outstanding, err
		}
		if visit != nil {
			visit(task.node)
		}
		outstanding[task.branch]--
		for _, child := range children {
			frontier = append(frontier, storedSHAMapTask{
				node:   child,
				branch: task.branch,
			})
			outstanding[task.branch]++
		}
	}
	return frontier, outstanding, nil
}

func (s *Service) walkStoredSHAMap(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	visit func([32]byte, *nodestore.Node) error,
) error {
	return s.walkStoredSHAMapWithFetch(ctx, root, mapType, s.nodeStore.Fetch, visit)
}

func (s *Service) walkStoredSHAMapWithFetch(
	ctx context.Context,
	root [32]byte,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
	visit func([32]byte, *nodestore.Node) error,
) error {
	if root == ([32]byte{}) {
		return fmt.Errorf("zero root")
	}
	return s.walkStoredSHAMapNodesWithFetch(
		ctx,
		[]storedSHAMapNode{{hash: root}},
		mapType,
		fetch,
		func(node storedSHAMapNode, stored *nodestore.Node) error {
			if visit == nil {
				return nil
			}
			return visit(node.hash, stored)
		},
	)
}

func (s *Service) walkStoredSHAMapNodesWithFetch(
	ctx context.Context,
	stack []storedSHAMapNode,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
	visit func(storedSHAMapNode, *nodestore.Node) error,
) error {
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		pending := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node, stored, err := s.loadStoredSHAMapNodeWithFetch(ctx, pending, mapType, fetch)
		if err != nil {
			return err
		}
		stack, err = appendStoredSHAMapChildren(stack, pending, node)
		if err != nil {
			return err
		}
		if visit != nil {
			if err := visit(pending, stored); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) walkStoredSHAMapNodesWithBatchFetch(
	ctx context.Context,
	stack []storedSHAMapNode,
	mapType shamap.Type,
	fetch storedSHAMapBatchFetch,
	maxNodes int,
	maxBytes int,
	visit func(storedSHAMapNode, *nodestore.Node) error,
) error {
	if maxNodes <= 0 {
		maxNodes = 1
	}
	pending := make([]storedSHAMapNode, 0, maxNodes)
	hashes := make([]nodestore.Hash256, 0, maxNodes)
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		count := min(len(stack), maxNodes)
		pending = pending[:count]
		for i := range count {
			pending[i] = stack[len(stack)-1-i]
		}
		stack = stack[:len(stack)-count]
		sort.SliceStable(pending, func(i, j int) bool {
			return bytes.Compare(pending[i].hash[:], pending[j].hash[:]) < 0
		})
		hashes = hashes[:count]
		for i := range pending {
			hashes[i] = nodestore.Hash256(pending[i].hash)
		}
		nodes, _, err := fetch(ctx, hashes, maxBytes)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(nodes) <= 0 || len(nodes) > len(pending) {
			return fmt.Errorf("invalid batch fetch result: returned %d of %d nodes", len(nodes), len(pending))
		}
		for i := len(pending) - 1; i >= len(nodes); i-- {
			stack = append(stack, pending[i])
		}
		for i, stored := range nodes {
			node, err := validateStoredSHAMapNode(pending[i], mapType, stored)
			if err != nil {
				return err
			}
			stack, err = appendStoredSHAMapChildren(stack, pending[i], node)
			if err != nil {
				return err
			}
			if visit != nil {
				if err := visit(pending[i], stored); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func appendStoredSHAMapChildren(
	children []storedSHAMapNode,
	pending storedSHAMapNode,
	node shamap.NodeReader,
) ([]storedSHAMapNode, error) {
	inner, ok := node.(shamap.InnerNodeReader)
	if !ok {
		return children, nil
	}
	if pending.depth >= 64 {
		return nil, fmt.Errorf("inner node %x exceeds maximum depth", pending.hash[:8])
	}
	for branch := range shamap.BranchFactor {
		if inner.IsEmptyBranch(branch) {
			continue
		}
		child, err := inner.ChildHash(branch)
		if err != nil {
			return nil, err
		}
		children = append(children, storedSHAMapNode{
			hash:  child,
			depth: pending.depth + 1,
		})
	}
	return children, nil
}

func (s *Service) loadStoredSHAMapNode(
	ctx context.Context,
	pending storedSHAMapNode,
	mapType shamap.Type,
) (shamap.NodeReader, *nodestore.Node, error) {
	return s.loadStoredSHAMapNodeWithFetch(ctx, pending, mapType, s.nodeStore.Fetch)
}

func (s *Service) loadStoredSHAMapNodeWithFetch(
	ctx context.Context,
	pending storedSHAMapNode,
	mapType shamap.Type,
	fetch storedSHAMapFetch,
) (shamap.NodeReader, *nodestore.Node, error) {
	stored, err := fetch(ctx, nodestore.Hash256(pending.hash))
	if err != nil {
		return nil, nil, err
	}
	if stored == nil {
		return nil, nil, fmt.Errorf("node %x is missing", pending.hash[:8])
	}
	node, err := validateStoredSHAMapNode(pending, mapType, stored)
	return node, stored, err
}

func validateStoredSHAMapNode(
	pending storedSHAMapNode,
	mapType shamap.Type,
	stored *nodestore.Node,
) (shamap.NodeReader, error) {
	if stored == nil {
		return nil, fmt.Errorf("node %x is missing", pending.hash[:8])
	}
	node, err := shamap.DeserializeFromPrefix(stored.Data)
	if err != nil {
		return nil, err
	}
	if node.Hash() != pending.hash {
		return nil, fmt.Errorf("node %x has invalid content hash", pending.hash[:8])
	}
	if _, inner := node.(shamap.InnerNodeReader); inner {
		return node, nil
	}
	if pending.depth == 0 {
		return nil, fmt.Errorf("root node %x is not an inner node", pending.hash[:8])
	}
	if mapType == shamap.TypeState && node.Type() != shamap.NodeTypeAccountState {
		return nil, fmt.Errorf("state tree contains %s leaf", node.Type())
	}
	if mapType == shamap.TypeTransaction &&
		node.Type() != shamap.NodeTypeTransactionNoMeta &&
		node.Type() != shamap.NodeTypeTransactionWithMeta {
		return nil, fmt.Errorf("transaction tree contains %s leaf", node.Type())
	}
	return node, nil
}
