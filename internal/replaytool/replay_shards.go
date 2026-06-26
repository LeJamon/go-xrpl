package replaytool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/cmdexit"
)

// ledgerSegment is one shard's half-open replay span: it seeds at `from` and
// replays (from, to]. Contiguous segments share boundaries — one shard's `to`
// is the next shard's `from` — so the whole range is covered without gaps or
// overlap.
type ledgerSegment struct {
	from uint32
	to   uint32
}

// partitionRange splits [from, to] into `shards` contiguous segments of as-equal
// length as possible. The boundaries are the seeds each shard loads, so they
// must be independently seedable ledgers (checkpoint seqs) — partitioning only
// decides where to cut, not whether the cut is seedable; an unseedable boundary
// fails that shard at seed-load time. Shards are capped at the block count so no
// segment is empty.
func partitionRange(from, to uint32, shards int) []ledgerSegment {
	if shards < 1 {
		shards = 1
	}
	total := to - from
	if total == 0 {
		return []ledgerSegment{{from: from, to: to}}
	}
	if uint32(shards) > total {
		shards = int(total)
	}

	segs := make([]ledgerSegment, 0, shards)
	base := total / uint32(shards)
	rem := total % uint32(shards)
	b := from
	for k := 0; k < shards; k++ {
		n := base
		if uint32(k) < rem {
			n++ // spread the remainder over the first `rem` shards
		}
		next := b + n
		segs = append(segs, ledgerSegment{from: b, to: next})
		b = next
	}
	return segs
}

// aggregateStats sums per-shard stats into one combined record. TotalDuration is
// left zero for the caller to set to the overall wall-clock (the shards overlap
// in time, so summing their durations would be meaningless). The earliest
// failing block across shards wins, mirroring a serial run stopping at the first
// failure.
func aggregateStats(parts []*RangeReplayStats) *RangeReplayStats {
	agg := &RangeReplayStats{}
	for _, p := range parts {
		if p == nil {
			continue
		}
		agg.BlocksProcessed += p.BlocksProcessed
		agg.BlocksSuccessful += p.BlocksSuccessful
		agg.TotalTransactions += p.TotalTransactions
		agg.Divergences += p.Divergences
		agg.FetchDuration += p.FetchDuration
		agg.ApplyDuration += p.ApplyDuration
		agg.FinalizeDuration += p.FinalizeDuration
		if p.FailedAtBlock > 0 && (agg.FailedAtBlock == 0 || p.FailedAtBlock < agg.FailedAtBlock) {
			agg.FailedAtBlock = p.FailedAtBlock
			agg.FailureReason = p.FailureReason
		}
	}
	return agg
}

// runSegmentsConcurrently runs fn for every segment on its own goroutine and
// collects each result by index, so callers never share a slot. fn must be
// safe to run concurrently — its per-shard state is private, and any shared
// sinks (findings writer, output) are concurrency-safe.
func runSegmentsConcurrently(
	ctx context.Context,
	segs []ledgerSegment,
	fn func(ctx context.Context, shard int, seg ledgerSegment) (*RangeReplayStats, error),
) ([]*RangeReplayStats, []error) {
	stats := make([]*RangeReplayStats, len(segs))
	errs := make([]error, len(segs))

	var wg sync.WaitGroup
	for i, seg := range segs {
		wg.Add(1)
		go func(i int, seg ledgerSegment) {
			defer wg.Done()
			stats[i], errs[i] = fn(ctx, i, seg)
		}(i, seg)
	}
	wg.Wait()
	return stats, errs
}

// shardWriter funnels one shard's output into a shared sink, prefixing each
// complete line with the shard tag and holding the shared mutex across the whole
// write so concurrent shards never interleave mid-line. Partial lines are
// buffered per shard until their newline arrives.
type shardWriter struct {
	mu     *sync.Mutex // shared across shards: serializes writes to w
	w      io.Writer
	prefix string
	buf    []byte // partial line for this shard only (guarded by mu)
}

func (s *shardWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		if _, err := io.WriteString(s.w, s.prefix); err != nil {
			return 0, err
		}
		if _, err := s.w.Write(s.buf[:i+1]); err != nil {
			return 0, err
		}
		s.buf = s.buf[i+1:]
	}
	return len(p), nil
}

