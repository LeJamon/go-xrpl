package types

import (
	"context"
	"sync"
	"testing"
)

func TestClientLoadShedderReleaseOwnership(t *testing.T) {
	var shedder ClientLoadShedder
	end := shedder.Begin()
	var dispatchReleases sync.WaitGroup
	for range 8 {
		dispatchReleases.Add(1)
		go func() {
			defer dispatchReleases.Done()
			end()
		}()
	}
	dispatchReleases.Wait()
	if got := shedder.InFlight(); got != 0 {
		t.Fatalf("in-flight after repeated release = %d, want 0", got)
	}

	first, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("first bounded acquire failed")
	}
	second, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("second bounded acquire failed")
	}
	if _, ok := shedder.AcquirePathfind(); ok {
		t.Fatal("third bounded acquire exceeded the cap")
	}

	var releases sync.WaitGroup
	for range 8 {
		releases.Add(1)
		go func() {
			defer releases.Done()
			first()
		}()
	}
	releases.Wait()
	if got := shedder.PathfindActive(); got != 1 {
		t.Fatalf("active after repeated release = %d, want 1", got)
	}

	third, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("release should make a bounded slot available")
	}
	second()
	third()
	third()
	if got := shedder.PathfindActive(); got != 0 {
		t.Fatalf("active after all releases = %d, want 0", got)
	}

	unlimited := shedder.AcquirePathfindUnlimited()
	bounded, ok := shedder.AcquirePathfind()
	if !ok {
		t.Fatal("unlimited acquisition should not poison bounded ownership")
	}
	bounded()
	unlimited()
	if got := shedder.PathfindActive(); got != 0 {
		t.Fatalf("active after mixed release = %d, want 0", got)
	}
}

func TestClientLoadShedderWaitCancellationAndZeroValues(t *testing.T) {
	var shedder ClientLoadShedder
	first := shedder.AcquirePathfindUnlimited()
	second := shedder.AcquirePathfindUnlimited()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, ok := shedder.WaitPathfind(ctx); ok || release != nil {
		t.Fatalf("canceled wait = (%T, %v), want (nil, false)", release, ok)
	}
	first()
	second()

	if release, ok := shedder.WaitPathfind(nil); !ok || release == nil {
		t.Fatalf("nil-context wait = (%T, %v), want successful owned release", release, ok)
	} else {
		release()
	}
	if got := shedder.PathfindActive(); got != 0 {
		t.Fatalf("zero-value shedder leaked pathfind slot: %d", got)
	}

	var nilShedder *ClientLoadShedder
	nilShedder.Begin()()
	if release, ok := nilShedder.AcquirePathfind(); !ok || release == nil {
		t.Fatalf("nil bounded acquire = (%T, %v), want no-op success", release, ok)
	} else {
		release()
	}
	if release := nilShedder.AcquirePathfindUnlimited(); release == nil {
		t.Fatal("nil unlimited acquire returned nil release")
	} else {
		release()
	}
}
