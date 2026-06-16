package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type stubHandler struct {
	role    types.Role
	handle  func(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError)
	apiVers []int
}

func (s *stubHandler) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if s.handle != nil {
		return s.handle(ctx, params)
	}
	return map[string]any{"ok": true}, nil
}
func (s *stubHandler) RequiredRole() types.Role           { return s.role }
func (s *stubHandler) SupportedApiVersions() []int        { return s.apiVers }
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

func decodeEnvelope(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("invalid response JSON: %v\nbody: %s", err, string(body))
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("response missing result object: %s", string(body))
	}
	return result
}

// TestPostBodyLimit ensures POSTs larger than MaxRequestBytes are rejected
// with HTTP 400, matching rippled's ServerHandler.cpp:625-633 behaviour for
// oversized requests rather than being buffered into memory.
func TestPostBodyLimit(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "ping", &stubHandler{})

	pad := strings.Repeat("a", MaxRequestBytes+1)
	body := `{"method":"ping","params":[{"x":"` + pad + `"}]}`

	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
}

// TestRoleNotElevatableByHeader ensures that a remote peer cannot become
// Admin by sending X-Forwarded-For: 127.0.0.1 / X-Real-IP: 127.0.0.1.
func TestRoleNotElevatableByHeader(t *testing.T) {
	var observedRole types.Role
	srv := newHardeningServer(t, time.Second, "ping", &stubHandler{
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
			observedRole = ctx.Role
			return map[string]any{"ok": true}, nil
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

// TestTrustedProxyAttributesClientIPButNotAdmin confirms that when a
// peer is in the per-port secure_gateway allowlist, its X-Forwarded-For
// is honoured for ClientIP attribution — but the role is still derived
// from the socket peer, so a proxy with XFF: 127.0.0.1 cannot elevate
// to admin.
func TestTrustedProxyAttributesClientIPButNotAdmin(t *testing.T) {
	var observedRole types.Role
	var observedClientIP string
	srv := newHardeningServer(t, time.Second, "ping", &stubHandler{
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
			observedRole = ctx.Role
			observedClientIP = ctx.ClientIP
			return map[string]any{"ok": true}, nil
		},
	})
	_, gateway, _ := net.ParseCIDR("203.0.113.0/24")
	pc := &PortContext{SecureGatewayNets: []net.IPNet{*gateway}}

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
	req.RemoteAddr = "203.0.113.5:1234" // peer is the trusted proxy
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.5")
	req = req.WithContext(WithPortContext(req.Context(), pc))

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if observedClientIP != "198.51.100.7" {
		t.Fatalf("expected ClientIP from XFF=198.51.100.7, got %q", observedClientIP)
	}
	if observedRole == types.RoleAdmin {
		t.Fatalf("trusted proxy must not promote to admin; role=%v", observedRole)
	}
}

// TestCredentialsMaskedInErrorEnvelope ensures secret/seed/passphrase values
// supplied in params are replaced with the literal "<masked>" in the error
// response echo (matching rippled ServerHandler.cpp:535-542) and that the
// original values never appear on the wire.
func TestCredentialsMaskedInErrorEnvelope(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "submit", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			return nil, types.RpcErrorInvalidParams("bad")
		},
	})

	body := `{"method":"submit","params":[{
		"secret":"snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"seed":"shz",
		"passphrase":"hunter2",
		"seed_hex":"DEADBEEF",
		"tx_json":{"Secret":"NESTED","Seed":"x","Account":"rPubliclyOK"}
	}]}`

	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.5:1234"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	raw := rr.Body.Bytes()
	for _, bad := range []string{"snoPBrXtMeMyMHUVTgbuqAfg1SUTb", "hunter2", "DEADBEEF", "NESTED"} {
		if bytes.Contains(raw, []byte(bad)) {
			t.Fatalf("credential value %q leaked into error envelope: %s", bad, string(raw))
		}
	}
	// Decode the envelope and confirm each credential key is present and
	// holds the literal "<masked>" placeholder (rippled ServerHandler.cpp).
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("invalid response JSON: %v\nbody: %s", err, string(raw))
	}
	result, _ := env["result"].(map[string]any)
	request, _ := result["request"].(map[string]any)
	if request == nil {
		t.Fatalf("response missing request echo: %s", string(raw))
	}
	for _, key := range []string{"secret", "seed", "passphrase", "seed_hex"} {
		v, ok := request[key]
		if !ok {
			t.Fatalf("credential key %q missing from echo (expected masked value): %s", key, string(raw))
		}
		if v != "<masked>" {
			t.Fatalf("credential key %q has value %v; want <masked>: %s", key, v, string(raw))
		}
	}
	txJson, _ := request["tx_json"].(map[string]any)
	if txJson["Secret"] != "<masked>" || txJson["Seed"] != "<masked>" {
		t.Fatalf("nested tx_json credentials not masked: %v", txJson)
	}
	if txJson["Account"] != "rPubliclyOK" {
		t.Fatalf("non-credential field in tx_json was altered: %v", txJson)
	}
}