// runSharded replays [from, to] as `shards` parallel segments. Each segment runs
// its own serial replay (own DB client and nodestore overlay) seeded from and
// account-hash-verified at its boundary, so the segments are independent; the
// only cross-block carry — the state SHAMap — is reproduced from each segment's
// verified seed. Stats are aggregated and findings are merged into one writer.
//
// Sharding requires per-segment seeds, so checkpoint/resume (a serial-run
// feature) is rejected here.
func (r *replayRangeRunner) runSharded(ctx context.Context) error {
	if r.resumeFrom > 0 {
		return fmt.Errorf("--resume-from is not supported with --shards (each shard seeds from its own segment boundary)")
	}
	if r.checkpointDir != "" {
		return fmt.Errorf("--checkpoint-dir is not supported with --shards")
	}

	segs := partitionRange(r.from, r.to, r.shards)

	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                XRPL Continuous State Replay (sharded)")
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "Range:   %d -> %d (%d blocks) split into %d shard(s):\n", r.from, r.to, r.to-r.from, len(segs))
	for k, s := range segs {
		fmt.Fprintf(r.out, "  shard %d: %d -> %d (%d blocks)\n", k, s.from, s.to, s.to-s.from)
	}
	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, "Each shard loads and account-hash-verifies its own seed; a boundary that is")
	fmt.Fprintln(r.out, "not an independently seedable ledger fails that shard, not the others.")
	fmt.Fprintln(r.out)

	// One findings writer, shared across shards (Write is mutex-guarded).
	var findings *findingsWriter
	if r.continueOnDivergence {
		f, err := newFindingsWriter(r.findingsPath())
		if err != nil {
			return fmt.Errorf("opening findings file: %w", err)
		}
		findings = f
		defer findings.Close()
	}

	var outMu sync.Mutex
	start := time.Now()

	statsList, errs := runSegmentsConcurrently(ctx, segs, func(ctx context.Context, shard int, seg ledgerSegment) (*RangeReplayStats, error) {
		worker := *r // copy flags; each shard is otherwise independent
		worker.from = seg.from
		worker.to = seg.to
		worker.shards = 1
		worker.out = &shardWriter{mu: &outMu, w: r.out, prefix: fmt.Sprintf("[s%d] ", shard)}
		return worker.replaySegment(ctx, findings)
	})

	agg := aggregateStats(statsList)
	agg.TotalDuration = time.Since(start)

	fmt.Fprintln(r.out)
	r.printShardedSummary(segs, statsList, errs, agg)

	failed := false
	for i, err := range errs {
		if err != nil {
			failed = true
			fmt.Fprintf(r.out, "shard %d (%d->%d) error: %v\n", i, segs[i].from, segs[i].to, err)
		}
	}
	if failed || agg.FailedAtBlock > 0 {
		return cmdexit.ErrReported
	}
	return nil
}

// printShardedSummary reports each shard's outcome, then the aggregate totals and
// per-phase breakdown via the shared summary printer.
func (r *replayRangeRunner) printShardedSummary(segs []ledgerSegment, statsList []*RangeReplayStats, errs []error, agg *RangeReplayStats) {
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "Per-shard results:")
	for i, seg := range segs {
		switch {
		case errs[i] != nil:
			fmt.Fprintf(r.out, "  shard %d %d->%d: ERROR\n", i, seg.from, seg.to)
		case statsList[i] == nil:
			fmt.Fprintf(r.out, "  shard %d %d->%d: no result\n", i, seg.from, seg.to)
		default:
			s := statsList[i]
			status := "ok"
			if s.FailedAtBlock > 0 {
				status = fmt.Sprintf("FAILED@%d", s.FailedAtBlock)
			} else if s.Divergences > 0 {
				status = fmt.Sprintf("%d divergence(s)", s.Divergences)
			}
			fmt.Fprintf(r.out, "  shard %d %d->%d: %d/%d blocks ok, %s, %v\n",
				i, seg.from, seg.to, s.BlocksSuccessful, s.BlocksProcessed, status,
				s.TotalDuration.Round(time.Millisecond))
		}
	}
	r.printRangeSummary(agg)
}
