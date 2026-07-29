// Package cleaner implements a background ledger-integrity verifier, the
// go-xrpl analog of rippled's LedgerCleaner.
//
// It walks the state and transaction SHAMap trees of a ledger (or a ledger
// range) against the content-addressed node store, reporting nodes that are
// missing or corrupt.
package cleaner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
)

// errNoFamily is returned when the cleaner has no node store to walk against.
var errNoFamily = errors.New("ledger_cleaner: no node store configured")

var errIncompleteLedger = errors.New("ledger_cleaner: ledger is incomplete")

const (
	interLedgerPause  = 50 * time.Millisecond
	defaultRetryDelay = 2 * time.Second
)

// LedgerSource supplies everything the cleaner needs to verify a ledger's
// trees against the node store. Implemented by an adapter over the ledger
// service; kept narrow so this package does not depend on the service.
type LedgerSource interface {
	// AvailableRange returns the inclusive [min, max] range of ledgers the
	// node can verify locally. ok is false when no ledger is available.
	AvailableRange() (min, max uint32, ok bool)

	// Ledger returns the locally indexed ledger header and tree roots.
	Ledger(ctx context.Context, seq uint32) (LedgerData, bool, error)

	// CanonicalHash returns the hash committed by the validated chain.
	CanonicalHash(ctx context.Context, seq uint32) ([32]byte, bool, error)

	// RepairLedgerIndex installs the canonical sequence-to-hash mapping and
	// reports whether relational ledger or transaction rows must be rewritten.
	RepairLedgerIndex(ctx context.Context, ledger LedgerData) (bool, error)

	// Family is the content-addressed node store the trees are walked against.
	Family() shamap.Family

	// Reacquire requests the ledger from peers after an incomplete or failed
	// verification. The cleaner retries the same sequence after the request.
	Reacquire(ctx context.Context, seq uint32) error

	// RepairTransactions rewrites the relational transaction indexes for seq.
	// Implementations without relational persistence return nil.
	RepairTransactions(ctx context.Context, seq uint32) error
}

type LedgerData struct {
	Sequence   uint32
	Hash       [32]byte
	ParentHash [32]byte
	StateRoot  [32]byte
	TxRoot     [32]byte
}

// Params configures a cleaning run; the fields mirror the parameters rippled's
// ledger_cleaner admin command accepts.
type Params struct {
	Ledger     *uint32 // single ledger; sets min==max and forces a deep check
	MinLedger  *uint32 // lower bound of the range
	MaxLedger  *uint32 // upper bound of the range
	Full       *bool   // set both CheckNodes and FixTxns
	CheckNodes *bool   // explicit node-check override
	FixTxns    *bool   // explicit relational-index repair override
	Stop       bool    // stop an in-progress run
}

// Status is a snapshot of the cleaner's state plus progress counters.
type Status struct {
	State          string // "idle" or "running"
	MinLedger      uint32
	MaxLedger      uint32
	CheckNodes     bool
	FixTxns        bool
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

	mu         sync.Mutex
	cond       *sync.Cond
	running    bool // worker goroutine is processing a range
	started    bool
	exit       bool
	min, max   uint32
	deep       bool
	fixTxns    bool
	failures   int
	generation uint64

	ledgersChecked uint64
	nodesChecked   uint64
	missingNodes   uint64
	lastError      string

	ctx        context.Context
	cancel     context.CancelFunc
	runCtx     context.Context
	runCancel  context.CancelFunc
	retryDelay time.Duration
	done       chan struct{}
}

