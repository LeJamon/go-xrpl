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
	r.txJobs = make(chan *peermanagement.InboundMessage, txQueueDepth)
	r.serveJobs = make(chan *peermanagement.InboundMessage, serveQueueDepth)
	if r.lifecycleReady == nil {
		r.lifecycleReady = make(chan struct{})
	}

	txJobs := r.txJobs
	serveJobs := r.serveJobs
	r.lifecycleWG.Add(txWorkerCount + serveWorkerCount)
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
				}
			}
		}()
	}
	for range serveWorkerCount {
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
				case msg := <-serveJobs:
					func() {
						defer func() { _ = msg.Close() }()
						r.handleGetLedger(msg)
					}()
				}
			}
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
	serveJobs := r.serveJobs
	r.txJobs = nil
	r.serveJobs = nil
	cancel := r.lifecycleCancel
	r.lifecycleCancel = nil
	cancel()
	r.lifecycleMu.Unlock()

	r.lifecycleWG.Wait()
	drainInboundMessages(txJobs)
	drainInboundMessages(serveJobs)
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
