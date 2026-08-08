package rpc

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// roleForRequest mirrors rippled's requestRole (Role.cpp:94-119):
//   - peer ∈ AdminNets + valid configured credentials → RoleAdmin
//   - peer ∈ SecureGatewayNets + non-empty user → RoleIdentified
//   - peer ∈ SecureGatewayNets + empty user      → RoleProxy
//   - else                                       → RoleGuest
//
// peerIP must be the actual TCP peer (from RemoteAddr), never a header-
// supplied IP. user is the X-User header value if present. params is the
// request parameter object containing optional admin_user/admin_password fields.
func roleForRequest(peerIP string, user string, params map[string]any, portCtx *PortContext) types.Role {
	if portCtx == nil {
		return types.RoleGuest
	}
	ip := net.ParseIP(peerIP)
	if ip == nil {
		return types.RoleGuest
	}
	if len(portCtx.AdminNets) > 0 && config.IPInNets(ip, portCtx.AdminNets) && adminCredentialsMatch(params, portCtx) {
		return types.RoleAdmin
	}
	if len(portCtx.SecureGatewayNets) > 0 && config.IPInNets(ip, portCtx.SecureGatewayNets) {
		if strings.TrimSpace(user) != "" {
			return types.RoleIdentified
		}
		return types.RoleProxy
	}
	return types.RoleGuest
}
func adminCredentialsMatch(params map[string]any, portCtx *PortContext) bool {
	if portCtx.AdminUser == "" && portCtx.AdminPassword == "" {
		return true
	}
	user, userOK := params["admin_user"].(string)
	password, passwordOK := params["admin_password"].(string)
	match := constantTimeCredentialsMatch(user, password, portCtx.AdminUser, portCtx.AdminPassword)
	if !userOK || !passwordOK {
		return false
	}
	return match
}
func roleParamsFromRawParams(raw json.RawMessage) map[string]any {
	var entries []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &entries) != nil || len(entries) == 0 {
		return nil
	}
	var params map[string]any
	if json.Unmarshal(entries[0], &params) != nil {
		return nil
	}
	return params
}
func roleParamsFromBatchElement(raw json.RawMessage) map[string]any {
	var request struct {
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return nil
	}
	return roleParamsFromRawParams(request.Params)
}

// resolveClientIP extracts the client IP for logging and identification.
// X-Forwarded-For / X-Real-IP are honoured only when the actual TCP peer
// is in the per-port SecureGatewayNets set (PortContext); otherwise the
// socket peer is returned. This MUST NOT be used for role or admin
// gating — callers that need a security decision should use
// remoteAddrIP, which always returns the socket-level peer.
//
// Per-port scoping matches rippled, which passes a single Port& into
// requestRole and forwardedFor — XFF trust does not bleed across ports
// (ServerHandler.cpp:709-734).
func resolveClientIP(r *http.Request, portCtx *PortContext) string {
	peer := remoteAddrIP(r.RemoteAddr)
	if portCtx == nil || len(portCtx.SecureGatewayNets) == 0 {
		return peer
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil || !config.IPInNets(peerIP, portCtx.SecureGatewayNets) {
		return peer
	}
	if fwd := forwardedForHeader(r); fwd != "" {
		return fwd
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := validatedForwardedIP(xri); ip != "" {
			return ip
		}
	}
	return peer
}

// forwardedForHeader returns the originating client IP carried by the
// RFC 7239 Forwarded header (preferred) or the legacy X-Forwarded-For,
// mirroring rippled's forwardedFor in Role.cpp:261-312. Returns "" when
// neither header is present or parseable.
func forwardedForHeader(r *http.Request) string {
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		if ip := extractForwardedFor(fwd); ip != "" {
			return ip
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := xff
		if before, _, ok := strings.Cut(xff, ","); ok {
			first = before
		}
		return validatedForwardedIP(first)
	}
	return ""
}

// extractForwardedFor returns the IP from the first `for=` token in an
// RFC 7239 Forwarded header value. Case-insensitive token search; the
// value is terminated by `,` or `;` per the RFC.
func extractForwardedFor(value string) string {
	lower := strings.ToLower(value)
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], "for=")
		if idx < 0 {
			return ""
		}
		idx += offset
		if idx == 0 || strings.ContainsRune(",; \t", rune(value[idx-1])) {
			rest := value[idx+len("for="):]
			if i := strings.IndexAny(rest, ",;"); i >= 0 {
				rest = rest[:i]
			}
			return validatedForwardedIP(rest)
		}
		offset = idx + 1
	}
	return ""
}

func validatedForwardedIP(field string) string {
	ip := net.ParseIP(extractIPAddrFromField(field))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// extractIPAddrFromField strips whitespace, surrounding double quotes,
// IPv6 square brackets, and a trailing ":port" from a single Forwarded /
// X-Forwarded-For element. Mirrors rippled's extractIpAddrFromField
// (Role.cpp:156-259).
func extractIPAddrFromField(field string) string {
	s := strings.TrimSpace(field)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if !strings.HasSuffix(s, `"`) || len(s) < 2 {
			return ""
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
		if s == "" {
			return ""
		}
	}
	if strings.HasPrefix(s, "[") {
		// Bracketed form is IPv6 (or IPv4-mapped). Scan until the first
		// character that is not hex / ':' / '.' / space, matching
		// rippled Role.cpp:214-234. If that scan-terminator isn't ']',
		// the bracketed value is malformed → empty result.
		inner := s[1:]
		end := -1
		for i := 0; i < len(inner); i++ {
			c := inner[i]
			if isHexDigit(c) || c == ':' || c == '.' || c == ' ' {
				continue
			}
			end = i
			break
		}
		if end < 0 || inner[end] != ']' {
			return ""
		}
		return strings.TrimSpace(inner[:end])
	}
	// Unbracketed: a colon means either an IPv6 address (multiple colons)
	// or a host:port pair (single colon). Strip port only for the latter.
	if strings.Count(s, ":") == 1 {
		s = s[:strings.IndexByte(s, ':')]
	}
	return s
}

// remoteAddrIP returns the host portion of an http.Request.RemoteAddr
// (or any "host:port" string). Used wherever the IP must be the actual
// TCP peer — never spoofable via headers.
func remoteAddrIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// userHeader returns the X-User header value (matches rippled
// ServerHandler.cpp:582-585). Only consulted by roleForRequest when the
// peer is already in the secure_gateway set, so an untrusted client
// cannot use X-User to upgrade their role.
func userHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-User"))
}

// isHexDigit reports whether c is an ASCII hex digit. Used by
// extractIPAddrFromField's bracket validator (matches rippled
// std::isxdigit in Role.cpp:222).
func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
