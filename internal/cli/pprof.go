package cli

import (
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
)

// Mutex + block profile rates are tuned to keep overhead bounded:
//   - MutexProfileFraction=100 samples 1-in-100 contention events; fraction=1
//     can dominate cost on hot locks without adding ranking precision.
//   - BlockProfileRate=1_000_000 ns samples blocks of ~1ms or longer; rate=1
//     captures every channel/sync block — orders of magnitude more samples
//     than needed to identify off-CPU hotspots.
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
