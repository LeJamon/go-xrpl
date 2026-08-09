package rpc

import (
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type rpcDiagnostics struct {
	mu      sync.Mutex
	now     func() time.Time
	nextID  uint64
	methods map[string]types.RPCMethodDiagnostics
	current map[uint64]rpcActivity
}

type rpcActivity struct {
	method string
	start  time.Time
}

func NewRPCDiagnostics() types.RPCDiagnostics {
	return newRPCDiagnostics(time.Now)
}

func newRPCDiagnostics(now func() time.Time) *rpcDiagnostics {
	return &rpcDiagnostics{
		now:     now,
		methods: make(map[string]types.RPCMethodDiagnostics),
		current: make(map[uint64]rpcActivity),
	}
}

func (d *rpcDiagnostics) Start(method string) func(bool) {
	start := d.now()
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	stats := d.methods[method]
	stats.Started++
	d.methods[method] = stats
	d.current[id] = rpcActivity{method: method, start: start}
	d.mu.Unlock()

	var once sync.Once
	return func(panicked bool) {
		once.Do(func() {
			end := d.now()
			d.mu.Lock()
			activity, ok := d.current[id]
			if ok {
				delete(d.current, id)
				stats := d.methods[method]
				if panicked {
					stats.Errored++
				} else {
					stats.Finished++
				}
				stats.DurationUs += elapsedMicroseconds(activity.start, end)
				d.methods[method] = stats
			}
			d.mu.Unlock()
		})
	}
}

func (d *rpcDiagnostics) Snapshot() types.RPCDiagnosticsSnapshot {
	now := d.now()
	d.mu.Lock()
	methods := make(map[string]types.RPCMethodDiagnostics, len(d.methods))
	for method, stats := range d.methods {
		methods[method] = stats
	}
	type currentActivity struct {
		id uint64
		types.RPCActivity
	}
	current := make([]currentActivity, 0, len(d.current))
	for id, activity := range d.current {
		current = append(current, currentActivity{
			id: id,
			RPCActivity: types.RPCActivity{
				Method:     activity.method,
				DurationUs: elapsedMicroseconds(activity.start, now),
			},
		})
	}
	d.mu.Unlock()
	sort.Slice(current, func(i, j int) bool {
		if current[i].Method != current[j].Method {
			return current[i].Method < current[j].Method
		}
		return current[i].id < current[j].id
	})
	activities := make([]types.RPCActivity, len(current))
	for i := range current {
		activities[i] = current[i].RPCActivity
	}
	return types.RPCDiagnosticsSnapshot{Methods: methods, Current: activities}
}

func elapsedMicroseconds(start, end time.Time) uint64 {
	if !end.After(start) {
		return 0
	}
	return uint64(end.Sub(start).Microseconds())
}
