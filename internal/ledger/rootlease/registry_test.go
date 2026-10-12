package rootlease

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

func testRoot(value byte) Root {
	return Root{Hash: [32]byte{value}, Kind: shamap.TypeState}
}

func TestLeaseRebindKeepsOneCapacitySlot(t *testing.T) {
	r, err := New(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot, newRoot := testRoot(1), testRoot(2)
	lease, err := r.AcquireLease(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Rebind(newRoot); err != nil {
		t.Fatal(err)
	}
	if got := r.Roots(); len(got) != 1 || got[0] != newRoot {
		t.Fatalf("roots after rebind = %v, want %v", got, []Root{newRoot})
	}
	if _, err := r.Acquire(oldRoot); err != ErrCapacity {
		t.Fatalf("Acquire while rebound lease is active = %v, want ErrCapacity", err)
	}
	lease.Release()
	release, err := r.Acquire(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGuardPreservedMapBlocksNewLeases(t *testing.T) {
	r, err := New(nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	root := testRoot(3)
	lease, err := r.AcquireLease(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	guardRelease, ok := r.Guard(map[Root]struct{}{root: {}})
	if !ok {
		t.Fatal("Guard rejected a preserved active root")
	}
	acquired := make(chan error, 1)
	go func() {
		release, err := r.Acquire(testRoot(4))
		if err == nil {
			release()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		t.Fatalf("Acquire completed while guard was held: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	guardRelease()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("Acquire after guard release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not resume after guard release")
	}
	lease.Release()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGuardFuncCallbackRunsOutsideRegistryLock(t *testing.T) {
	r, err := New(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	root := testRoot(5)
	releaseLease, err := r.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	guardRelease, ok := r.GuardFunc(func(got Root) bool {
		return got == root && len(r.Roots()) == 1
	})
	if !ok {
		t.Fatal("GuardFunc rejected a preserved root")
	}
	guardRelease()
	releaseLease()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseReleaseIsConcurrentAndIdempotent(t *testing.T) {
	r, err := New(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := r.AcquireLease(testRoot(6))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	for range 8 {
		go func() {
			lease.Release()
			done <- struct{}{}
		}()
	}
	for range 8 {
		<-done
	}
	if got := r.Roots(); len(got) != 0 {
		t.Fatalf("roots after concurrent release = %v", got)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
