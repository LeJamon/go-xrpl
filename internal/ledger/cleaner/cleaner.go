// Package cleaner implements a background ledger-integrity verifier, the
// go-xrpl analog of rippled's LedgerCleaner.
//
// It walks the state and transaction SHAMap trees of a ledger (or a ledger
// range) against the content-addressed node store, reporting nodes that are
// missing or corrupt.
//
// Divergence from rippled, by design: rippled re-acquires missing nodes from
// peers and loops on a ledger until it is whole. go-xrpl has no inbound-ledger
// acquisition wired here, so the worker reports gaps and advances rather than
// blocking — every ledger in the range is checked exactly once. Re-acquisition
// (and rippled's fix_txns relational-index repair) are follow-ups that land
// with the content-addressed persistence migration.
package cleaner

import (
	"context"
	"errors"
	"sync"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
)

// errNoFamily is returned when the cleaner has no node store to walk against.
var errNoFamily = errors.New("ledger_cleaner: no node store configured")

// interLedgerPause is the small courtesy delay between ledgers so the verifier
// never monopolises the node store.
const interLedgerPause = 50 * time.Millisecond

// LedgerSource supplies everything the cleaner needs to verify a ledger's
// trees against the node store. Implemented by an adapter over the ledger
// service; kept narrow so this package does not depend on the service.
type LedgerSource interface {
	// AvailableRange returns the inclusive [min, max] range of ledgers the
	// node can verify locally. ok is false when no ledger is available.
	AvailableRange() (min, max uint32, ok bool)

	// LedgerRoots returns the state-tree and transaction-tree root hashes for
	// a ledger sequence. ok is false when the ledger is unknown locally. A
	// zero hash denotes an empty tree.
	LedgerRoots(seq uint32) (stateRoot, txRoot [32]byte, ok bool)

	// Family is the content-addressed node store the trees are walked against.
	Family() shamap.Family
}

// Params configures a cleaning run; the fields mirror the parameters rippled's
// ledger_cleaner admin command accepts.
type Params struct {
	Ledger     *uint32 // single ledger; sets min==max and forces a deep check
	MinLedger  *uint32 // lower bound of the range
	MaxLedger  *uint32 // upper bound of the range
	Full       bool    // deep check: walk every node (implies CheckNodes)
	CheckNodes bool    // walk every node rather than just the roots
	Stop       bool    // stop an in-progress run
}

// Status is a snapshot of the cleaner's state plus progress counters.
type Status struct {
	State          string // "idle" or "running"
	MinLedger      uint32
	MaxLedger      uint32
	CheckNodes     bool
	Failures       int
	LedgersChecked uint64
	NodesChecked   uint64
	MissingNodes   uint64
	LastError      string
}

// Cleaner is the background ledger-integrity verifier.
type Cleaner struct {
	src    LedgerSource
	logger xrpllog.Logger

	mu       sync.Mutex
	cond     *sync.Cond
	running  bool // worker goroutine is processing a range
	started  bool
	exit     bool
	min, max uint32
	deep     bool
	failures int

	ledgersChecked uint64
	nodesChecked   uint64
	missingNodes   uint64
	lastError      string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a Cleaner. The worker does not run until Start is called.
func New(src LedgerSource, logger xrpllog.Logger) *Cleaner {
	c := &Cleaner{
		src:    src,
		logger: logger,
		done:   make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c
}

// Start launches the background worker. Idempotent.
func (c *Cleaner) Start() {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()
	go c.run()
}

// Stop signals the worker to exit and waits for it to finish. Idempotent.
func (c *Cleaner) Stop() {
	c.mu.Lock()
	if !c.started || c.exit {
		c.mu.Unlock()
		return
	}
	c.exit = true
	c.cancel()
	c.cond.Broadcast()
	c.mu.Unlock()
	<-c.done
}

// Clean configures and (unless Stop is set) starts a verification run, then
// returns the resulting status.
func (c *Cleaner) Clean(p Params) Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	if p.Stop {
		c.running = false
		c.min, c.max = 0, 0
		c.cond.Broadcast()
		return c.statusLocked()
	}

	// Default the range to the locally-available validated range, then let
	// explicit parameters narrow it.
	min, max, ok := c.src.AvailableRange()
	if !ok {
		c.lastError = "no ledgers available to verify"
		return c.statusLocked()
	}

	c.deep = p.Full || p.CheckNodes
	c.failures = 0
	c.lastError = ""

	if p.MinLedger != nil && *p.MinLedger > min {
		min = *p.MinLedger
	}
	if p.MaxLedger != nil && *p.MaxLedger < max {
		max = *p.MaxLedger
	}
	if p.Ledger != nil {
		// A single ledger forces a deep check.
		min, max = *p.Ledger, *p.Ledger
		c.deep = true
	}

	c.min, c.max = min, max
	c.running = true
	c.cond.Broadcast()
	return c.statusLocked()
}

// Status returns a snapshot of the current state.
func (c *Cleaner) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *Cleaner) statusLocked() Status {
	state := "idle"
	if c.running {
		state = "running"
	}
	return Status{
		State:          state,
		MinLedger:      c.min,
		MaxLedger:      c.max,
		CheckNodes:     c.deep,
		Failures:       c.failures,
		LedgersChecked: c.ledgersChecked,
		NodesChecked:   c.nodesChecked,
		MissingNodes:   c.missingNodes,
		LastError:      c.lastError,
	}
}

