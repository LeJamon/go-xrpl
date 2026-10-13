package rootlease

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

func testRoot(value byte) Root {
	return Root{Hash: [32]byte{value}, Kind: shamap.TypeState}
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

func TestBuildAdmissionBlocksRetirementUntilDestinationIsRegistered(t *testing.T) {
	r, err := New(nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	parent, destination := testRoot(7), testRoot(8)
	parentLease, err := r.AcquireLease(parent)
	if err != nil {
		t.Fatal(err)
	}
	build, err := r.BeginBuild()
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Stats().ActiveBuilds; got != 1 {
		t.Fatalf("active builds = %d, want 1", got)
	}
	if release, ok := r.Guard(map[Root]struct{}{parent: {}}); ok || release != nil {
		t.Fatal("Guard admitted retirement during an active build")
	}

	destinationLease, err := r.AcquireLease(destination)
	if err != nil {
		t.Fatal(err)
	}
	build.End()
	build.End()
	if got := r.Stats().ActiveBuilds; got != 0 {
		t.Fatalf("active builds after End = %d, want 0", got)
	}
	guardRelease, ok := r.Guard(map[Root]struct{}{parent: {}, destination: {}})
	if !ok {
		t.Fatal("Guard rejected registered destination after build completion")
	}
	guardRelease()
	destinationLease.Release()
	parentLease.Release()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRejectsActiveBuild(t *testing.T) {
	r, err := New(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	build, err := r.BeginBuild()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != ErrBuildActive {
		t.Fatalf("Close with active build = %v, want %v", err, ErrBuildActive)
	}
	build.End()
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
