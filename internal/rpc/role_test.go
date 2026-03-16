package rpc

import (
	"net"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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
	role := roleForRequest("10.1.2.3", pc)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin, got %v", role)
	}
}

func TestRoleForRequest_WithAdminNets_NoMatch(t *testing.T) {
	pc := &PortContext{
		AdminNets: []net.IPNet{mustParseCIDR("10.0.0.0/8")},
	}
	role := roleForRequest("192.168.1.1", pc)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest, got %v", role)
	}
}

func TestRoleForRequest_NilPortCtx_Localhost(t *testing.T) {
	role := roleForRequest("127.0.0.1", nil)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin for localhost with nil portCtx, got %v", role)
	}
}

func TestRoleForRequest_NilPortCtx_NonLocal(t *testing.T) {
	role := roleForRequest("10.0.0.1", nil)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest for non-local with nil portCtx, got %v", role)
	}
}

func TestRoleForRequest_EmptyAdminNets_FallsBackToLocalhost(t *testing.T) {
	pc := &PortContext{AdminNets: nil}
	role := roleForRequest("127.0.0.1", pc)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin for localhost with empty AdminNets, got %v", role)
	}
}

func TestRoleForRequest_IPv6Loopback(t *testing.T) {
	pc := &PortContext{
		AdminNets: []net.IPNet{mustParseCIDR("::1/128")},
	}
	role := roleForRequest("::1", pc)
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
	role := roleForRequest("172.20.1.1", pc)
	if role != types.RoleAdmin {
		t.Fatalf("expected RoleAdmin, got %v", role)
	}
	// Should not match either
	role = roleForRequest("8.8.8.8", pc)
	if role != types.RoleGuest {
		t.Fatalf("expected RoleGuest, got %v", role)
	}
}
