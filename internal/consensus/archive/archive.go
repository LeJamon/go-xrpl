// Package archive persists stale validations to the relational DB via a
// batched async writer hooked into ValidationTracker.SetOnStale.
//
// Matches rippled's onStale / doStaleWrite contract (RCLValidations.cpp)
// in semantics: OnStale returns in O(1) — it only enqueues to a channel —
// so the consensus receive path is never gated on DB I/O.
//
// # Eviction trigger: ledger-seq, not freshness (intentional divergence)
//
// Rippled (pre-2019) fired onStale from Validations<>::current() when
// isCurrent() returned false — i.e. on time-window violations
// (SignTime/SeenTime drifted outside the wall/local windows). goXRPL
// fires onStale only from ExpireOld(seq - inMemoryLedgers), which is
// driven by ledger-seq retention from the fully-validated callback.
//
// Practical consequence: time-stale validations that never reach a
// fully-validated ledger (e.g. orphaned validations on losing forks)
// are rejected at ValidationTracker.Add (isCurrent check at
// validations.go:291) but are NOT archived. Rippled would have archived
// them.
//
// Trade-off:
//   - Forensic completeness for finalized network state: same as rippled.
//   - Forensic completeness for "what did the network consider but
//     discard": slightly lower — losing-fork validations are visible
//     only in logs, not in the archive.
//
// Defensible because the dominant use case is replaying finalized state,
// and the divergence avoids archiving orphans we'd just delete on the
// next retention sweep. If "considered but discarded" forensics ever
// becomes a requirement, hooking a second OnStale call from Add's
// isCurrent reject path is straightforward.
package archive

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
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

// Archive is the async writer. Safe for concurrent OnStale from any
// goroutine once New has returned.
type Archive struct {
	repo   relationaldb.ValidationRepository
	cfg    Config
	logger *slog.Logger

	ch       chan *consensus.Validation
	flushReq chan chan struct{} // ack channel per flush request
	stop     chan struct{}
	wg       sync.WaitGroup
	flushed  chan struct{} // closed when the writer goroutine exits

	lastSeq atomic.Uint32 // most-recent fully-validated seq, used as retention pivot

	closed atomic.Bool
}

// New creates a running archive. repo may be nil — that turns OnStale into
// a no-op and nothing is ever written. ch buffer is BatchSize*8 so a
// moderate burst of stale validations never blocks the caller.
func New(repo relationaldb.ValidationRepository, cfg Config, logger *slog.Logger) *Archive {
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

	a := &Archive{
		repo:     repo,
		cfg:      cfg,
		logger:   logger,
		ch:       make(chan *consensus.Validation, cfg.BatchSize*8),
		flushReq: make(chan chan struct{}, 4),
		stop:     make(chan struct{}),
		flushed:  make(chan struct{}),
	}
	if repo != nil {
		a.wg.Add(1)
		go a.run()
	} else {
		close(a.flushed)
	}
	return a
}

// OnStale is the ValidationTracker.SetOnStale callback. Non-blocking when
// the channel has space; falls through a small bounded blocking wait
// (matches rippled's vector-append semantics) so under sustained overload
// we apply back-pressure instead of silently dropping validations.
func (a *Archive) OnStale(v *consensus.Validation) {
	if a == nil || a.repo == nil || v == nil || a.closed.Load() {
		return
	}
	select {
	case a.ch <- v:
		return
	default:
	}
	// Fall back to a bounded blocking send — 100ms is an eternity at
	// consensus-message timescales but still lets the caller progress if
	// the writer goroutine is wedged.
	t := time.NewTimer(100 * time.Millisecond)
	defer t.Stop()
	select {
	case a.ch <- v:
	case <-t.C:
		a.logger.Warn("validation archive channel full; dropping stale validation",
			slog.Uint64("ledger_seq", uint64(v.LedgerSeq)))
	case <-a.stop:
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

// Flush blocks until every stale validation enqueued before the call has
// been committed. Returns ErrClosed if called after Close so callers
// notice attempts to flush a stopped archive (the previous silent-nil
// behaviour hid bugs); returns nil for a nil receiver or a nil-repo
// archive (those are configured no-ops, not error states).
//
// Implemented as an ack-channel barrier: we send an ack channel on
// flushReq AFTER every previously-enqueued OnStale has landed in ch
// (FIFO on the same goroutine ordering), and the writer loop closes the
// ack only after it has fully drained ch up to that point and committed
// the resulting batch.
func (a *Archive) Flush(ctx context.Context) error {
	if a == nil || a.repo == nil {
		return nil
	}
	if a.closed.Load() {
		return ErrClosed
	}
	ack := make(chan struct{})
	select {
	case a.flushReq <- ack:
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stop:
		return nil
	}
	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stop:
		return nil
	}
}

// ApplyRetention performs a one-shot retention sweep against the current
// NotedFullyValidated seq. Intended for tests and manual admin flushes;
// the writer loop calls it implicitly after each batch commit.
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

// Close drains pending validations, commits the final batch, and stops
// the writer goroutine. Safe to call multiple times; subsequent calls
// return nil immediately.
func (a *Archive) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if !a.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(a.stop)
	select {
	case <-a.flushed:
	case <-ctx.Done():
		return ctx.Err()
	}
	a.wg.Wait()
	return nil
}

