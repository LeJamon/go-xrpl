package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// TestNewServerWiresClientLoadShedder confirms NewServer attaches a default
// shared shedder to the services container so that handlers (including those
// reached via WebSocket later) see the same in-flight counter.
func TestNewServerWiresClientLoadShedder(t *testing.T) {
	services := types.NewServiceContainer(nil)
	_ = NewServer(time.Second, services)

	if services.ClientLoad == nil {
		t.Fatal("NewServer did not wire services.ClientLoad")
	}
}

// TestRpcTooBusyReturnsHTTP503 verifies the HTTP envelope written when a
// handler returns rpcTOO_BUSY: status code 503 (matching rippled
// ErrorCodes.cpp:114) and `result.error = "tooBusy"` in the body.
func TestRpcTooBusyReturnsHTTP503(t *testing.T) {
	services := types.NewServiceContainer(nil)
	// Force the shedder into the busy state regardless of in-flight count.
	services.ClientLoad = types.NewClientLoadShedder(-1)

	srv := NewServer(time.Second, services)
	srv.registry.Register("book_offers", &handlers.BookOffersMethod{})

	body := `{"method":"book_offers","params":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"rUsd"}}]}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d\nbody: %s", rr.Code, rr.Body.String())
	}

	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid response JSON: %v\nbody: %s", err, rr.Body.String())
	}
	result, _ := env["result"].(map[string]any)
	if result == nil {
		t.Fatalf("missing result envelope: %s", rr.Body.String())
	}
	if result["error"] != "tooBusy" {
		t.Errorf(`result.error = %v, want "tooBusy"`, result["error"])
	}
	if code, _ := result["error_code"].(float64); int(code) != types.RpcTOO_BUSY {
		t.Errorf("result.error_code = %v, want %d", result["error_code"], types.RpcTOO_BUSY)
	}
}

// TestRequestUnderThresholdReturnsHTTP200 is the negative control: when the
// shedder is idle, the same book_offers payload short-circuits on its normal
// validation error (missing fields) and rides on HTTP 200 — never 503.
func TestRequestUnderThresholdReturnsHTTP200(t *testing.T) {
	services := types.NewServiceContainer(nil)
	srv := NewServer(time.Second, services)
	srv.registry.Register("book_offers", &handlers.BookOffersMethod{})

	body := `{"method":"book_offers","params":[{}]}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
}
