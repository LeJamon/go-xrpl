package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConstantTimeCredentialsMatch(t *testing.T) {
	const (
		user     = "operator"
		password = "transport-secret"
	)
	for _, test := range []struct {
		name        string
		gotUser     string
		gotPassword string
		want        bool
	}{
		{name: "correct", gotUser: user, gotPassword: password, want: true},
		{name: "wrong user", gotUser: "attacker", gotPassword: password},
		{name: "wrong password", gotUser: user, gotPassword: "wrong"},
		{name: "different length user", gotUser: "op", gotPassword: password},
		{name: "different length password", gotUser: user, gotPassword: "transport-secret-extra"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := constantTimeCredentialsMatch(test.gotUser, test.gotPassword, user, password); got != test.want {
				t.Fatalf("constantTimeCredentialsMatch() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestBasicAuthMatches(t *testing.T) {
	pc := &PortContext{User: "operator", Password: "transport-secret"}
	for _, test := range []struct {
		name     string
		user     string
		password string
		setAuth  bool
		want     bool
	}{
		{name: "correct", user: "operator", password: "transport-secret", setAuth: true, want: true},
		{name: "wrong user", user: "attacker", password: "transport-secret", setAuth: true},
		{name: "wrong password", user: "operator", password: "wrong", setAuth: true},
		{name: "missing authorization", want: false},
		{name: "different length user", user: "op", password: "transport-secret", setAuth: true},
		{name: "different length password", user: "operator", password: "transport-secret-extra", setAuth: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if test.setAuth {
				req.SetBasicAuth(test.user, test.password)
			}
			if got := basicAuthMatches(req, pc); got != test.want {
				t.Fatalf("basicAuthMatches() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAdminCredentialsMatch(t *testing.T) {
	pc := &PortContext{AdminUser: "root", AdminPassword: "admin-secret"}
	for _, test := range []struct {
		name   string
		params map[string]any
		want   bool
	}{
		{name: "correct", params: map[string]any{"admin_user": "root", "admin_password": "admin-secret"}, want: true},
		{name: "wrong user", params: map[string]any{"admin_user": "attacker", "admin_password": "admin-secret"}},
		{name: "wrong password", params: map[string]any{"admin_user": "root", "admin_password": "wrong"}},
		{name: "missing credentials", params: map[string]any{}},
		{name: "different length user", params: map[string]any{"admin_user": "r", "admin_password": "admin-secret"}},
		{name: "different length password", params: map[string]any{"admin_user": "root", "admin_password": "admin-secret-extra"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := adminCredentialsMatch(test.params, pc); got != test.want {
				t.Fatalf("adminCredentialsMatch() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAuthorizeTransportClosesRejectedRequests(t *testing.T) {
	pc := &PortContext{
		User:           "operator",
		Password:       "transport-secret",
		AllowedOrigins: []string{"https://console.example"},
	}
	for _, test := range []struct {
		name    string
		request func() *http.Request
		status  int
	}{
		{
			name: "malformed origin",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.Header.Set("Origin", "https://")
				return req
			},
			status: http.StatusForbidden,
		},
		{
			name: "disallowed origin",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.Header.Set("Origin", "https://attacker.example")
				return req
			},
			status: http.StatusForbidden,
		},
		{
			name: "duplicate origin",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.Header.Add("Origin", "https://console.example")
				req.Header.Add("Origin", "https://attacker.example")
				return req
			},
			status: http.StatusForbidden,
		},
		{
			name: "missing authorization",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/", nil)
			},
			status: http.StatusUnauthorized,
		},
		{
			name: "wrong authorization",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.SetBasicAuth("operator", "wrong")
				return req
			},
			status: http.StatusUnauthorized,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := test.request()
			rr := httptest.NewRecorder()
			require.False(t, authorizeTransport(rr, req, pc))
			require.Equal(t, test.status, rr.Code)
			require.True(t, req.Close)
			require.Equal(t, "close", rr.Header().Get("Connection"))
		})
	}
}
