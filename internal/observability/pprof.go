package observability

import (
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"
)

func EnablePProf() {
	runtime.SetMutexProfileFraction(100)
	runtime.SetBlockProfileRate(1_000_000)
}

func PProfHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/goroutine", serveGoroutineProfile)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func serveGoroutineProfile(w http.ResponseWriter, r *http.Request) {
	// debug >= 2 calls runtime.Stack(all=true). A live Go 1.24/amd64 node
	// suffered a fatal SIGSEGV in runtime.(*unwinder).next on this path during
	// periodic collection. This is a process-fatal runtime fault, not a panic
	// that HTTP recovery can contain. Keep binary and grouped-text profiles,
	// but don't let a diagnostic request take down the validator again.
	debug, _ := strconv.Atoi(r.FormValue("debug"))
	if debug >= 2 {
		http.Error(w, "full goroutine stack dumps are disabled after a runtime unwinder crash; use debug=1 or the binary goroutine profile", http.StatusBadRequest)
		return
	}
	pprof.Handler("goroutine").ServeHTTP(w, r)
}
