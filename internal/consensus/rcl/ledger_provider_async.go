package rcl

import (
	"context"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
)

const (
	ancestryWorkers       = 2
	ancestryQueueCapacity = 64
	ancestryLookupTimeout = 2 * time.Second
)

type ancestryJob struct {
	id     consensus.LedgerID
	flight *ancestryFlight
}

// All mutable fields other than wg are guarded by the provider's mutex.
type ancestryBackground struct {
	lookup  func(context.Context, [32]byte) (ledgerHeader, error)
	ctx     context.Context
	cancel  context.CancelFunc
	jobs    chan ancestryJob
	wg      sync.WaitGroup
	started bool
	stopped bool
}

// stop releases the owned context. The caller must hold the provider's mutex
// and join the workers after releasing it.
func (b *ancestryBackground) stop() {
	b.stopped = true
	b.cancel()
}

func newBackgroundAncestryProvider(lookup func(context.Context, [32]byte) (ledgerHeader, error)) *AncestryProvider {
	p := newAncestryProviderFromLookup(nil)
	ctx, cancel := context.WithCancel(context.Background())
	p.background = &ancestryBackground{
		lookup: lookup, ctx: ctx, cancel: cancel,
		jobs: make(chan ancestryJob, ancestryQueueCapacity),
	}
	return p
}

// Consensus calls this with its own locks held. It must never perform storage
// reads or wait for an in-flight ancestry build. Unknown ancestry stays parked
// in ValidationTracker and is retried on later validation/timer events.
func (p *AncestryProvider) ledgerByIDBackground(id consensus.LedgerID) (ledgertrie.Ledger, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.background
	if b.stopped || b.lookup == nil {
		return nil, false
	}
	fallback, cached := p.cacheGetLocked(id)
	if cached && !fallback.partial {
		return fallback, true
	}
	if flight, found := p.inflight[id]; found {
		if fallback == nil {
			fallback = flight.fallback
		}
		return backgroundAncestryResult(fallback)
	}
	flight := &ancestryFlight{done: make(chan struct{}), fallback: fallback}
	select {
	case b.jobs <- ancestryJob{id: id, flight: flight}:
		p.inflight[id] = flight
	default:
		// No unbounded goroutine or pending-ID list when storage is stalled.
		return backgroundAncestryResult(fallback)
	}
	if !b.started {
		b.started = true
		b.wg.Add(ancestryWorkers)
		for range ancestryWorkers {
			go p.runAncestryWorker()
		}
	}
	return backgroundAncestryResult(fallback)
}

func backgroundAncestryResult(result *providerLedger) (ledgertrie.Ledger, bool) {
	if result == nil {
		return nil, false
	}
	return result, true
}

func (p *AncestryProvider) runAncestryWorker() {
	b := p.background
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case job := <-b.jobs:
			ctx, cancel := context.WithTimeout(b.ctx, ancestryLookupTimeout)
			// The synchronous provider's panic boundary lives in its caller;
			// background execution needs the same containment in the worker.
			safeAncestryCall("background ancestry", func() {
				p.buildAndFinishUsing(job.id, job.flight, func() *providerLedger {
					return p.buildChainContext(ctx, job.id, func(hash [32]byte) (ledgerHeader, error) {
						return b.lookup(ctx, hash)
					})
				})
			})
			cancel()
		}
	}
}

// stop cancels SQL/SHAMap reads and joins the bounded worker set. Engine owns
// production providers and calls this outside its consensus mutex.
func (p *AncestryProvider) stop() {
	if p == nil || p.background == nil {
		return
	}
	p.mu.Lock()
	b := p.background
	b.stop()
	p.mu.Unlock()
	b.wg.Wait()
	p.mu.Lock()
	for id, flight := range p.inflight {
		flight.result = flight.fallback
		close(flight.done)
		delete(p.inflight, id)
	}
	p.mu.Unlock()
}
