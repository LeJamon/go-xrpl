package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

type stubHandler struct {
	role    types.Role
	handle  func(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError)
	apiVers []int
}

func (s *stubHandler) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if s.handle != nil {
		return s.handle(ctx, params)
	}
	return map[string]interface{}{"ok": true}, nil
}
func (s *stubHandler) RequiredRole() types.Role         { return s.role }
func (s *stubHandler) SupportedApiVersions() []int      { return s.apiVers }
func (s *stubHandler) RequiredCondition() types.Condition { return types.NoCondition }

func newHardeningServer(t *testing.T, timeout time.Duration, method string, h types.MethodHandler) *Server {
	t.Helper()
	srv := &Server{
		registry: types.NewMethodRegistry(),
		timeout:  timeout,
		services: types.NewServiceContainer(nil),
	}
	srv.registry.Register(method, h)
	return srv
}

func decodeEnvelope(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var env map[string]interface{}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("invalid response JSON: %v\nbody: %s", err, string(body))
	}
	result, ok := env["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("response missing result object: %s", string(body))
	}
	return result
}

// TestPostBodyLimit ensures POSTs larger than MaxRequestBytes are rejected
// with an error envelope rather than being buffered into memory.
func TestPostBodyLimit(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "ping", &stubHandler{})

	// Build a body that exceeds MaxRequestBytes.
	pad := strings.Repeat("a", MaxRequestBytes+1)
	body := `{"method":"ping","params":[{"x":"` + pad + `"}]}`

	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 envelope, got %d", rr.Code)
	}
	result := decodeEnvelope(t, rr.Body.Bytes())
	if result["error"] != "invalidParams" {
		t.Fatalf("expected error=invalidParams, got %v\nbody: %s", result["error"], rr.Body.String())
	}
}

// TestRoleNotElevatableByHeader ensures that a remote peer cannot become
// Admin by sending X-Forwarded-For: 127.0.0.1 / X-Real-IP: 127.0.0.1.
func TestRoleNotElevatableByHeader(t *testing.T) {
	var observedRole types.Role
	srv := newHardeningServer(t, time.Second, "ping", &stubHandler{
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (interface{}, *types.RpcError) {
			observedRole = ctx.Role
			return map[string]interface{}{"ok": true}, nil
		},
	})

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
	req.RemoteAddr = "203.0.113.5:1234" // non-local peer
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if observedRole == types.RoleAdmin {
		t.Fatalf("non-local peer elevated to admin via XFF; role=%v", observedRole)
	}
}

// TestCredentialsStrippedFromErrorEnvelope ensures secret/seed/passphrase
// values supplied in params don't leak back in the error response request echo.
func TestCredentialsStrippedFromErrorEnvelope(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "submit", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (interface{}, *types.RpcError) {
			return nil, types.RpcErrorInvalidParams("bad")
		},
	})

	body := `{"method":"submit","params":[{
		"secret":"snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"seed":"shz",
		"passphrase":"hunter2",
		"key":"ED1234",
		"tx_json":{"Secret":"NESTED","Seed":"x","Account":"rPubliclyOK"}
	}]}`

	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.5:1234"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	raw := rr.Body.Bytes()
	for _, bad := range []string{"snoPBrXtMeMyMHUVTgbuqAfg1SUTb", "hunter2", "NESTED", "\"secret\"", "\"seed\"", "\"passphrase\""} {
		if bytes.Contains(raw, []byte(bad)) {
			t.Fatalf("credential %q leaked into error envelope: %s", bad, string(raw))
		}
	}
	// Sanity: non-credential field still echoed.
	if !bytes.Contains(raw, []byte("rPubliclyOK")) {
		t.Fatalf("expected non-credential field to survive in echo: %s", string(raw))
	}
}

// TestHandlerPanicRecovered ensures a panicking handler returns an error
// envelope to the client instead of crashing the server goroutine.
func TestHandlerPanicRecovered(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "panic", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (interface{}, *types.RpcError) {
			panic("synthetic panic")
		},
	})

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"panic","params":[{}]}`))
	req.RemoteAddr = "203.0.113.5:1234"
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req) // must not propagate the panic

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	result := decodeEnvelope(t, rr.Body.Bytes())
	if result["error"] != "internal" {
		t.Fatalf("expected error=internal after panic, got %v\nbody: %s", result["error"], rr.Body.String())
	}
}

// TestDispatchHasDeadline ensures the configured Server.timeout produces a
// context with a deadline that handlers can observe.
func TestDispatchHasDeadline(t *testing.T) {
	var observed context.Context
	srv := newHardeningServer(t, 250*time.Millisecond, "ping", &stubHandler{
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (interface{}, *types.RpcError) {
			observed = ctx.Context
			return map[string]interface{}{"ok": true}, nil
		},
	})

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if observed == nil {
		t.Fatal("handler did not observe context")
	}
	if _, ok := observed.Deadline(); !ok {
		t.Fatal("expected dispatch context to have a deadline")
	}
}
