package adaptor

import "sync"

// ledgerAcceptWorker serializes the potentially long ledger-application job
// away from the consensus heartbeat. Its one-slot queue makes admission
// bounded while still allowing the worker to finish the one accepted round
// that is parked by the engine.
type ledgerAcceptWorker struct {
	mu       sync.Mutex
	queue    chan func()
	stop     chan struct{}
	done     chan struct{}
	started  bool
	stopping bool
}

func newLedgerAcceptWorker() *ledgerAcceptWorker {
	return &ledgerAcceptWorker{
		queue: make(chan func(), 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// deferAccept admits one completion callback without waiting for a running
// build. A false return leaves ownership with the caller, as required by the
// consensus deferral contract.
func (w *ledgerAcceptWorker) deferAccept(complete func()) bool {
	if complete == nil {
		return false
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopping {
		return false
	}
	if !w.started {
		w.started = true
		go w.run()
	}
	select {
	case w.queue <- complete:
		return true
	default:
		return false
	}
}

func (w *ledgerAcceptWorker) run() {
	defer close(w.done)
	for {
		select {
		case complete := <-w.queue:
			complete()
		case <-w.stop:
			for {
				select {
				case complete := <-w.queue:
					complete()
				default:
					return
				}
			}
		}
	}
}

// stopAndWait rejects new work, drains admitted callbacks, and joins the
// worker. A callback is always run exactly once after admission, including
// when shutdown races with queueing.
func (w *ledgerAcceptWorker) stopAndWait() {
	w.mu.Lock()
	if w.stopping {
		done := w.done
		started := w.started
		w.mu.Unlock()
		if started {
			<-done
		}
		return
	}
	w.stopping = true
	started := w.started
	if started {
		close(w.stop)
	}
	w.mu.Unlock()
	if started {
		<-w.done
	}
}