// TestHandlerPanicRecovered ensures a panicking handler returns an error
// envelope to the client instead of crashing the server goroutine.
func TestHandlerPanicRecovered(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "panic", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
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
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
			observed = ctx.Context
			return map[string]any{"ok": true}, nil
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

// TestSecureGatewayPromotesToIdentifiedWithUser confirms rippled-parity
// for requestRole (Role.cpp:108-116): a peer in secure_gateway_nets that
// presents an X-User header is RoleIdentified (unlimited resources) but
// is NOT admin. Without X-User the same peer is RoleProxy.
func TestSecureGatewayPromotesToIdentifiedWithUser(t *testing.T) {
	var observed *types.RpcContext
	srv := newHardeningServer(t, time.Second, "ping", &stubHandler{
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
			observed = ctx
			return map[string]any{"ok": true}, nil
		},
	})

	_, gateway, _ := net.ParseCIDR("203.0.113.0/24")
	pc := &PortContext{SecureGatewayNets: []net.IPNet{*gateway}}

	// With X-User → Identified, Unlimited=true, IsAdmin=false.
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
	req.RemoteAddr = "203.0.113.5:1234"
	req.Header.Set("X-User", "alice")
	req = req.WithContext(WithPortContext(req.Context(), pc))
	srv.ServeHTTP(httptest.NewRecorder(), req)

	if observed.Role != types.RoleIdentified {
		t.Fatalf("with X-User: expected RoleIdentified, got %v", observed.Role)
	}
	if observed.IsAdmin || !observed.Unlimited {
		t.Fatalf("with X-User: expected IsAdmin=false, Unlimited=true; got IsAdmin=%v Unlimited=%v",
			observed.IsAdmin, observed.Unlimited)
	}

	// Without X-User → Proxy, Unlimited=false.
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"ping","params":[{}]}`))
	req.RemoteAddr = "203.0.113.5:1234"
	req = req.WithContext(WithPortContext(req.Context(), pc))
	srv.ServeHTTP(httptest.NewRecorder(), req)

	if observed.Role != types.RoleProxy {
		t.Fatalf("without X-User: expected RoleProxy, got %v", observed.Role)
	}
	if observed.Unlimited {
		t.Fatalf("RoleProxy must not be Unlimited")
	}
}

// heavyStub is a stub MethodHandler that declares LoadHeavy via the
// optional LoadCharger interface — used to exercise the per-IP load
// budget rejection path.
type heavyStub struct{ stubHandler }

func (heavyStub) LoadKind() loadtrack.LoadKind { return loadtrack.LoadHeavy }

// Once the per-IP balance crosses DropThreshold, the overload-admission gate
// (gateLoad) rejects with rippled's canonical HTTP 503 "Server is overloaded"
// bare-string body (ServerHandler.cpp:739), not the slowDown result envelope.
func TestLoadTracker_RejectsAfterDropThreshold(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "path_find", &heavyStub{stubHandler{}})
	srv.loadTracker = loadtrack.New()

	var lastBody string
	for range 12 {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"path_find","params":[{}]}`))
		req.RemoteAddr = "198.51.100.7:5555"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		lastBody = rr.Body.String()
		if rr.Code == http.StatusServiceUnavailable {
			if got := strings.TrimSpace(rr.Body.String()); got != "Server is overloaded" {
				t.Fatalf("503 body = %q, want \"Server is overloaded\"", got)
			}
			// The denial must not ride the result envelope (the old slowDown-on-200 shape).
			if strings.Contains(rr.Body.String(), "result") || strings.Contains(rr.Body.String(), "slowDown") {
				t.Fatalf("overload denial leaked the result envelope: %s", rr.Body.String())
			}
			return
		}
	}
	t.Fatalf("never received HTTP 503 after 12 heavy invocations; last body %s", lastBody)
}

func TestLoadTracker_AdminBypassesCharge(t *testing.T) {
	srv := newHardeningServer(t, time.Second, "path_find", &heavyStub{stubHandler{}})
	srv.loadTracker = loadtrack.New()

	for i := range 50 {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"method":"path_find","params":[{}]}`))
		// 127.0.0.1 with no AdminNets → roleForRequest fallback → RoleAdmin → Unlimited.
		req.RemoteAddr = "127.0.0.1:5555"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		result := decodeEnvelope(t, rr.Body.Bytes())
		if result["error"] == "slowDown" {
			t.Fatalf("admin caller hit slowDown at iter %d (should be unlimited)", i)
		}
	}
}

func TestForwardedForParser(t *testing.T) {
	cases := []struct {
		name, header, value, want string
	}{
		{"xff plain", "X-Forwarded-For", "198.51.100.7", "198.51.100.7"},
		{"xff multi", "X-Forwarded-For", "198.51.100.7, 10.0.0.1", "198.51.100.7"},
		{"xff with port", "X-Forwarded-For", "198.51.100.7:9000", "198.51.100.7"},
		{"xff ipv6 bracketed", "X-Forwarded-For", `[2001:db8::1]:9000`, "2001:db8::1"},
		{"xff ipv6 bracketed quoted", "X-Forwarded-For", `"[2001:db8::1]:9000"`, "2001:db8::1"},
		{"forwarded for token", "Forwarded", `for=198.51.100.7`, "198.51.100.7"},
		{"forwarded for token semicolon", "Forwarded", `for=198.51.100.7;proto=https`, "198.51.100.7"},
		{"forwarded for ipv6 quoted bracketed", "Forwarded", `for="[2001:db8::1]:9000"`, "2001:db8::1"},
		{"forwarded prefers Forwarded over xff", "Forwarded", `for=198.51.100.7`, "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "/", nil)
			r.Header.Set(tc.header, tc.value)
			got := forwardedForHeader(r)
			if got != tc.want {
				t.Fatalf("got %q, want %q (input %s: %q)", got, tc.want, tc.header, tc.value)
			}
		})
	}
}