// retentionMinInterval is the minimum wall-clock gap between two
// retention sweeps. Decoupling retention from the per-batch flush rate
// caps DELETE pressure under sustained load: with BatchSize=128 and
// FlushInterval=1s the writer can commit every second, but retention
// only runs ~once per minute. The bounded DeleteBatch keeps each sweep
// cheap, so the archive size still tracks RetentionLedgers within a
// minute of any seq advance.
const retentionMinInterval = time.Minute

// saveBatchMaxAttempts is how many times the writer retries a failed
// SaveBatch before logging and dropping the batch. One retry catches
// transient lock contention / connection blips; persistent failure
// (disk full, schema drift) gets logged at Error level so the operator
// notices, then dropped to avoid unbounded memory growth.
const saveBatchMaxAttempts = 2

// run is the writer goroutine. It accumulates up to BatchSize rows or
// waits FlushInterval, whichever comes first, then commits. Retention
// runs after every non-empty commit BUT only if at least
// retentionMinInterval has elapsed since the last sweep — see comment
// on retentionMinInterval for why per-flush retention is too aggressive.
func (a *Archive) run() {
	defer a.wg.Done()
	defer close(a.flushed)

	ticker := time.NewTicker(a.cfg.FlushInterval)
	defer ticker.Stop()

	var lastRetention time.Time

	batch := make([]*relationaldb.ValidationRecord, 0, a.cfg.BatchSize)
	flush := func(reason string) {
		if len(batch) == 0 {
			return
		}

		// Retry-once on SaveBatch error. Most failures are transient
		// (SQLite SQLITE_BUSY under contention, brief Postgres
		// disconnects) and a single 50ms backoff clears them.
		var err error
		for attempt := 1; attempt <= saveBatchMaxAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = a.repo.SaveBatch(ctx, batch)
			cancel()
			if err == nil {
				break
			}
			if attempt < saveBatchMaxAttempts {
				a.logger.Warn("validation archive: batch write failed; retrying",
					slog.Int("rows", len(batch)), slog.Int("attempt", attempt), slog.String("err", err.Error()))
				time.Sleep(50 * time.Millisecond)
			}
		}
		if err != nil {
			// Persistent failure: log at Error so operators notice, then
			// drop the batch. Re-queueing indefinitely would let memory
			// grow without bound on a permanently broken backend, which
			// is strictly worse than visible data loss with a paper
			// trail.
			a.logger.Error("validation archive: batch write failed permanently; dropping rows",
				slog.Int("rows", len(batch)),
				slog.Int("attempts", saveBatchMaxAttempts),
				slog.String("reason", reason),
				slog.String("err", err.Error()))
		} else {
			a.logger.Debug("validation archive: batch committed",
				slog.Int("rows", len(batch)), slog.String("reason", reason))
		}
		batch = batch[:0]

		// Retention sweep — gated on retentionMinInterval to keep
		// DELETE pressure off the hot path under sustained flush
		// activity. Errors are logged but don't stop the writer.
		if a.cfg.RetentionLedgers > 0 && time.Since(lastRetention) >= retentionMinInterval {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, rerr := a.ApplyRetention(ctx)
			cancel()
			lastRetention = time.Now()
			if rerr != nil {
				a.logger.Warn("validation archive: retention sweep failed",
					slog.String("err", rerr.Error()))
			}
		}
	}

	drainPending := func() {
		for {
			select {
			case v := <-a.ch:
				if v == nil {
					continue
				}
				rec := toRecord(v, a.lastSeq.Load())
				if rec == nil {
					continue
				}
				batch = append(batch, rec)
			default:
				return
			}
		}
	}

	handleFlushReq := func(ack chan struct{}) {
		// Drain everything enqueued before flush was requested. Flush
		// is called from outside run, so anything a caller enqueued
		// BEFORE sending the flushReq is already on ch by the time
		// flushReq arrives (Go channel operations are happens-before
		// synchronized). Drain to empty to pick it all up.
		drainPending()
		flush("flush-request")
		close(ack)
	}

	for {
		select {
		case v, ok := <-a.ch:
			if !ok {
				flush("channel closed")
				return
			}
			if v == nil {
				continue
			}
			rec := toRecord(v, a.lastSeq.Load())
			if rec == nil {
				continue
			}
			batch = append(batch, rec)
			if len(batch) >= a.cfg.BatchSize {
				flush("full")
			}
		case ack := <-a.flushReq:
			handleFlushReq(ack)
		case <-ticker.C:
			flush("tick")
		case <-a.stop:
			drainPending()
			// Service any pending flush requests so Close-then-Flush
			// callers aren't left hanging.
			for {
				select {
				case ack := <-a.flushReq:
					close(ack)
				default:
					flush("close")
					return
				}
			}
		}
	}
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
// Non-Full validations are filtered upstream at ValidationTracker.Add
// (validations.go:297) — they never enter the tracker, so the OnStale
// stream never carries them. We don't re-filter here; rippled's
// historical doStaleWrite filter is therefore moot for goXRPL.
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
		NodePubKey: append([]byte(nil), v.NodeID[:]...),
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

// ErrClosed is returned by callers that try to use an Archive after Close.
var ErrClosed = errors.New("archive: closed")
