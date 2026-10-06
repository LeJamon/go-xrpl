// Package membudget provides a small, node-local byte admission pool.
//
// The pool accounts for bytes retained by cooperating consumers. Accounts have
// a protected minimum, which other accounts cannot consume, and an optional
// maximum. Capacity above the protected minima is shared by all accounts.
package membudget

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const maxAccounts = 256

var (
	// ErrDuplicateAccount is returned when an account name is already in use.
	ErrDuplicateAccount = errors.New("memory budget account already exists")
	// ErrInvalidAccountName is returned for an empty account name.
	ErrInvalidAccountName = errors.New("memory budget account name is empty")
	// ErrAccountLimit is returned when the bounded account registry is full.
	ErrAccountLimit = errors.New("memory budget account limit reached")
	// ErrMinimumExceedsLimit is returned when protected minima cannot fit in
	// the parent pool.
	ErrMinimumExceedsLimit = errors.New("memory budget protected minima exceed pool limit")
	// ErrInvalidAccountBounds is returned when an account maximum is below its
	// minimum or above the parent pool limit.
	ErrInvalidAccountBounds = errors.New("memory budget account bounds are invalid")
)

// Pool owns a fixed byte allowance and a bounded set of named accounts.
//
// A Pool must be created once during node startup. Account creation is
// intentionally separate from admission so hot reserve/release paths do not
// allocate or grow maps.
type Pool struct {
	mu sync.Mutex

	limit uint64
	used  uint64
	peak  uint64

	minimum       uint64
	unusedMinimum uint64
	denied        uint64
	waits         uint64
	invalid       uint64

	accounts map[string]*Account
	changed  chan struct{}
}

// Account is a named child of a Pool. The zero value is not usable; accounts
// must be returned by Pool.NewAccount.
type Account struct {
	pool *Pool
	name string

	minimum uint64
	maximum uint64
	used    uint64
	peak    uint64
	denied  uint64
	waits   uint64
	invalid uint64
}

// New creates a byte admission pool. A zero limit is valid and simply rejects
// positive reservations.
func New(limit uint64) *Pool {
	return &Pool{
		limit:    limit,
		accounts: make(map[string]*Account),
	}
}

// NewAccount registers a named account with a protected minimum and an
// optional maximum. A maximum of zero means the pool limit. The minimum is a
// logical protected capacity; it is not allocated until the account reserves
// bytes.
func (p *Pool) NewAccount(name string, minimum, maximum uint64) (*Account, error) {
	if p == nil {
		return nil, errors.New("memory budget pool is nil")
	}
	if strings.TrimSpace(name) == "" {
		return nil, ErrInvalidAccountName
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.accounts) >= maxAccounts {
		return nil, ErrAccountLimit
	}
	if _, exists := p.accounts[name]; exists {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateAccount, name)
	}
	if maximum == 0 {
		maximum = p.limit
	}
	if minimum > maximum || maximum > p.limit {
		return nil, fmt.Errorf("%w: %s minimum=%d maximum=%d pool=%d", ErrInvalidAccountBounds, name, minimum, maximum, p.limit)
	}
	if minimum > p.limit-p.minimum {
		return nil, fmt.Errorf("%w: %s minimum=%d protected=%d pool=%d", ErrMinimumExceedsLimit, name, minimum, p.minimum, p.limit)
	}

	a := &Account{
		pool:    p,
		name:    name,
		minimum: minimum,
		maximum: maximum,
	}
	p.accounts[name] = a
	p.minimum += minimum
	p.unusedMinimum += minimum
	return a, nil
}

// Changed returns a pool-wide wake channel. It is safe to call before
// TryReserve: releases close the current channel and lazily create a new one
// on the next call. A caller should retain the returned channel while retrying
// its reservation to avoid a lost wake-up.
func (p *Pool) Changed() <-chan struct{} {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.changed == nil {
		p.changed = make(chan struct{})
	}
	p.waits++
	ch := p.changed
	p.mu.Unlock()
	return ch
}

