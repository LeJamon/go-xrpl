package rootlease

import (
	"errors"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
)

var ErrCapacity = errors.New("ledger root lease capacity exhausted")

var ErrReleased = errors.New("ledger root lease is released")

var ErrBuildActive = errors.New("ledger root build is still active")

type Root struct {
	Hash [32]byte
	Kind shamap.Type
}

const entryBytes = 256

// Registry supplies rotation with the roots owned by operations outside the
// ledger history. A lease pins a closure, not the database mutation lock.
type Registry struct {
	mu     sync.Mutex
	roots  map[Root]uint64
	budget *membudget.Account
	limit  int
	active int
	builds int
	closed bool
}

// Lease owns one root entry until Release. Rebind changes the authenticated
// root covered by the same ownership slot, which lets a sealed builder hand
// its dependency lease to the newly emitted root without a capacity gap.
type Lease struct {
	registry *Registry
	root     Root
	released bool
}

// BuildLease prevents destructive root retirement while a bounded builder is
// emitting its replacement nodes. It does not hold the registry mutex or a
// durable-generation snapshot while the build is running.
type BuildLease struct {
	registry *Registry
	once     sync.Once
}

type Stats struct {
	Active       int
	Distinct     int
	ActiveBuilds int
}

func New(budget *membudget.Account, limit int) (*Registry, error) {
	if limit <= 0 || uint64(limit) > ^uint64(0)/entryBytes || (budget != nil && !budget.TryReserve(uint64(limit)*entryBytes)) {
		return nil, ErrCapacity
	}
	return &Registry{roots: make(map[Root]uint64), budget: budget, limit: limit}, nil
}

func (r *Registry) Acquire(root Root) (func(), error) {
	lease, err := r.AcquireLease(root)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

// BeginBuild admits one bounded builder. Guard rejects retirement while any
// build is active; the returned token must be ended after the destination root
// lease has been registered or the build has been handed off for retry.
func (r *Registry) BeginBuild() (*BuildLease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrCapacity
	}
	r.builds++
	return &BuildLease{registry: r}, nil
}

// EndBuild releases a build admission token. It is safe to call more than
// once, including from cleanup paths that race a cancellation path.
func (r *Registry) EndBuild(build *BuildLease) {
	if build == nil || build.registry != r {
		return
	}
	build.once.Do(func() {
		r.mu.Lock()
		if r.builds > 0 {
			r.builds--
		}
		r.mu.Unlock()
	})
}

// End releases a build admission token through its owning registry.
func (b *BuildLease) End() {
	if b == nil || b.registry == nil {
		return
	}
	b.registry.EndBuild(b)
}

// AcquireLease reserves one registry slot for root. Zero roots are represented
// by a lease without an active slot so a builder can later rebind its parent
// lease when the first non-empty root is emitted.
func (r *Registry) AcquireLease(root Root) (*Lease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if root.Hash != ([32]byte{}) {
		if r.closed || r.active >= r.limit {
			return nil, ErrCapacity
		}
		r.active++
		r.roots[root]++
	}
	return &Lease{registry: r, root: root}, nil
}

// Release relinquishes the lease. It is safe to call more than once and from
// a different goroutine than Rebind.
func (l *Lease) Release() {
	if l == nil || l.registry == nil {
		return
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	r.removeLocked(l.root)
}

// Rebind atomically moves this lease to root without consuming another
// registry slot. It is used only after a builder has successfully emitted all
// nodes for its replacement root.
func (l *Lease) Rebind(root Root) error {
	if l == nil || l.registry == nil {
		return ErrReleased
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	if l.released {
		return ErrReleased
	}
	if l.root == root {
		return nil
	}
	if root.Hash != ([32]byte{}) && r.closed {
		return ErrCapacity
	}
	if l.root.Hash == ([32]byte{}) {
		if root.Hash != ([32]byte{}) {
			if r.active >= r.limit {
				return ErrCapacity
			}
			r.active++
			r.roots[root]++
		}
	} else {
		if root.Hash != ([32]byte{}) {
			r.roots[root]++
		} else {
			r.active--
		}
		r.removeRootLocked(l.root)
	}
	l.root = root
	return nil
}

func (r *Registry) removeLocked(root Root) {
	if root.Hash == ([32]byte{}) {
		return
	}
	r.active--
	r.removeRootLocked(root)
}

func (r *Registry) removeRootLocked(root Root) {
	if r.roots[root] == 1 {
		delete(r.roots, root)
	} else if r.roots[root] > 1 {
		r.roots[root]--
	}
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != 0 {
		return errors.New("ledger root leases are still active")
	}
	if r.builds != 0 {
		return ErrBuildActive
	}
	if !r.closed {
		r.closed = true
		r.roots = nil
		if r.budget != nil {
			r.budget.Release(uint64(r.limit) * entryBytes)
		}
	}
	return nil
}

func (r *Registry) Roots() []Root {
	r.mu.Lock()
	defer r.mu.Unlock()
	roots := make([]Root, 0, len(r.roots))
	for root := range r.roots {
		roots = append(roots, root)
	}
	return roots
}

func (r *Registry) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{Active: r.active, Distinct: len(r.roots), ActiveBuilds: r.builds}
}

// Guard admits retirement only when every current lease appears in preserved.
// The map is inspected only while taking the guard and is never retained. New
// leases wait until the returned release function is called. Callers must
// build preserved before calling Guard; no callback runs while the mutex is
// held.
func (r *Registry) Guard(preserved map[Root]struct{}) (release func(), admitted bool) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, false
	}
	if r.builds != 0 {
		r.mu.Unlock()
		return nil, false
	}
	for root := range r.roots {
		if _, ok := preserved[root]; !ok {
			r.mu.Unlock()
			return nil, false
		}
	}
	return sync.OnceFunc(r.mu.Unlock), true
}

// GuardFunc is a compatibility adapter for callers that still derive their
// preservation set from a predicate. The predicate runs without the registry
// mutex; Guard rechecks the resulting immutable set before admitting
// retirement.
func (r *Registry) GuardFunc(preserve func(Root) bool) (func(), bool) {
	if preserve == nil {
		return nil, false
	}
	roots := r.Roots()
	preserved := make(map[Root]struct{}, len(roots))
	for _, root := range roots {
		if !preserve(root) {
			return nil, false
		}
		preserved[root] = struct{}{}
	}
	return r.Guard(preserved)
}
