package cli

import "testing"

// TestHTTPWriteTimeoutExceedsDispatch pins the invariant that the transport
// WriteTimeout is strictly greater than the request dispatch timeout. net/http
// measures WriteTimeout across handler execution plus the response write, so if
// it were equal to (or below) the dispatch deadline a request that timed out
// would find its socket already closed and the client would see a connection
// reset instead of a clean timeout envelope.
func TestHTTPWriteTimeoutExceedsDispatch(t *testing.T) {
	if httpWriteTimeout <= rpcDispatchTimeout {
		t.Fatalf("httpWriteTimeout (%s) must be strictly greater than rpcDispatchTimeout (%s)",
			httpWriteTimeout, rpcDispatchTimeout)
	}
	if httpReadTimeout <= rpcDispatchTimeout {
		t.Fatalf("httpReadTimeout (%s) must exceed rpcDispatchTimeout (%s)",
			httpReadTimeout, rpcDispatchTimeout)
	}
	if httpReadHeaderTimeout <= 0 || httpReadHeaderTimeout >= httpReadTimeout {
		t.Fatalf("httpReadHeaderTimeout (%s) must be a positive slow-loris bound below httpReadTimeout (%s)",
			httpReadHeaderTimeout, httpReadTimeout)
	}
}
