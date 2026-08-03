package rpc

import (
	cryptorand "crypto/rand"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/gorilla/websocket"
)

var connectionIDSeq atomic.Uint64

// generateConnectionID returns `conn_<seq>_<random>`. The atomic seq
// avoids collisions under same-nanosecond accept bursts; the random
// suffix keeps IDs unguessable so they can't be used as cross-connection
// references.
func generateConnectionID() string {
	seq := connectionIDSeq.Add(1)
	var rnd [6]byte
	if _, err := cryptorand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("conn_%d_%x", seq, time.Now().UnixNano())
	}
	return fmt.Sprintf("conn_%d_%x", seq, rnd)
}
func getWebSocketClientIP(conn *websocket.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// resolveWSClientIP returns the attributed client IP for a WebSocket
// dispatch. If the peer is in this port's SecureGatewayNets allowlist
// and the upgrade captured a Forwarded / X-Forwarded-For / X-Real-IP
// value, that value is returned; otherwise the socket peer is returned.
// Role decisions never consult this — see roleForRequest.
func resolveWSClientIP(peerIP, upgradeForwardedFor string, portCtx *PortContext) string {
	if upgradeForwardedFor == "" || portCtx == nil || len(portCtx.SecureGatewayNets) == 0 {
		return peerIP
	}
	parsed := net.ParseIP(peerIP)
	if parsed == nil || !config.IPInNets(parsed, portCtx.SecureGatewayNets) {
		return peerIP
	}
	return upgradeForwardedFor
}
