package rpc

import (
	"net"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func mustParseCIDR(s string) net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return *n
}

func TestRoleForRequest_WithAdminNets_Match(t *testing.T) {
	pc := &PortContext{
		AdminNets: []net.IPNet{mustParseCIDR("10.0.0.0/8")},
	}
	role := roleForRequest("10.1.2.3", "", nil, pc)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin, got %v", role)
	}
}

func TestRoleForRequest_WithAdminNets_NoMatch(t *testing.T) {
	pc := &PortContext{
		AdminNets: []net.IPNet{mustParseCIDR("10.0.0.0/8")},
	}
	role := roleForRequest("192.168.1.1", "", nil, pc)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest, got %v", role)
	}
}

func TestRoleForRequest_NilPortCtx_Localhost(t *testing.T) {
	role := roleForRequest("127.0.0.1", "", nil, nil)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest for localhost with nil portCtx, got %v", role)
	}
}

func TestRoleForRequest_NilPortCtx_NonLocal(t *testing.T) {
	role := roleForRequest("10.0.0.1", "", nil, nil)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest for non-local with nil portCtx, got %v", role)
	}
}

func TestRoleForRequest_EmptyAdminNets_LocalhostIsGuest(t *testing.T) {
	pc := &PortContext{AdminNets: nil}
	role := roleForRequest("127.0.0.1", "", nil, pc)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest for localhost with empty AdminNets, got %v", role)
	}
}

func TestRoleForRequest_ExplicitLoopbackAdmin(t *testing.T) {
	pc := &PortContext{AdminNets: []net.IPNet{mustParseCIDR("127.0.0.0/8")}}
	role := roleForRequest("127.0.0.1", "", nil, pc)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin for localhost in AdminNets, got %v", role)
	}
}

func TestRoleForRequest_IPv6Loopback(t *testing.T) {
	pc := &PortContext{
		AdminNets: []net.IPNet{mustParseCIDR("::1/128")},
	}
	role := roleForRequest("::1", "", nil, pc)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin for ::1, got %v", role)
	}
}

func TestRoleForRequest_MultipleNets(t *testing.T) {
	pc := &PortContext{
		AdminNets: []net.IPNet{
			mustParseCIDR("10.0.0.0/8"),
			mustParseCIDR("172.16.0.0/12"),
		},
	}
	// Should match second net
	role := roleForRequest("172.20.1.1", "", nil, pc)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin, got %v", role)
	}
	// Should not match either
	role = roleForRequest("8.8.8.8", "", nil, pc)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest, got %v", role)
	}
}

func TestRoleForRequest_AdminCredentials(t *testing.T) {
	pc := &PortContext{
		AdminNets:     []net.IPNet{mustParseCIDR("10.0.0.0/8")},
		AdminUser:     "root",
		AdminPassword: "secret",
	}
	tests := []struct {
		name   string
		params map[string]any
		want   types.Role
	}{
		{name: "missing", want: types.RoleGuest},
		{name: "wrong user", params: map[string]any{"admin_user": "other", "admin_password": "secret"}, want: types.RoleGuest},
		{name: "wrong password", params: map[string]any{"admin_user": "root", "admin_password": "other"}, want: types.RoleGuest},
		{name: "non-string user", params: map[string]any{"admin_user": 1, "admin_password": "secret"}, want: types.RoleGuest},
		{name: "non-string password", params: map[string]any{"admin_user": "root", "admin_password": true}, want: types.RoleGuest},
		{name: "exact", params: map[string]any{"admin_user": "root", "admin_password": "secret"}, want: types.RoleAdmin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := roleForRequest("10.1.2.3", "", test.params, pc); got != test.want {
				t.Fatalf("roleForRequest() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRoleForRequest_OneConfiguredCredentialStillRequiresBothFields(t *testing.T) {
	pc := &PortContext{
		AdminNets:     []net.IPNet{mustParseCIDR("10.0.0.0/8")},
		AdminPassword: "secret",
	}
	if got := roleForRequest("10.1.2.3", "", map[string]any{"admin_password": "secret"}, pc); got != types.RoleGuest {
		t.Fatalf("missing admin_user role = %v, want %v", got, types.RoleGuest)
	}
	params := map[string]any{"admin_user": "", "admin_password": "secret"}
	if got := roleForRequest("10.1.2.3", "", params, pc); got != types.RoleAdmin {
		t.Fatalf("exact empty admin_user role = %v, want %v", got, types.RoleAdmin)
	}
}
