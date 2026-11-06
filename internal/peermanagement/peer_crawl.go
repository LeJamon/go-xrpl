package peermanagement

import (
	"context"
	"net"
)

func (o *Overlay) checkPeerListeningPort(peer *Peer, address string) bool {
	endpoint, err := ParseEndpoint(address)
	if err != nil || endpoint.Port == 0 {
		return false
	}
	o.lifecycleMu.Lock()
	ctx := o.ctx
	o.lifecycleMu.Unlock()
	peer.mu.Lock()
	if peer.portCheckStarted {
		canAccept := peer.listeningPort != 0
		peer.mu.Unlock()
		return canAccept
	}
	if ctx == nil {
		peer.mu.Unlock()
		return false
	}
	peer.portCheckStarted = true
	peer.mu.Unlock()
	done, ok := o.beginPeerStart()
	if !ok {
		return false
	}
	go func() {
		defer done()
		probeCtx, cancel := context.WithTimeout(ctx, o.cfg.ConnectTimeout)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", endpoint.String())
		if err != nil {
			return
		}
		_ = conn.Close()
		if current, ok := o.getPeer(peer.id); !ok || current != peer {
			return
		}
		peer.mu.Lock()
		peer.listeningPort = endpoint.Port
		peer.mu.Unlock()
	}()
	return false
}
