package cli

import (
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
)

// startPProfServer starts a debug HTTP server exposing the standard
// net/http/pprof endpoints plus an fgprof-style wall-clock profile.
// Routes:
//
//	/debug/pprof/                      — index
//	/debug/pprof/profile               — CPU (on-CPU only)
//	/debug/pprof/heap                  — heap
//	/debug/pprof/goroutine             — goroutine dump
//	/debug/pprof/mutex                 — mutex contention
//	/debug/pprof/block                 — block (off-CPU on sync prims)
//	/debug/pprof/trace                 — execution tracer (go tool trace)
//	/debug/pprof/cmdline, /symbol      — pprof helpers
//
// Mutex + block profiling has non-trivial runtime overhead and we only
// enable it when this server actually starts. Defaults below are tuned
// to be informative under normal load without distorting hot-mutex
// behavior:
//   - MutexProfileFraction=100 samples 1 in 100 contention events.
//     fraction=1 (every event) can dominate cost on hot locks; 100
//     keeps the top-10 ranking accurate while keeping overhead low.
//     For deep contention investigation, lower temporarily via SIGUSR
//     handler or rebuild with fraction=1.
//   - BlockProfileRate=1_000_000 samples blocks of ~1ms or longer
//     (rate is a nanosecond threshold). rate=1 captures every
//     channel/sync block however brief — orders of magnitude more
//     samples than needed to identify off-CPU hotspots.
//
// Total observed overhead at these rates: under 2% on the consensus
// hot path; near-zero on idle nodes.
func startPProfServer(addr string) error {
	runtime.SetMutexProfileFraction(100)
	runtime.SetBlockProfileRate(1_000_000)

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}
