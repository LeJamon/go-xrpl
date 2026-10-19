package adaptor

import (
	"context"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

type routerLifecycleState uint8

const (
	routerLifecycleInitial routerLifecycleState = iota
	routerLifecycleRunning
	routerLifecycleStopped
)

func (r *Router) startLifecycle(parent context.Context) (context.Context, bool) {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.lifecycleState != routerLifecycleInitial {
		return nil, false
	}

	ctx, cancel := context.WithCancel(parent)
	r.lifecycleState = routerLifecycleRunning
	r.lifecycleCtx = ctx
	r.lifecycleCancel = cancel
	if r.catchupReplay != nil {
		r.catchupReplay.startTasks(ctx)
	}
	r.txJobs = make(chan *peermanagement.InboundMessage, txQueueDepth)
	r.txSetLearnJobs = make(chan txSetLearnJob, txSetLearnQueueDepth)
	r.servePools = newLedgerServePools(ctx, serveWorkerCount)
	if r.lifecycleReady == nil {
		r.lifecycleReady = make(chan struct{})
	}

	txJobs := r.txJobs
	txSetLearnJobs := r.txSetLearnJobs
	r.lifecycleWG.Add(txWorkerCount + len(r.servePools))
	for range txWorkerCount {
		go func() {
			defer r.lifecycleWG.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				select {
				case <-ctx.Done():
					return
				case msg := <-txJobs:
					func() {
						defer func() { _ = msg.Close() }()
						r.handleTransaction(msg)
					}()
				case job := <-txSetLearnJobs:
					r.handleTxSetLearnJob(job)
				}
			}
		}()
	}
	for _, pool := range r.servePools {
		go func() {
			defer r.lifecycleWG.Done()
			_ = pool.Run(ctx)
		}()
	}
	close(r.lifecycleReady)
	return ctx, true
}

func (r *Router) lifecycleReadyChannel() <-chan struct{} {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.lifecycleReady == nil {
		r.lifecycleReady = make(chan struct{})
	}
	return r.lifecycleReady
}

func (r *Router) stopLifecycle() {
	r.lifecycleMu.Lock()
	if r.lifecycleState != routerLifecycleRunning {
		r.lifecycleMu.Unlock()
		return
	}
	r.lifecycleState = routerLifecycleStopped
	txJobs := r.txJobs
	servePools := r.servePools
	r.txJobs = nil
	// Queued learning owns detached blobs, not overlay reservations. It can
	// be discarded at shutdown; the acquired consensus set was delivered
	// independently. In-flight workers are still joined below.
	r.txSetLearnJobs = nil
	cancel := r.lifecycleCancel
	r.lifecycleCancel = nil
	cancel()
	r.lifecycleMu.Unlock()
	if r.catchupReplay != nil {
		r.catchupReplay.stopTasks()
	}

	for _, pool := range servePools {
		pool.Close()
	}
	r.lifecycleWG.Wait()
	drainInboundMessages(txJobs)
}

func drainInboundMessages(messages <-chan *peermanagement.InboundMessage) {
	for {
		select {
		case msg := <-messages:
			_ = msg.Close()
		default:
			return
		}
	}
}

func (r *Router) lifecycleContext() context.Context {
	r.lifecycleMu.RLock()
	defer r.lifecycleMu.RUnlock()
	return r.lifecycleCtx
}

func (r *Router) runLifecycleTask(fn func(context.Context)) bool {
	r.lifecycleMu.Lock()
	switch r.lifecycleState {
	case routerLifecycleInitial:
		r.lifecycleMu.Unlock()
		return false
	case routerLifecycleRunning:
		ctx := r.lifecycleCtx
		r.lifecycleWG.Add(1)
		go func() {
			defer r.lifecycleWG.Done()
			fn(ctx)
		}()
		r.lifecycleMu.Unlock()
		return true
	default:
		r.lifecycleMu.Unlock()
		return false
	}
}