// New creates a Cleaner. The worker does not run until Start is called.
func New(src LedgerSource, logger xrpllog.Logger) *Cleaner {
	c := &Cleaner{
		src:        src,
		logger:     logger,
		retryDelay: defaultRetryDelay,
		done:       make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c
}

// Start launches the background worker. Idempotent.
func (c *Cleaner) Start() {
	c.mu.Lock()
	if c.started || c.exit {
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
	if c.exit {
		started := c.started
		c.mu.Unlock()
		if started {
			<-c.done
		}
		return
	}
	c.exit = true
	c.running = false
	c.generation++
	if c.runCancel != nil {
		c.runCancel()
	}
	c.cancel()
	c.cond.Broadcast()
	if !c.started {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	<-c.done
}

// Clean configures and (unless Stop is set) starts a verification run, then
// returns the resulting status.
func (c *Cleaner) Clean(p Params) Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.exit {
		c.lastError = "ledger_cleaner: cleaner stopped"
		return c.statusLocked()
	}
	if p.Stop {
		c.generation++
		if c.runCancel != nil {
			c.runCancel()
			c.runCancel = nil
		}
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

	if c.runCancel != nil {
		c.runCancel()
	}
	c.generation++
	c.runCtx, c.runCancel = context.WithCancel(c.ctx)

	c.deep = false
	c.fixTxns = false
	c.failures = 0
	c.ledgersChecked = 0
	c.nodesChecked = 0
	c.missingNodes = 0
	c.lastError = ""

	if p.Ledger != nil {
		min, max = *p.Ledger, *p.Ledger
		c.deep = true
		c.fixTxns = true
	}
	if p.MaxLedger != nil {
		max = *p.MaxLedger
	}
	if p.MinLedger != nil {
		min = *p.MinLedger
	}
	if p.Full != nil {
		c.deep = *p.Full
		c.fixTxns = *p.Full
	}
	if p.FixTxns != nil {
		c.fixTxns = *p.FixTxns
	}
	if p.CheckNodes != nil {
		c.deep = *p.CheckNodes
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
	if c.exit {
		state = "stopped"
	} else if c.running {
		state = "running"
	}
	return Status{
		State:          state,
		MinLedger:      c.min,
		MaxLedger:      c.max,
		CheckNodes:     c.deep,
		FixTxns:        c.fixTxns,
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
		if c.min == 0 || c.max == 0 || c.min > c.max {
			c.min, c.max = 0, 0
			c.running = false
			if c.runCancel != nil {
				c.runCancel()
				c.runCancel = nil
			}
			c.mu.Unlock()
			continue
		}
		seq := c.max
		deep := c.deep
		fixTxns := c.fixTxns
		generation := c.generation
		runCtx := c.runCtx
		c.mu.Unlock()

		nodes, missing, repairTxns, reacquire, err := c.cleanLedger(runCtx, seq, deep)
		if err == nil && missing == 0 && (fixTxns || repairTxns) {
			err = c.src.RepairTransactions(runCtx, seq)
		}
		failed := err != nil || missing != 0
		if failed && runCtx.Err() == nil && (reacquire || missing != 0) {
			reacquireErr := c.src.Reacquire(runCtx, seq)
			if err == nil {
				err = errIncompleteLedger
			}
			if reacquireErr != nil {
				err = errors.Join(err, reacquireErr)
			}
		}

		c.mu.Lock()
		if c.exit {
			c.mu.Unlock()
			return
		}
		if generation != c.generation || !c.running {
			c.mu.Unlock()
			continue
		}
		c.nodesChecked += nodes
		c.missingNodes += missing
		if failed {
			c.failures++
			if err != nil {
				c.lastError = err.Error()
			}
			if c.logger != nil {
				c.logger.Warn("ledger_cleaner: ledger verification failed", "seq", seq, "err", err)
			}
			c.mu.Unlock()
			if c.sleepRun(runCtx, c.retryDelay) {
				continue
			}
			continue
		}

		c.failures = 0
		c.lastError = ""
		c.ledgersChecked++
		if c.logger != nil {
			c.logger.Debug("ledger_cleaner: ledger verified complete", "seq", seq, "nodes", nodes)
		}
		if seq == c.min {
			c.min++
		}
		if seq == c.max && c.max > 0 {
			c.max--
		}
		if c.min > c.max {
			c.running = false
			if c.runCancel != nil {
				c.runCancel()
				c.runCancel = nil
			}
			if c.logger != nil {
				c.logger.Info("ledger_cleaner: run complete",
					"ledgers_checked", c.ledgersChecked,
					"missing_nodes", c.missingNodes)
			}
		}
		c.mu.Unlock()

		if c.sleepRun(runCtx, interLedgerPause) {
			continue
		}
		if c.ctx.Err() != nil {
			return
		}
	}
}

func (c *Cleaner) sleepRun(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

// cleanLedger verifies one ledger's state and transaction trees. With deep
// set it walks every node; otherwise it only confirms the tree roots are
// present (a shallow check). It returns the number of nodes inspected and the
// number found missing or corrupt.
func (c *Cleaner) cleanLedger(
	ctx context.Context,
	seq uint32,
	deep bool,
) (nodes, missing uint64, repairTxns, reacquire bool, err error) {
	info, ok, err := c.src.Ledger(ctx, seq)
	if err != nil {
		return 0, 0, false, true, err
	}
	if !ok {
		return 0, 1, false, true, nil
	}
	if info.Sequence != seq {
		return 0, 0, false, true, fmt.Errorf(
			"ledger_cleaner: ledger sequence %d indexed at %d",
			info.Sequence,
			seq,
		)
	}
	canonicalHash, ok, err := c.src.CanonicalHash(ctx, seq)
	if err != nil {
		return 0, 0, false, true, err
	}
	if !ok {
		return 0, 0, false, true, fmt.Errorf("ledger_cleaner: canonical hash unavailable for ledger %d", seq)
	}
	if info.Hash != canonicalHash {
		return 0, 0, false, true, fmt.Errorf("ledger_cleaner: ledger %d hash does not match validated chain", seq)
	}
	expectedParent := [32]byte{}
	if seq > 1 {
		expectedParent, ok, err = c.src.CanonicalHash(ctx, seq-1)
		if err != nil {
			return 0, 0, false, true, err
		}
		if !ok {
			return 0, 0, false, true, fmt.Errorf(
				"ledger_cleaner: canonical parent unavailable for ledger %d",
				seq,
			)
		}
	}
	if info.ParentHash != expectedParent {
		return 0, 0, false, true, fmt.Errorf(
			"ledger_cleaner: ledger %d parent does not match validated chain",
			seq,
		)
	}
	repairTxns, err = c.src.RepairLedgerIndex(ctx, info)
	if err != nil {
		return 0, 0, false, false, err
	}
	family := c.src.Family()
	if family == nil {
		return 0, 0, repairTxns, false, errNoFamily
	}

	for _, t := range []struct {
		root    [32]byte
		mapType shamap.Type
	}{
		{info.StateRoot, shamap.TypeState},
		{info.TxRoot, shamap.TypeTransaction},
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
			return nodes, missing, repairTxns, true, cerr
		}
		nodes += uint64(res.InnerNodes + res.LeafNodes)
		missing += uint64(len(res.Missing) + len(res.Corrupt))
	}
	return nodes, missing, repairTxns, missing != 0, nil
}

func isZeroHash(h [32]byte) bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}
