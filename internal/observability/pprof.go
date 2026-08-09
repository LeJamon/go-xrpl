package observability

import (
	"net/http"
	"net/http/pprof"
	"runtime"
)

func EnablePProf() {
	runtime.SetMutexProfileFraction(100)
	runtime.SetBlockProfileRate(1_000_000)
}

func PProfHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}
