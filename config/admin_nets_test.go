package config

import (
	"net"
	"testing"
)

func TestParseAdminNets_BareIPv4(t *testing.T) {
	p := PortConfig{Admin: []string{"127.0.0.1"}}
	nets, err := p.ParseAdminNets()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 {
		t.Fatalf("expected 1 net, got %d", len(nets))
	}
	ones, _ := nets[0].Mask.Size()
	if ones != 32 {
		t.Fatalf("expected /32, got /%d", ones)
	}
}

func TestParseAdminNets_BareIPv6(t *testing.T) {
	p := PortConfig{Admin: []string{"::1"}}
	nets, err := p.ParseAdminNets()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 {
		t.Fatalf("expected 1 net, got %d", len(nets))
	}
	ones, _ := nets[0].Mask.Size()
	if ones != 128 {
		t.Fatalf("expected /128, got /%d", ones)
	}
}

func TestParseAdminNets_CIDR(t *testing.T) {
	p := PortConfig{Admin: []string{"10.0.0.0/8", "fe80::/10"}}
	nets, err := p.ParseAdminNets()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("expected 2 nets, got %d", len(nets))
	}
}

func TestParseAdminNets_Empty(t *testing.T) {
	p := PortConfig{Admin: nil}
	nets, err := p.ParseAdminNets()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 0 {
		t.Fatalf("expected 0 nets, got %d", len(nets))
	}
}

func TestParseAdminNets_Invalid(t *testing.T) {
	p := PortConfig{Admin: []string{"not-an-ip"}}
	_, err := p.ParseAdminNets()
	if err == nil {
		t.Fatal("expected error for invalid IP")
	}
}

func TestParseAdminNets_SkipsBlank(t *testing.T) {
	p := PortConfig{Admin: []string{"", "  ", "127.0.0.1"}}
	nets, err := p.ParseAdminNets()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 {
		t.Fatalf("expected 1 net, got %d", len(nets))
	}
}

func TestIPInNets_Match(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	if !IPInNets(net.ParseIP("10.1.2.3"), []net.IPNet{*cidr}) {
		t.Fatal("expected 10.1.2.3 to match 10.0.0.0/8")
	}
}

func TestIPInNets_NoMatch(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	if IPInNets(net.ParseIP("192.168.1.1"), []net.IPNet{*cidr}) {
		t.Fatal("expected 192.168.1.1 NOT to match 10.0.0.0/8")
	}
}

func TestIPInNets_IPv4Mapped(t *testing.T) {
	// ::ffff:127.0.0.1 should match 127.0.0.0/8
	_, cidr, _ := net.ParseCIDR("127.0.0.0/8")
	// net.ParseIP("::ffff:127.0.0.1") returns a 16-byte IP
	ip := net.ParseIP("::ffff:127.0.0.1")
	if !IPInNets(ip, []net.IPNet{*cidr}) {
		t.Fatal("expected ::ffff:127.0.0.1 to match 127.0.0.0/8 after normalization")
	}
}

func TestIPInNets_EmptyNets(t *testing.T) {
	if IPInNets(net.ParseIP("127.0.0.1"), nil) {
		t.Fatal("expected no match with nil nets")
	}
}

func TestIPInNets_IPv6(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("fe80::/10")
	if !IPInNets(net.ParseIP("fe80::1"), []net.IPNet{*cidr}) {
		t.Fatal("expected fe80::1 to match fe80::/10")
	}
	if IPInNets(net.ParseIP("2001:db8::1"), []net.IPNet{*cidr}) {
		t.Fatal("expected 2001:db8::1 NOT to match fe80::/10")
	}
}
