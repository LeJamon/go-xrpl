package membudget

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"sync"
	"testing"
	"time"
)

func TestPoolProtectsAccountMinimums(t *testing.T) {
	p := New(100)
	first, err := p.NewAccount("first", 40, 80)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.NewAccount("second", 30, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !first.TryReserve(40) {
		t.Fatal("first protected minimum was not admitted")
	}
	if !first.TryReserve(30) {
		t.Fatal("first shared capacity was not admitted")
	}
	if first.TryReserve(1) {
		t.Fatal("first borrowed second's protected minimum")
	}
	if !second.TryReserve(30) {
		t.Fatal("second protected minimum was not admitted")
	}
	snapshot := p.Snapshot()
	if snapshot.Limit != 100 || snapshot.Used != 100 || snapshot.Reserved != 70 || snapshot.UnusedReserved != 0 {
		t.Fatalf("unexpected pool snapshot: %+v", snapshot)
	}
	if snapshot.SharedAvailable != 0 || snapshot.Available != 0 {
		t.Fatalf("unexpected availability: %+v", snapshot)
	}
	second.Release(30)
	if first.Snapshot().Available != 0 || second.Snapshot().Available != 30 {
		t.Fatal("account availability must include only its protected minimum and shared capacity")
	}
	if first.TryReserve(1) {
		t.Fatal("first borrowed second's still-protected minimum")
	}
}

func TestPoolRejectsOverflowAndInvalidRelease(t *testing.T) {
	p := New(math.MaxUint64)
	a, err := p.NewAccount("all", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !a.TryReserve(math.MaxUint64) {
		t.Fatal("maximum reservation was rejected")
	}
	if a.TryReserve(1) {
		t.Fatal("overflow reservation was admitted")
	}
	a.Release(math.MaxUint64)
	if !a.TryReserve(1) {
		t.Fatal("one-byte reservation failed after full release")
	}
	a.Release(2)
	if got := a.Snapshot().Used; got != 1 {
		t.Fatalf("invalid release changed usage: %d", got)
	}
	snapshot := p.Snapshot()
	if snapshot.InvalidReleases != 1 || snapshot.Denied != 1 || snapshot.Released != math.MaxUint64 {
		t.Fatalf("invalid operation counters missing: %+v", snapshot)
	}
	a.Release(1)
	if p.Snapshot().Used != 0 {
		t.Fatal("valid release did not return usage")
	}
}

func TestPoolChangedWakesSiblingWaiters(t *testing.T) {
	p := New(1)
	holder, err := p.NewAccount("holder", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := p.NewAccount("waiter", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !holder.TryReserve(1) {
		t.Fatal("holder reservation failed")
	}
	changed := waiter.Changed()
	go func() {
		time.Sleep(10 * time.Millisecond)
		holder.Release(1)
	}()
	if waiter.TryReserve(1) {
		t.Fatal("waiter reserved before release")
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("waiter missed release wakeup")
	}
	if !waiter.TryReserve(1) {
		t.Fatal("waiter did not reserve after wakeup")
	}
}

func TestPoolReserveReleaseConcurrent(t *testing.T) {
	p := New(32)
	a, err := p.NewAccount("workers", 0, 32)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 1000; attempt++ {
				if a.TryReserve(1) {
					a.Release(1)
				}
			}
		}()
	}
	wg.Wait()
	snapshot := p.Snapshot()
	if snapshot.Used != 0 || snapshot.Peak > snapshot.Limit {
		t.Fatalf("concurrent accounting escaped bounds: %+v", snapshot)
	}
}

func TestPoolCancellationPattern(t *testing.T) {
	p := New(1)
	holder, err := p.NewAccount("holder", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := p.NewAccount("waiter", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !holder.TryReserve(1) {
		t.Fatal("holder reservation failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed := waiter.Changed()
	if waiter.TryReserve(1) {
		t.Fatal("waiter reserved unexpectedly")
	}
	select {
	case <-ctx.Done():
	case <-changed:
		t.Fatal("unexpected release wakeup")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("context cancellation was not preserved")
	}
}

func TestPoolTransferIsAtomic(t *testing.T) {
	p := New(10)
	source, err := p.NewAccount("source", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := p.NewAccount("destination", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !source.TryReserve(4) || !source.TransferTo(destination, 4) {
		t.Fatal("transfer failed")
	}
	if source.Snapshot().Used != 0 || destination.Snapshot().Used != 4 || p.Snapshot().Used != 4 {
		t.Fatalf("transfer accounting mismatch: source=%+v destination=%+v pool=%+v", source.Snapshot(), destination.Snapshot(), p.Snapshot())
	}
	if source.TransferTo(destination, 1) {
		t.Fatal("oversized transfer succeeded")
	}
}

func TestPoolRejectsMinimumRegistrationWithoutSharedCapacity(t *testing.T) {
	p := New(100)
	first, err := p.NewAccount("first", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !first.TryReserve(90) {
		t.Fatal("initial reservation failed")
	}
	if _, err := p.NewAccount("late", 20, 100); err == nil {
		t.Fatal("minimum registration consumed already-used shared capacity")
	}
}

func TestPoolTransferWakesWhenProtectedFloorIsFilled(t *testing.T) {
	p := New(100)
	source, err := p.NewAccount("source", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := p.NewAccount("destination", 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := p.NewAccount("waiter", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !source.TryReserve(50) {
		t.Fatal("source reservation failed")
	}
	changed := waiter.Changed()
	if !source.TransferTo(destination, 50) {
		t.Fatal("transfer failed")
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("transfer did not wake shared-capacity waiter")
	}
	if !waiter.TryReserve(50) {
		t.Fatal("waiter could not use shared capacity after transfer")
	}
}

func TestProbeLinuxNestedCgroupFixture(t *testing.T) {
	files := map[string]string{
		"/proc/meminfo":                            "MemTotal: 4096000 kB\nMemAvailable: 1000 kB\n",
		"/proc/self/cgroup":                        "0::/slice/leaf\n",
		"/proc/self/mountinfo":                     "36 29 0:32 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup2 rw\n",
		"/sys/fs/cgroup/slice/leaf/memory.max":     "800000\n",
		"/sys/fs/cgroup/slice/leaf/memory.high":    "max\n",
		"/sys/fs/cgroup/slice/leaf/memory.current": "100000\n",
		"/sys/fs/cgroup/slice/memory.max":          "600000\n",
		"/sys/fs/cgroup/slice/memory.high":         "max\n",
		"/sys/fs/cgroup/slice/memory.current":      "200000\n",
		"/sys/fs/cgroup/memory.max":                "max\n",
		"/sys/fs/cgroup/memory.high":               "max\n",
		"/sys/fs/cgroup/memory.current":            "300000\n",
	}
	read := func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(value), nil
	}
	headroom, err := probeLinux(ProbeOptions{
		ReadFile:       read,
		DisableRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if headroom.Available != 400000 || headroom.Source != "linux:MemAvailable+cgroup" {
		t.Fatalf("unexpected nested cgroup headroom: %+v", headroom)
	}
}

func TestProbeLinuxFixtureErrorsAndRuntimeCap(t *testing.T) {
	files := map[string]string{"/proc/meminfo": "MemAvailable: 10 kB\n"}
	read := func(path string) ([]byte, error) {
		if value, ok := files[path]; ok {
			return []byte(value), nil
		}
		return nil, fs.ErrNotExist
	}
	headroom, err := probeLinux(ProbeOptions{
		ReadFile: read,
		RuntimeMemoryLimit: func() (uint64, bool) {
			return 8192, true
		},
		RuntimeManagedBytes: func() (uint64, error) {
			return 4096, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if headroom.Available != 4096 || headroom.Source != "linux:MemAvailable+GOMEMLIMIT" {
		t.Fatalf("unexpected runtime-capped headroom: %+v", headroom)
	}
	zeroLimit, err := probeLinux(ProbeOptions{
		ReadFile: read,
		RuntimeMemoryLimit: func() (uint64, bool) {
			return 0, true
		},
		RuntimeManagedBytes: func() (uint64, error) {
			return 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if zeroLimit.Available != 0 {
		t.Fatalf("zero GOMEMLIMIT was treated as unlimited: %+v", zeroLimit)
	}
	files["/proc/meminfo"] = "MemAvailable: -1 kB\n"
	if _, err := probeLinux(ProbeOptions{ReadFile: read, DisableRuntime: true}); err == nil {
		t.Fatal("negative MemAvailable was accepted")
	}
}

func TestProbeLinuxCgroupMembershipRequiresMount(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/proc/meminfo":
			return []byte("MemAvailable: 10 kB\n"), nil
		case "/proc/self/cgroup":
			return []byte("0::/slice\n"), nil
		default:
			return nil, fs.ErrNotExist
		}
	}
	if _, err := probeLinux(ProbeOptions{ReadFile: read, DisableRuntime: true}); err == nil {
		t.Fatal("missing cgroup mount was accepted")
	}
}

func TestProbeLinuxV1CgroupFixture(t *testing.T) {
	files := map[string]string{
		"/proc/meminfo":        "MemAvailable: 1000 kB\n",
		"/proc/self/cgroup":    "5:memory:/docker/leaf\n",
		"/proc/self/mountinfo": "12 1 0:42 / /sys/fs/cgroup/memory rw,relatime - cgroup cgroup rw,memory\n",
		"/sys/fs/cgroup/memory/docker/leaf/memory.limit_in_bytes":      "1000\n",
		"/sys/fs/cgroup/memory/docker/leaf/memory.soft_limit_in_bytes": "0\n",
		"/sys/fs/cgroup/memory/docker/leaf/memory.usage_in_bytes":      "400\n",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes":                  "2000\n",
		"/sys/fs/cgroup/memory/memory.soft_limit_in_bytes":             "0\n",
		"/sys/fs/cgroup/memory/memory.usage_in_bytes":                  "500\n",
	}
	read := func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(value), nil
	}
	headroom, err := probeLinux(ProbeOptions{ReadFile: read, DisableRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	if headroom.Available != 600 || headroom.Source != "linux:MemAvailable+cgroup" {
		t.Fatalf("unexpected v1 headroom: %+v", headroom)
	}
}

func TestAccountCapacityExcludesSiblingMinimums(t *testing.T) {
	p := New(100)
	a, err := p.NewAccount("processing", 20, 90)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.NewAccount("cache", 30, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Capacity(); got != 70 {
		t.Fatalf("capacity = %d, want 70", got)
	}
	if !b.TryReserve(60) {
		t.Fatal("cache reservation failed")
	}
	if got := a.Capacity(); got != 70 {
		t.Fatalf("elastic use changed capacity to %d", got)
	}
	if got := b.Capacity(); got != 60 {
		t.Fatalf("maximum lost: capacity = %d", got)
	}
	b.Release(60)
}