// run is the worker loop: it sleeps until a run is configured, then drains the
// range one ledger at a time.
func (c *Cleaner) run() {
	defer close(c.done)
	for {
		c.mu.Lock()
		for !c.exit && !c.running {
			c.cond.Wait()
		}
		if c.exit {
			c.mu.Unlock()
			return
		}
		// Process from the top of the range downward.
		if c.min > c.max {
			c.running = false
			c.mu.Unlock()
			continue
		}
		seq := c.max
		deep := c.deep
		c.mu.Unlock()

		nodes, missing, err := c.cleanLedger(c.ctx, seq, deep)

		c.mu.Lock()
		if c.exit {
			c.mu.Unlock()
			return
		}
		c.ledgersChecked++
		c.nodesChecked += nodes
		c.missingNodes += missing
		if err != nil {
			c.failures++
			c.lastError = err.Error()
			if c.logger != nil {
				c.logger.Warn("ledger_cleaner: ledger verification failed", "seq", seq, "err", err)
			}
		} else if missing > 0 {
			c.failures++
			if c.logger != nil {
				c.logger.Warn("ledger_cleaner: incomplete ledger", "seq", seq, "missing_nodes", missing)
			}
		} else if c.logger != nil {
			c.logger.Debug("ledger_cleaner: ledger verified complete", "seq", seq, "nodes", nodes)
		}
		// Advance regardless of outcome: with no peer re-acquisition there is
		// nothing to retry, so draining the range guarantees termination.
		if seq == c.min {
			c.min++
		}
		if seq == c.max && c.max > 0 {
			c.max--
		}
		if c.min > c.max {
			c.running = false
			if c.logger != nil {
				c.logger.Info("ledger_cleaner: run complete",
					"ledgers_checked", c.ledgersChecked,
					"missing_nodes", c.missingNodes,
					"failures", c.failures)
			}
		}
		c.mu.Unlock()

		if c.sleep(interLedgerPause) {
			return
		}
	}
}

// sleep pauses for d, returning true if the cleaner was stopped meanwhile.
func (c *Cleaner) sleep(d time.Duration) (stopped bool) {
	select {
	case <-c.ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

// cleanLedger verifies one ledger's state and transaction trees. With deep
// set it walks every node; otherwise it only confirms the tree roots are
// present (a shallow check). It returns the number of nodes inspected and the
// number found missing or corrupt.
func (c *Cleaner) cleanLedger(ctx context.Context, seq uint32, deep bool) (nodes, missing uint64, err error) {
	stateRoot, txRoot, ok := c.src.LedgerRoots(seq)
	if !ok {
		return 0, 1, nil // ledger unavailable locally; counts as one gap
	}
	family := c.src.Family()
	if family == nil {
		return 0, 0, errNoFamily
	}

	for _, t := range []struct {
		root    [32]byte
		mapType shamap.Type
	}{
		{stateRoot, shamap.TypeState},
		{txRoot, shamap.TypeTransaction},
	} {
		if isZeroHash(t.root) {
			continue // empty tree
		}

		sm, ferr := shamap.NewFromRootHash(t.mapType, t.root, family)
		if ferr != nil {
			// The root node itself is missing or unreadable.
			missing++
			continue
		}

		if !deep {
			nodes++ // root present; shallow check stops here
			continue
		}

		res, cerr := sm.CheckComplete(ctx)
		if cerr != nil {
			return nodes, missing, cerr
		}
		nodes += uint64(res.InnerNodes + res.LeafNodes)
		missing += uint64(len(res.Missing) + len(res.Corrupt))
	}
	return nodes, missing, nil
}

func isZeroHash(h [32]byte) bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}
