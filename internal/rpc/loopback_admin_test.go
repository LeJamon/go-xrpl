package rpc

import (
	"net"
	"net/http"
)

func loopbackAdminPortContext() *PortContext {
	_, ipv4, _ := net.ParseCIDR("127.0.0.0/8")
	_, ipv6, _ := net.ParseCIDR("::1/128")
	return &PortContext{AdminNets: []net.IPNet{*ipv4, *ipv6}}
}

func withLoopbackAdmin(req *http.Request) *http.Request {
	return req.WithContext(WithPortContext(req.Context(), loopbackAdminPortContext()))
}
