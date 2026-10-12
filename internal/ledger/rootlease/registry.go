package rootlease

import (
	"errors"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
)

var ErrCapacity = errors.New("ledger root lease capacity exhausted")

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
	closed bool
}

func New(budget *membudget.Account, limit int) (*Registry, error) {
	if limit <= 0 || uint64(limit) > ^uint64(0)/entryBytes || (budget != nil && !budget.TryReserve(uint64(limit)*entryBytes)) {
		return nil, ErrCapacity
	}
	return &Registry{roots: make(map[Root]uint64), budget: budget, limit: limit}, nil
}

func (r *Registry) Acquire(root Root) (func(), error) {
	if root.Hash == ([32]byte{}) {
		return func() {}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.active >= r.limit {
		return nil, ErrCapacity
	}
	r.active++
	r.roots[root]++
	return sync.OnceFunc(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active--
		if r.roots[root] == 1 {
			delete(r.roots, root)
		} else {
			r.roots[root]--
		}
	}), nil
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != 0 {
		return errors.New("ledger root leases are still active")
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

// Guard admits retirement only when every current lease was preserved. New
// leases wait until release; fetching and preservation happen before Guard.
func (r *Registry) Guard(preserved func(Root) bool) (release func(), admitted bool) {
	r.mu.Lock()
	for root := range r.roots {
		if !preserved(root) {
			r.mu.Unlock()
			return nil, false
		}
	}
	return sync.OnceFunc(r.mu.Unlock), true
}
