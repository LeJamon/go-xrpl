// Package archive persists validations evicted from the in-memory tracker.
// It is a go-xrpl operational extension inspired by rippled's historical
// validation database, which was removed before rippled v3.2.0.
package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// Config tunes the archive writer. Validated ranges match config.ValidationArchiveConfig.
type Config struct {
	// RetentionLedgers is how many ledger-seqs of validation history to
	// keep. Zero disables retention (keep forever).
	RetentionLedgers uint32
	// BatchSize caps accumulated rows before forcing a commit.
	BatchSize int
	// FlushInterval bounds how long a partial batch may wait.
	FlushInterval time.Duration
	// DeleteBatch caps a single retention-sweep DELETE.
	DeleteBatch int
}

type archiveState uint8

const (
	archiveRunning archiveState = iota
	archiveClosing
	archiveClosed
)

type flushRequest struct {
	result chan error
}

type errorState struct {
	err error
}

// Health is a cumulative snapshot of archive admission and maintenance.
type Health struct {
	Enqueued           uint64
	OverloadDropped    uint64
	ClosedDropped      uint64
	MalformedDropped   uint64
	PersistenceDropped uint64
	WriteFailures      uint64
	RetentionFailures  uint64
	LastError          string
	Healthy            bool
}

// Archive is the async writer. Safe for concurrent use.
type Archive struct {
	repo   relationaldb.ValidationRepository
	cfg    Config
	logger *slog.Logger

	ch                chan *consensus.Validation
	flushWake         chan struct{}
	retentionWake     chan struct{}
	retentionTick     <-chan time.Time
	retentionTimeout  time.Duration
	stopTicker        func()
	stop              chan struct{}
	done              chan struct{}
	runCtx            context.Context
	cancelRun         context.CancelCauseFunc
	maintenanceCtx    context.Context
	cancelMaintenance context.CancelFunc

	stateMu     sync.Mutex
	state       archiveState
	flushes     []*flushRequest
	terminalErr error

	lastSeq atomic.Uint32 // most-recent fully-validated seq, used as retention pivot

	enqueued           atomic.Uint64
	overloadDropped    atomic.Uint64
	closedDropped      atomic.Uint64
	malformedDropped   atomic.Uint64
	persistenceDropped atomic.Uint64
	writeFailures      atomic.Uint64
	retentionFailures  atomic.Uint64
	lastError          atomic.Pointer[errorState]
	lastOverloadLog    atomic.Int64
	lastMalformedLog   atomic.Int64
}

// New creates a running archive. repo may be nil — that turns OnStale into
// a no-op and nothing is ever written. The channel holds at least 64 rows and
// otherwise scales with BatchSize so moderate bursts do not immediately shed.
func New(repo relationaldb.ValidationRepository, cfg Config, logger *slog.Logger) *Archive {
	return newArchive(repo, cfg, logger, nil)
}

func newArchive(
	repo relationaldb.ValidationRepository,
	cfg Config,
	logger *slog.Logger,
	retentionTick <-chan time.Time,
) *Archive {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 128
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.DeleteBatch < 1 {
		cfg.DeleteBatch = 1000
	}
	channelCapacity := cfg.BatchSize * 8
	if channelCapacity < 64 {
		channelCapacity = 64
	}

	runCtx, cancelRun := context.WithCancelCause(context.Background())
	maintenanceCtx, cancelMaintenance := context.WithCancel(context.Background())
	a := &Archive{
		repo:              repo,
		cfg:               cfg,
		logger:            logger,
		ch:                make(chan *consensus.Validation, channelCapacity),
		flushWake:         make(chan struct{}, 1),
		retentionWake:     make(chan struct{}, 1),
		retentionTick:     retentionTick,
		retentionTimeout:  retentionSweepTimeout,
		stop:              make(chan struct{}),
		done:              make(chan struct{}),
		runCtx:            runCtx,
		cancelRun:         cancelRun,
		maintenanceCtx:    maintenanceCtx,
		cancelMaintenance: cancelMaintenance,
	}
	if repo == nil {
		a.state = archiveClosed
		cancelMaintenance()
		cancelRun(nil)
		close(a.done)
		return a
	}
	if cfg.RetentionLedgers > 0 && retentionTick == nil {
		ticker := time.NewTicker(retentionMinInterval)
		a.retentionTick = ticker.C
		a.stopTicker = ticker.Stop
	}
	go a.run()
	return a
}

