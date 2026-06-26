package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// printTestServer registers the real print handler with every section's
// backing service wired to representative values, so the fully-populated
// output can be exercised through the real JSON-RPC serialization path rather
// than only at the handler return value. Standalone mode never activates the
// overlay/consensus subsystems, so this is the only place the multi-section
// output is observed end-to-end.
func printTestServer(t *testing.T) *Server {
	t.Helper()
	svc := servicesForMissingMethods(newMockLedgerServiceMissingMethods())
	svc.PeerDisconnects = func() (uint64, uint64) { return 7, 3 }
	svc.JqTransOverflow = func() uint64 { return 9 }
	svc.LastCloseInfo = func() (int, int) { return 5, 1900 }
	svc.StateAccounting = func() types.StateAccountingSnapshot {
		return types.StateAccountingSnapshot{
			Modes:             map[string]types.StateAccountingEntry{"full": {Transitions: 2, DurationUs: 1000}},
			CurrentDurationUs: 500,
		}
	}

	srv := &Server{
		registry: types.NewMethodRegistry(),
		timeout:  time.Second,
		services: svc,
	}
	srv.registry.Register("print", &handlers.PrintMethod{})
	srv.SetPeerSource(&stubPeerSource{
		peers:   []map[string]any{{"address": "192.0.2.1:51235"}},
		cluster: map[string]any{},
	})
	return srv
}

func printOverWire(t *testing.T, srv *Server, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234" // loopback ⇒ admin
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	return decodeEnvelope(t, rr.Body.Bytes())
}

// TestPrintMethod_WireShape pins print's serialized contract: with all sections
// wired, it asserts the JSON value types observed after real marshaling.
// Cumulative counters are decimal strings (rippled std::to_string —
// NetworkOPs.cpp:2986-2991, 4843-4846, matching server_info); sequence and
// proposer counts stay numeric.
func TestPrintMethod_WireShape(t *testing.T) {
	srv := printTestServer(t)

	t.Run("all sections present with rippled-faithful types", func(t *testing.T) {
		res := printOverWire(t, srv, `{"method":"print","params":[{}]}`)

		for _, section := range []string{"ledger", "overlay", "counters", "last_close", "state_accounting"} {
			assert.Contains(t, res, section, "missing section %q", section)
		}

		// JSON strings decode back to Go strings; JSON numbers to float64.
		counters := res["counters"].(map[string]any)
		assert.Equal(t, "7", counters["peer_disconnects"])
		assert.Equal(t, "3", counters["peer_disconnects_resources"])
		assert.Equal(t, "9", counters["jq_trans_overflow"])

		sa := res["state_accounting"].(map[string]any)
		assert.Equal(t, "500", sa["current_duration_us"])
		full := sa["states"].(map[string]any)["full"].(map[string]any)
		assert.Equal(t, "2", full["transitions"])
		assert.Equal(t, "1000", full["duration_us"])

		assert.IsType(t, float64(0), res["last_close"].(map[string]any)["proposers"])
		assert.IsType(t, float64(0), res["overlay"].(map[string]any)["count"])
	})

	t.Run("subtree selector narrows output over the wire", func(t *testing.T) {
		res := printOverWire(t, srv, `{"method":"print","params":[{"params":["counters"]}]}`)
		assert.Contains(t, res, "counters")
		assert.NotContains(t, res, "ledger")
		assert.NotContains(t, res, "state_accounting")
	})

	t.Run("unknown selector yields empty result over the wire", func(t *testing.T) {
		res := printOverWire(t, srv, `{"method":"print","params":[{"params":["nope"]}]}`)
		for _, section := range []string{"ledger", "overlay", "counters", "last_close", "state_accounting"} {
			assert.NotContains(t, res, section)
		}
	})
}
