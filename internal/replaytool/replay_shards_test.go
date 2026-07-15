package replaytool

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPartitionRange(t *testing.T) {
	cases := []struct {
		name           string
		from, to       uint32
		shards         int
		wantSegs       int
		wantBoundaries []uint32 // from, b1, ..., to
	}{
		{"single shard", 100, 200, 1, 1, []uint32{100, 200}},
		{"even split", 100, 200, 4, 4, []uint32{100, 125, 150, 175, 200}},
		{"remainder spread to front", 0, 10, 3, 3, []uint32{0, 4, 7, 10}},
		{"more shards than blocks caps", 100, 103, 8, 3, []uint32{100, 101, 102, 103}},
		{"zero shards treated as one", 5, 9, 0, 1, []uint32{5, 9}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs := partitionRange(c.from, c.to, c.shards)
			if len(segs) != c.wantSegs {
				t.Fatalf("got %d segments, want %d: %+v", len(segs), c.wantSegs, segs)
			}

			// Contiguous, gap-free, covers the whole range, no empty segments.
			if segs[0].from != c.from {
				t.Errorf("first segment starts at %d, want %d", segs[0].from, c.from)
			}
			if segs[len(segs)-1].to != c.to {
				t.Errorf("last segment ends at %d, want %d", segs[len(segs)-1].to, c.to)
			}
			var covered uint32
			for k, s := range segs {
				if s.to <= s.from {
					t.Errorf("segment %d is empty: %d->%d", k, s.from, s.to)
				}
				if k > 0 && s.from != segs[k-1].to {
					t.Errorf("gap/overlap at %d: prev.to=%d this.from=%d", k, segs[k-1].to, s.from)
				}
				covered += s.to - s.from
			}
			if covered != c.to-c.from {
				t.Errorf("covered %d blocks, want %d", covered, c.to-c.from)
			}

			if c.wantBoundaries != nil {
				got := []uint32{segs[0].from}
				for _, s := range segs {
					got = append(got, s.to)
				}
				if fmt.Sprint(got) != fmt.Sprint(c.wantBoundaries) {
					t.Errorf("boundaries %v, want %v", got, c.wantBoundaries)
				}
			}
		})
	}
}

func TestAggregateStats(t *testing.T) {
	parts := []*RangeReplayStats{
		{BlocksProcessed: 10, BlocksSuccessful: 10, TotalTransactions: 100, FetchDuration: time.Second, ApplyDuration: 10 * time.Second, FinalizeDuration: time.Second},
		nil, // a shard that produced no stats must be skipped, not panic
		{BlocksProcessed: 5, BlocksSuccessful: 4, TotalTransactions: 40, Divergences: 1, FailedAtBlock: 207, FailureReason: "hash mismatch", FetchDuration: time.Second, ApplyDuration: 5 * time.Second},
		{BlocksProcessed: 3, BlocksSuccessful: 2, FailedAtBlock: 150, FailureReason: "earlier"},
	}
	agg := aggregateStats(parts)

	if agg.BlocksProcessed != 18 || agg.BlocksSuccessful != 16 || agg.TotalTransactions != 140 {
		t.Errorf("count aggregation wrong: %+v", agg)
	}
	if agg.Divergences != 1 {
		t.Errorf("divergences = %d, want 1", agg.Divergences)
	}
	if agg.FetchDuration != 2*time.Second || agg.ApplyDuration != 15*time.Second || agg.FinalizeDuration != time.Second {
		t.Errorf("phase durations wrong: fetch=%v apply=%v finalize=%v", agg.FetchDuration, agg.ApplyDuration, agg.FinalizeDuration)
	}
	// Earliest failing block across shards wins.
	if agg.FailedAtBlock != 150 || agg.FailureReason != "earlier" {
		t.Errorf("failure aggregation: got %d/%q, want 150/earlier", agg.FailedAtBlock, agg.FailureReason)
	}
}

// TestRunSegmentsConcurrently checks every segment's result lands at its own
// index and shared sinks stay uncorrupted under concurrency. Run under -race.
func TestRunSegmentsConcurrently(t *testing.T) {
	segs := partitionRange(0, 4000, 8)

	var sinkMu sync.Mutex
	var sink bytes.Buffer
	const linesPerShard = 50

	stats, errs := runSegmentsConcurrently(context.Background(), segs,
		func(_ context.Context, shard int, seg ledgerSegment) (*RangeReplayStats, error) {
			w := &shardWriter{mu: &sinkMu, w: &sink, prefix: fmt.Sprintf("[s%d] ", shard)}
			for i := 0; i < linesPerShard; i++ {
				fmt.Fprintf(w, "block %d of shard %d\n", i, shard)
			}
			// Encode the shard index into the stats so we can verify ordering.
			return &RangeReplayStats{BlocksProcessed: shard, TotalTransactions: int(seg.to - seg.from)}, nil
		})

	if len(stats) != len(segs) {
		t.Fatalf("got %d stats, want %d", len(stats), len(segs))
	}
	for i := range segs {
		if errs[i] != nil {
			t.Errorf("shard %d unexpected error: %v", i, errs[i])
		}
		if stats[i] == nil || stats[i].BlocksProcessed != i {
			t.Errorf("shard %d result landed at wrong index: %+v", i, stats[i])
		}
	}

	// Every emitted line must be intact and correctly tagged — no interleaving.
	lines := strings.Split(strings.TrimRight(sink.String(), "\n"), "\n")
	if len(lines) != len(segs)*linesPerShard {
		t.Fatalf("got %d lines, want %d", len(lines), len(segs)*linesPerShard)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "[s") {
			t.Fatalf("line missing shard prefix: %q", line)
		}
		var ls, li, lsh int
		if _, err := fmt.Sscanf(line, "[s%d] block %d of shard %d", &ls, &li, &lsh); err != nil {
			t.Fatalf("malformed/interleaved line %q: %v", line, err)
		}
		if ls != lsh {
			t.Fatalf("prefix shard %d != body shard %d in %q", ls, lsh, line)
		}
	}
}
