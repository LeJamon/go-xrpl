package service

import (
	"sync"
	"time"
)

type openLedgerRole uint8

const (
	openLedgerTransition openLedgerRole = iota
	openLedgerIngress
	openLedgerConsensus
	openLedgerValidation
	openLedgerPreferredSwitch
	openLedgerShutdown
)

func (r openLedgerRole) String() string {
	switch r {
	case openLedgerIngress:
		return "ingress"
	case openLedgerConsensus:
		return "consensus"
	case openLedgerValidation:
		return "validation"
	case openLedgerPreferredSwitch:
		return "preferred-switch"
	case openLedgerShutdown:
		return "shutdown"
	default:
		return "transition"
	}
}

type openLedgerGateWait struct {
	LifecycleWait  time.Duration
	Role           openLedgerRole
	Wait           time.Duration
	PriorityQueued int
	IngressQueued  int
	AcquiredAt     time.Time
}

type openLedgerGateSnapshot struct {
	Owner          openLedgerRole
	Held           bool
	HeldFor        time.Duration
	QueuedPriority int
	QueuedIngress  int
}

type openLedgerGateSlowEvent struct {
	Role           openLedgerRole
	Wait           time.Duration
	Hold           time.Duration
	QueuedPriority int
	QueuedIngress  int
}

type priorityGateWaiter struct {
	role  openLedgerRole
	ready chan struct{}
}

// priorityGate serializes open-ledger work while allowing lifecycle-critical
// roles to pass queued ingress in FIFO order. Its zero value is ready to use.
type priorityGate struct {
	// mu guards ownership, admission queues, and diagnostic state.
	mu sync.Mutex

	held       bool
	owner      openLedgerRole
	ownerSince time.Time

	priority []*priorityGateWaiter
	ingress  []*priorityGateWaiter

	slowLog     func(openLedgerGateSlowEvent)
	lastSlowLog time.Time
}

const (
	openLedgerGateSlowWait     = 250 * time.Millisecond
	openLedgerGateSlowHold     = time.Second
	openLedgerGateSlowInterval = time.Second
)

func (g *priorityGate) Lock() {
	g.LockRole(openLedgerTransition)
}

func (g *priorityGate) LockRole(role openLedgerRole) openLedgerGateWait {
	queuedAt := time.Now()
	g.mu.Lock()
	if !g.held && len(g.priority) == 0 && len(g.ingress) == 0 {
		g.held = true
		g.owner = role
		acquiredAt := time.Now()
		g.ownerSince = acquiredAt
		g.mu.Unlock()
		return openLedgerGateWait{Role: role, Wait: acquiredAt.Sub(queuedAt), AcquiredAt: acquiredAt}
	}

	waiter := &priorityGateWaiter{
		role:  role,
		ready: make(chan struct{}),
	}
	if role == openLedgerIngress {
		g.ingress = append(g.ingress, waiter)
	} else {
		g.priority = append(g.priority, waiter)
	}
	priorityQueued := len(g.priority)
	ingressQueued := len(g.ingress)
	g.mu.Unlock()

	<-waiter.ready
	acquiredAt := time.Now()
	wait := openLedgerGateWait{
		Role:           role,
		Wait:           acquiredAt.Sub(queuedAt),
		PriorityQueued: priorityQueued,
		IngressQueued:  ingressQueued,
		AcquiredAt:     acquiredAt,
	}
	g.recordWait(wait)
	return wait
}

func (g *priorityGate) TryLock() bool {
	g.mu.Lock()
	now := time.Now()
	defer g.mu.Unlock()
	if g.held || len(g.priority) != 0 || len(g.ingress) != 0 {
		return false
	}
	g.held = true
	g.owner = openLedgerTransition
	g.ownerSince = now
	return true
}

func (g *priorityGate) Unlock() {
	g.mu.Lock()
	now := time.Now()
	if !g.held {
		g.mu.Unlock()
		panic("sync: unlock of unlocked priorityGate")
	}
	hold := now.Sub(g.ownerSince)
	owner := g.owner

	priorityQueued := len(g.priority)
	ingressQueued := len(g.ingress)
	var next *priorityGateWaiter
	if len(g.priority) != 0 {
		next = g.priority[0]
		g.priority[0] = nil
		g.priority = g.priority[1:]
	} else if len(g.ingress) != 0 {
		next = g.ingress[0]
		g.ingress[0] = nil
		g.ingress = g.ingress[1:]
	}
	if next == nil {
		g.held = false
		g.owner = openLedgerTransition
		g.ownerSince = time.Time{}
	} else {
		g.held = true
		g.owner = next.role
		g.ownerSince = now
	}
	slowLog, shouldLog := g.takeSlowLogLocked(openLedgerGateSlowEvent{
		Role:           owner,
		Hold:           hold,
		QueuedPriority: priorityQueued,
		QueuedIngress:  ingressQueued,
	})
	g.mu.Unlock()

	if next != nil {
		close(next.ready)
	}
	if shouldLog {
		slowLog(openLedgerGateSlowEvent{
			Role:           owner,
			Hold:           hold,
			QueuedPriority: priorityQueued,
			QueuedIngress:  ingressQueued,
		})
	}
}

func (g *priorityGate) Snapshot() openLedgerGateSnapshot {
	g.mu.Lock()
	now := time.Now()
	defer g.mu.Unlock()
	snapshot := openLedgerGateSnapshot{
		Owner:          g.owner,
		Held:           g.held,
		QueuedPriority: len(g.priority),
		QueuedIngress:  len(g.ingress),
	}
	if g.held {
		snapshot.HeldFor = now.Sub(g.ownerSince)
	}
	return snapshot
}

func (g *priorityGate) setSlowLogger(fn func(openLedgerGateSlowEvent)) {
	g.mu.Lock()
	g.slowLog = fn
	g.mu.Unlock()
}

func (g *priorityGate) recordWait(wait openLedgerGateWait) {
	g.mu.Lock()
	slowLog, shouldLog := g.takeSlowLogLocked(openLedgerGateSlowEvent{
		Role:           wait.Role,
		Wait:           wait.Wait,
		QueuedPriority: wait.PriorityQueued,
		QueuedIngress:  wait.IngressQueued,
	})
	g.mu.Unlock()
	if shouldLog {
		slowLog(openLedgerGateSlowEvent{
			Role:           wait.Role,
			Wait:           wait.Wait,
			QueuedPriority: wait.PriorityQueued,
			QueuedIngress:  wait.IngressQueued,
		})
	}
}

func (g *priorityGate) takeSlowLogLocked(event openLedgerGateSlowEvent) (func(openLedgerGateSlowEvent), bool) {
	if g.slowLog == nil || (event.Wait < openLedgerGateSlowWait && event.Hold < openLedgerGateSlowHold) {
		return nil, false
	}
	now := time.Now()
	if !g.lastSlowLog.IsZero() && now.Sub(g.lastSlowLog) < openLedgerGateSlowInterval {
		return nil, false
	}
	g.lastSlowLog = now
	return g.slowLog, true
}
