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
func TestRpcTooBusyReturnsHTTP503(t *testing.T) {
	services := types.NewServiceContainer(nil)
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
