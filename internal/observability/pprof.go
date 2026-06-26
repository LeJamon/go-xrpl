package observability

import (
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
)

// StartPProf runs a pprof HTTP server on addr, blocking until it returns, and
// is shared by the long-running commands (server, replay-range) so profiles can
// be captured without each re-implementing the wiring. Run it in its own
// goroutine.
//
// Mutex and block profile rates are tuned to keep overhead bounded:
//   - MutexProfileFraction=100 samples 1-in-100 contention events; fraction=1
//     can dominate cost on hot locks without adding ranking precision.
//   - BlockProfileRate=1_000_000 ns samples blocks of ~1ms or longer, enough to
//     surface off-CPU (DB / blob-store) wait without flooding the profile.
func StartPProf(addr string) error {
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