// TryReserve attempts to hold bytes for an account without blocking. It has no
// allocation on either the successful or denied path.
func (a *Account) TryReserve(bytes uint64) bool {
	if a == nil || a.pool == nil {
		return false
	}
	p := a.pool
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.canReserveLocked(a, bytes) {
		a.denied++
		p.denied++
		return false
	}
	if bytes == 0 {
		return true
	}
	p.adjustUsedLocked(a, bytes)
	return true
}

// Release returns bytes held by the account. Invalid over-release is ignored
// and recorded in the snapshots so a bad terminal path cannot corrupt the
// parent's accounting or underflow an unsigned counter.
func (a *Account) Release(bytes uint64) {
	if a == nil || a.pool == nil {
		return
	}
	p := a.pool
	var ch chan struct{}
	p.mu.Lock()
	if bytes > a.used {
		a.invalid++
		p.invalid++
		p.mu.Unlock()
		return
	}
	if bytes == 0 {
		p.mu.Unlock()
		return
	}
	p.releaseUsedLocked(a, bytes)
	ch = p.takeChangedLocked()
	p.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// TransferTo atomically moves bytes between accounts in one pool. It avoids a
// transient parent-capacity failure and wakes no waiters because total pool
// usage is unchanged.
func (a *Account) TransferTo(dst *Account, bytes uint64) bool {
	if a == nil || dst == nil || a.pool == nil || dst.pool != a.pool {
		return false
	}
	p := a.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if bytes > a.used {
		return false
	}
	if bytes == 0 || a == dst {
		return bytes == 0 || a == dst
	}

	p.releaseUsedLocked(a, bytes)
	if !p.canReserveLocked(dst, bytes) {
		p.adjustUsedLocked(a, bytes)
		return false
	}
	p.adjustUsedLocked(dst, bytes)
	return true
}

// Limit returns the account's immutable admission ceiling.
func (a *Account) Limit() uint64 {
	if a == nil {
		return 0
	}
	return a.maximum
}

// AccountSnapshot is a point-in-time account view. Reserved is the protected
// minimum; UnusedReserved is the currently unborrowable part of that minimum.
type AccountSnapshot struct {
	Name string

	Minimum         uint64
	Maximum         uint64
	Reserved        uint64
	UnusedReserved  uint64
	Used            uint64
	Available       uint64
	Peak            uint64
	Denied          uint64
	Waits           uint64
	InvalidReleases uint64
}

// AccountStats is a descriptive alias for AccountSnapshot used by node
// diagnostics.
type AccountStats = AccountSnapshot

// Snapshot is a point-in-time view of a Pool. Accounts are sorted by name and
// limited by the construction-time account bound, making diagnostic output
// deterministic and bounded.
type PoolSnapshot struct {
	Limit uint64
	Used  uint64
	Peak  uint64

	// Reserved is the sum of all protected minima. UnusedReserved is the part
	// not currently held by those accounts and therefore unavailable to peers.
	Reserved        uint64
	UnusedReserved  uint64
	Committed       uint64
	Available       uint64
	SharedAvailable uint64

	Denied          uint64
	Waits           uint64
	InvalidReleases uint64
	Accounts        []AccountStats
}

// Snapshot returns a consistent copy of the pool and all currently registered
// accounts. The snapshot path may allocate; admission paths do not.
func (p *Pool) Snapshot() PoolSnapshot {
	if p == nil {
		return PoolSnapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	accounts := make([]AccountSnapshot, 0, len(p.accounts))
	for _, a := range p.accounts {
		accounts = append(accounts, a.snapshotLocked())
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	available := p.limit - p.used
	shared := uint64(0)
	if available >= p.unusedMinimum {
		shared = available - p.unusedMinimum
	}
	committed := p.used + p.unusedMinimum
	if committed < p.used { // defensive overflow guard for corrupted state
		committed = ^uint64(0)
	}
	return PoolSnapshot{
		Limit:           p.limit,
		Used:            p.used,
		Peak:            p.peak,
		Reserved:        p.minimum,
		UnusedReserved:  p.unusedMinimum,
		Committed:       committed,
		Available:       available,
		SharedAvailable: shared,
		Denied:          p.denied,
		Waits:           p.waits,
		InvalidReleases: p.invalid,
		Accounts:        accounts,
	}
}

// Snapshot returns a consistent view of one account.
func (a *Account) Snapshot() AccountSnapshot {
	if a == nil || a.pool == nil {
		return AccountSnapshot{}
	}
	p := a.pool
	p.mu.Lock()
	snapshot := a.snapshotLocked()
	p.mu.Unlock()
	return snapshot
}

func (a *Account) snapshotLocked() AccountSnapshot {
	unused := uint64(0)
	if a.minimum > a.used {
		unused = a.minimum - a.used
	}
	available := uint64(0)
	if a.maximum >= a.used {
		available = a.maximum - a.used
	}
	return AccountSnapshot{
		Name:            a.name,
		Minimum:         a.minimum,
		Maximum:         a.maximum,
		Reserved:        a.minimum,
		UnusedReserved:  unused,
		Used:            a.used,
		Available:       available,
		Peak:            a.peak,
		Denied:          a.denied,
		Waits:           a.waits,
		InvalidReleases: a.invalid,
	}
}

func (a *Account) Changed() <-chan struct{} {
	if a == nil || a.pool == nil {
		return nil
	}
	return a.pool.changedForAccount(a)
}

func (p *Pool) changedForAccount(a *Account) <-chan struct{} {
	p.mu.Lock()
	if p.changed == nil {
		p.changed = make(chan struct{})
	}
	p.waits++
	a.waits++
	ch := p.changed
	p.mu.Unlock()
	return ch
}

func (p *Pool) canReserveLocked(a *Account, bytes uint64) bool {
	if a.used > a.maximum || bytes > a.maximum-a.used {
		return false
	}
	if bytes > p.limit-p.used {
		return false
	}
	ownUnused := uint64(0)
	if a.minimum > a.used {
		ownUnused = a.minimum - a.used
	}
	if p.unusedMinimum < ownUnused {
		return false
	}
	otherUnused := p.unusedMinimum - ownUnused
	available := p.limit - p.used
	return available >= otherUnused && bytes <= available-otherUnused
}

func (p *Pool) adjustUsedLocked(a *Account, delta uint64) {
	if delta == 0 {
		return
	}
	oldUnused := uint64(0)
	if a.minimum > a.used {
		oldUnused = a.minimum - a.used
	}
	if delta > 0 {
		a.used += delta
		p.used += delta
		if a.used > a.peak {
			a.peak = a.used
		}
		if p.used > p.peak {
			p.peak = p.used
		}
	}
	newUnused := uint64(0)
	if a.minimum > a.used {
		newUnused = a.minimum - a.used
	}
	if newUnused >= oldUnused {
		p.unusedMinimum += newUnused - oldUnused
	} else {
		p.unusedMinimum -= oldUnused - newUnused
	}
}

func (p *Pool) releaseUsedLocked(a *Account, bytes uint64) {
	oldUnused := uint64(0)
	if a.minimum > a.used {
		oldUnused = a.minimum - a.used
	}
	a.used -= bytes
	p.used -= bytes
	newUnused := uint64(0)
	if a.minimum > a.used {
		newUnused = a.minimum - a.used
	}
	if newUnused >= oldUnused {
		p.unusedMinimum += newUnused - oldUnused
	} else {
		p.unusedMinimum -= oldUnused - newUnused
	}
}

func (p *Pool) takeChangedLocked() chan struct{} {
	ch := p.changed
	p.changed = nil
	return ch
}
