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

func TestNewServerWiresClientLoadShedder(t *testing.T) {
	services := types.NewServiceContainer(nil)
	_ = NewServer(time.Second, services)

	if services.ClientLoad == nil {
		t.Fatal("NewServer did not wire services.ClientLoad")
	}
}

// 503 status mapping matches rippled ErrorCodes.cpp:114 (rpcTOO_BUSY row).
// Drives the generic dispatcher gate above MaxJobQueueClients (500) so the
// path runs through RequireNotBusyClient inside executeMethod.
func TestRpcTooBusyReturnsHTTP503(t *testing.T) {
	services := types.NewServiceContainer(nil)
	srv := NewServer(time.Second, services)
	srv.registry.Register("book_offers", &handlers.BookOffersMethod{})

	for i := int64(0); i <= types.MaxJobQueueClients; i++ {
		services.ClientLoad.Begin()
	}

	body := `{"method":"book_offers","params":[{"taker_pays":{"currency":"XRP"},"taker_gets":{"currency":"USD","issuer":"rUsd"}}]}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	// 192.0.2.1 is in TEST-NET-1 (RFC 5737), never localhost, so the
	// default port context resolves role=RoleGuest and the shedder gates
	// are not bypassed by the isUnlimited(RoleAdmin) carve-out.
	req.RemoteAddr = "192.0.2.1:1234"
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
	if msg, _ := result["error_message"].(string); msg != "The server is too busy to help you now." {
		t.Errorf("result.error_message = %q, want rippled-canonical", msg)
	}
}

func TestRequestUnderThresholdReturnsHTTP200(t *testing.T) {
	services := types.NewServiceContainer(nil)
	srv := NewServer(time.Second, services)
	srv.registry.Register("book_offers", &handlers.BookOffersMethod{})

	body := `{"method":"book_offers","params":[{}]}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	// 192.0.2.1 is in TEST-NET-1 (RFC 5737), never localhost, so the
	// default port context resolves role=RoleGuest and the shedder gates
	// are not bypassed by the isUnlimited(RoleAdmin) carve-out.
	req.RemoteAddr = "192.0.2.1:1234"
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
}