// OnStale is the ValidationTracker.SetOnStale callback. It never waits for
// channel capacity or database I/O.
func (a *Archive) OnStale(v *consensus.Validation) {
	if a == nil || a.repo == nil || v == nil {
		return
	}

	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.state != archiveRunning {
		a.closedDropped.Add(1)
		return
	}
	select {
	case a.ch <- v:
		a.enqueued.Add(1)
	default:
		n := a.overloadDropped.Add(1)
		a.logDrop(
			&a.lastOverloadLog,
			"validation archive channel full; dropping stale validations",
			slog.Uint64("ledger_seq", uint64(v.LedgerSeq)),
			slog.Uint64("dropped_total", n),
		)
	}
}

// NoteFullyValidated informs the archive of the most recent fully-
// validated ledger seq. Used as the pivot for retention: rows with
// ledger_seq < (noted - RetentionLedgers) become eligible for deletion.
func (a *Archive) NoteFullyValidated(seq uint32) {
	if a == nil {
		return
	}
	for {
		cur := a.lastSeq.Load()
		if seq <= cur {
			return
		}
		if a.lastSeq.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// Flush waits until every validation admitted before its barrier has been
// processed. A persistence failure remains sticky because a later successful
// write cannot restore rows that were already lost.
func (a *Archive) Flush(ctx context.Context) error {
	if a == nil || a.repo == nil {
		return nil
	}

	req := &flushRequest{result: make(chan error, 1)}
	a.stateMu.Lock()
	if a.state != archiveRunning {
		a.stateMu.Unlock()
		return ErrClosed
	}
	a.flushes = append(a.flushes, req)
	select {
	case a.flushWake <- struct{}{}:
	default:
	}
	a.stateMu.Unlock()

	select {
	case err := <-req.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ApplyRetention performs one bounded retention delete. Automatic maintenance
// calls it repeatedly within a work budget; callers may use it for a
// synchronous administrative sweep.
func (a *Archive) ApplyRetention(ctx context.Context) (int64, error) {
	if a == nil || a.repo == nil || a.cfg.RetentionLedgers == 0 {
		return 0, nil
	}
	last := a.lastSeq.Load()
	if last <= a.cfg.RetentionLedgers {
		return 0, nil
	}
	cutoff := last - a.cfg.RetentionLedgers
	return a.repo.DeleteOlderThanSeq(ctx, relationaldb.LedgerIndex(cutoff), a.cfg.DeleteBatch)
}

// Close drains accepted validations and waits for the shared terminal result.
// If the initiating context expires, it cancels writer I/O; later callers can
// still wait for and observe the eventual terminal error.
func (a *Archive) Close(ctx context.Context) error {
	if a == nil || a.repo == nil {
		return nil
	}

	a.stateMu.Lock()
	if a.state == archiveClosed {
		err := a.terminalErr
		a.stateMu.Unlock()
		return err
	}
	first := a.state == archiveRunning
	if first {
		a.state = archiveClosing
		a.cancelMaintenance()
		close(a.stop)
	}
	a.stateMu.Unlock()

	var stopCancel func() bool
	if first {
		stopCancel = context.AfterFunc(ctx, func() {
			a.cancelRun(ctx.Err())
		})
	}

	select {
	case <-a.done:
		if stopCancel != nil {
			stopCancel()
		}
		return a.terminalResult()
	case <-ctx.Done():
		select {
		case <-a.done:
			if stopCancel != nil {
				stopCancel()
			}
			return a.terminalResult()
		default:
			if first {
				a.cancelRun(ctx.Err())
			}
			return ctx.Err()
		}
	}
}

const (
	retentionMinInterval  = time.Minute
	retentionSweepTimeout = 5 * time.Second
	retentionBatchBudget  = 8
	dropLogInterval       = time.Second
	writeTimeout          = 30 * time.Second
	writeRetryDelay       = 50 * time.Millisecond
)

// saveBatchMaxAttempts is the maximum number of write attempts for a failed
// SaveBatch before logging and dropping the batch. One retry catches
// transient lock contention / connection blips; persistent failure
// (disk full, schema drift) gets logged at Error level so the operator
// notices, then dropped to avoid unbounded memory growth.
const saveBatchMaxAttempts = 2

func (a *Archive) run() {
	ticker := time.NewTicker(a.cfg.FlushInterval)
	defer ticker.Stop()
	if a.stopTicker != nil {
		defer a.stopTicker()
	}

	batch := make([]*relationaldb.ValidationRecord, 0, a.cfg.BatchSize)
	var durabilityErr error

	noteDurabilityFailure := func(err error, rows int) {
		if err == nil {
			return
		}
		wrapped := fmt.Errorf("%w: %w", ErrDurability, err)
		if durabilityErr == nil {
			durabilityErr = wrapped
		}
		a.lastError.Store(&errorState{err: wrapped})
		a.writeFailures.Add(1)
		a.persistenceDropped.Add(uint64(rows))
	}

	flush := func(reason string) error {
		if len(batch) == 0 {
			return nil
		}

		rows := len(batch)
		var err error
		for attempt := 1; attempt <= saveBatchMaxAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(a.runCtx, writeTimeout)
			err = a.repo.SaveBatch(ctx, batch)
			cancel()
			if err == nil {
				break
			}
			if attempt < saveBatchMaxAttempts {
				a.logger.Warn("validation archive: batch write failed; retrying",
					slog.Int("rows", len(batch)), slog.Int("attempt", attempt), slog.String("err", err.Error()))
				timer := time.NewTimer(writeRetryDelay)
				select {
				case <-timer.C:
				case <-a.runCtx.Done():
					timer.Stop()
					err = context.Cause(a.runCtx)
					attempt = saveBatchMaxAttempts
				}
			}
		}
		if err != nil {
			a.logger.Error("validation archive: batch write failed permanently; dropping rows",
				slog.Int("rows", len(batch)),
				slog.Int("attempts", saveBatchMaxAttempts),
				slog.String("reason", reason),
				slog.String("err", err.Error()))
			noteDurabilityFailure(err, rows)
		} else {
			a.logger.Debug("validation archive: batch committed",
				slog.Int("rows", len(batch)), slog.String("reason", reason))
		}
		batch = batch[:0]
		return err
	}

	drainPending := func() {
		for {
			select {
			case v := <-a.ch:
				if v == nil {
					continue
				}
				a.appendRecord(&batch, v)
			default:
				return
			}
		}
	}

	handleFlushes := func() {
		requests := a.takeFlushes()
		drainPending()
		_ = flush("flush-request")
		for _, req := range requests {
			req.result <- durabilityErr
		}
	}

	for {
		select {
		case v := <-a.ch:
			if v == nil {
				continue
			}
			a.appendRecord(&batch, v)
			if len(batch) >= a.cfg.BatchSize {
				_ = flush("full")
			}
		case <-a.flushWake:
			handleFlushes()
		case <-ticker.C:
			_ = flush("tick")
		case <-a.retentionTick:
			a.applyRetentionBudget()
		case <-a.retentionWake:
			a.applyRetentionBudget()
		case <-a.stop:
			drainPending()
			_ = flush("close")
			for _, req := range a.takeFlushes() {
				req.result <- durabilityErr
			}
			a.finish(durabilityErr)
			return
		case <-a.runCtx.Done():
			drainPending()
			_ = flush("close-canceled")
			for _, req := range a.takeFlushes() {
				req.result <- durabilityErr
			}
			a.finish(durabilityErr)
			return
		}
	}
}

func (a *Archive) appendRecord(
	batch *[]*relationaldb.ValidationRecord,
	v *consensus.Validation,
) {
	rec := toRecord(v, a.lastSeq.Load())
	if rec != nil {
		*batch = append(*batch, rec)
		return
	}
	n := a.malformedDropped.Add(1)
	a.logDrop(
		&a.lastMalformedLog,
		"validation archive received validation without canonical raw bytes; dropping",
		slog.Uint64("ledger_seq", uint64(v.LedgerSeq)),
		slog.Uint64("dropped_total", n),
	)
}

func (a *Archive) takeFlushes() []*flushRequest {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	requests := a.flushes
	a.flushes = nil
	return requests
}

func (a *Archive) finish(err error) {
	a.stateMu.Lock()
	a.state = archiveClosed
	a.terminalErr = err
	a.stateMu.Unlock()
	a.cancelMaintenance()
	a.cancelRun(err)
	close(a.done)
}

func (a *Archive) terminalResult() error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.terminalErr
}

func (a *Archive) applyRetentionBudget() {
	if a.cfg.RetentionLedgers == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(a.maintenanceCtx, a.retentionTimeout)
	defer cancel()

	backlog := false
	for range retentionBatchBudget {
		n, err := a.ApplyRetention(ctx)
		if err != nil {
			if a.maintenanceCtx.Err() != nil {
				return
			}
			if backlog && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				a.wakeRetention()
				return
			}
			a.retentionFailures.Add(1)
			a.lastError.Store(&errorState{err: err})
			a.logger.Warn("validation archive: retention sweep failed", slog.String("err", err.Error()))
			return
		}
		backlog = n == int64(a.cfg.DeleteBatch)
		if !backlog {
			return
		}
	}
	if backlog {
		a.wakeRetention()
	}
}

func (a *Archive) wakeRetention() {
	select {
	case a.retentionWake <- struct{}{}:
	default:
	}
}

func (a *Archive) logDrop(lastLog *atomic.Int64, message string, attrs ...any) {
	now := time.Now().UnixNano()
	last := lastLog.Load()
	if now-last >= int64(dropLogInterval) && lastLog.CompareAndSwap(last, now) {
		a.logger.Warn(message, attrs...)
	}
}

// Health returns cumulative counters and the most recent maintenance error.
func (a *Archive) Health() Health {
	if a == nil {
		return Health{Healthy: true}
	}
	health := Health{
		Enqueued:           a.enqueued.Load(),
		OverloadDropped:    a.overloadDropped.Load(),
		ClosedDropped:      a.closedDropped.Load(),
		MalformedDropped:   a.malformedDropped.Load(),
		PersistenceDropped: a.persistenceDropped.Load(),
		WriteFailures:      a.writeFailures.Load(),
		RetentionFailures:  a.retentionFailures.Load(),
	}
	if state := a.lastError.Load(); state != nil && state.err != nil {
		health.LastError = state.err.Error()
	}
	health.Healthy = health.OverloadDropped == 0 &&
		health.ClosedDropped == 0 &&
		health.MalformedDropped == 0 &&
		health.PersistenceDropped == 0 &&
		health.WriteFailures == 0 &&
		health.RetentionFailures == 0
	return health
}

// toRecord marshals a Validation into the archive row shape. Returns nil
// on anything invalid so a single bad validation can't poison the batch.
//
// initialSeq is the most-recent fully-validated ledger seq AT THE TIME
// the row is committed — matching rippled's column comment ("the current
// ledger seq when the row is inserted; only relevant during online
// delete"). Pass 0 if no fully-validated pivot has been observed yet;
// the column then degenerates to LedgerSeq, which is harmless.
//
// Full and partial validations are both retained. The archive is forensic,
// and Flags preserves the distinction for readers.
func toRecord(v *consensus.Validation, initialSeq uint32) *relationaldb.ValidationRecord {
	if v == nil {
		return nil
	}

	raw := v.Raw
	if len(raw) == 0 {
		// All Validations reaching the tracker either come from the wire
		// (parseSTValidation populates Raw) or from our own signer
		// (ValidationToMessage populates Raw at broadcast time). A
		// missing Raw means a programming error upstream — log and skip
		// rather than re-serialize, which would silently mask the bug.
		return nil
	}

	if initialSeq == 0 {
		initialSeq = v.LedgerSeq
	}

	rec := &relationaldb.ValidationRecord{
		LedgerSeq:  relationaldb.LedgerIndex(v.LedgerSeq),
		InitialSeq: relationaldb.LedgerIndex(initialSeq),
		// Persist the 33-byte ephemeral signing pubkey: that is the
		// public key whose signature is in v.Signature, and the form
		// rippled stores for forensic queries. The 20-byte NodeID is
		// derivable from this via calcNodeID when needed.
		NodePubKey: append([]byte(nil), v.SigningPubKey[:]...),
		SignTime:   v.SignTime,
		SeenTime:   v.SeenTime,
		// Flags carries the original wire sfFlags word (parser fills
		// it from the inbound blob, signer fills it at sign time).
		// Forensic queries SELECT flags FROM validations therefore
		// read what the validator actually signed, not a synthesized
		// constant.
		Flags: v.Flags,
		Raw:   append([]byte(nil), raw...),
	}
	copy(rec.LedgerHash[:], v.LedgerID[:])
	return rec
}

var (
	// ErrClosed is returned by callers that try to flush a closing archive.
	ErrClosed = errors.New("archive: closed")
	// ErrDurability marks permanent loss of one or more admitted validations.
	ErrDurability = errors.New("archive: durability failure")
)
