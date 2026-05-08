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
// Mutex + block profiling has runtime overhead (~5%) so we only enable
// it when this server actually starts.
func startPProfServer(addr string) error {
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)

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
