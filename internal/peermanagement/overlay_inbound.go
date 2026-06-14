package peermanagement

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
)

// inboundBacklogSlack caps the accept-side goroutine count to
// MaxInbound + slack so a burst of accepts cannot fan out unbounded;
// canAcceptInbound is the authoritative slot gate.
const inboundBacklogSlack = 8

// acceptBackoff throttles the retry rate when listener.Accept returns
// a non-fatal error (typically EMFILE-class) so the loop does not
// spin at CPU speed under FD pressure.
const acceptBackoff = 100 * time.Millisecond

// chargeInboundHandshake charges the inbound endpoint's resource Consumer
// for a malformed or abusive handshake. During the handshake the peer is
// not yet in o.peers (addPeer runs only after a successful handshake), so
// routing the charge through IncPeerBadData / the peer map would silently
// no-op. We charge the endpoint Consumer directly by address, mirroring
// rippled which charges the inbound endpoint's Resource::Consumer for
// handshake abuse. The Consumer's balance persists in the manager keyed by
// address, so a host spamming malformed handshakes accrues balance across
// attempts and is eventually throttled at admission.
func (o *Overlay) chargeInboundHandshake(addr, reason string) {
	if o.resourceManager == nil {
		return
	}
	c := o.resourceManager.NewInboundEndpoint(addr)
	c.Charge(chargeForReason(reason), reason)
	c.Release()
}

// startListener creates and starts the TCP/TLS listener.
func (o *Overlay) startListener() error {
	tcpListener, err := net.Listen("tcp", o.cfg.ListenAddr)
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
	if err != nil {
		tcpListener.Close()
		return fmt.Errorf("overlay: build TLS cert: %w", err)
	}

	l := peertls.NewListener(tcpListener, &peertls.Config{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	})
	o.listenerMu.Lock()
	o.listener = l
	o.listenerMu.Unlock()
	return nil
}

// acceptLoop accepts incoming connections. acceptBackoff throttles
// retries under EMFILE-class errors; inboundSem caps the handler
// goroutine fan-out.
func (o *Overlay) acceptLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := o.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A closed listener is terminal — exit instead of
			// spinning the backoff. Also the !cgo peertls stub path,
			// which closes the inner listener at NewListener.
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(acceptBackoff):
			}
			continue
		}

		select {
		case o.inboundSem <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return ctx.Err()
		}

		o.peerWG.Add(1)
		go func(c net.Conn) {
			defer o.peerWG.Done()
			defer func() { <-o.inboundSem }()
			o.handleInbound(ctx, c)
		}(conn)
	}
}

func (o *Overlay) handleInbound(ctx context.Context, conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in inbound handler", "t", "Overlay", "panic", r)
			conn.Close()
		}
	}()

	// The inbound slot limit is enforced after the handshake (see
	// hasInboundSlot below) because reserved/cluster peers are admitted beyond
	// the cap and their node key is unknown until the handshake completes.
	// Concurrent handshakes stay bounded by inboundSem regardless.
	remoteAddr := conn.RemoteAddr().String()
	endpoint, _ := ParseEndpoint(remoteAddr)

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, true, o.identity, o.events)
	peer.SetDroppedEventsCounter(&o.droppedEvents)
	if err := peer.AcceptConnection(conn); err != nil {
		slog.Warn("Inbound rejected: peer not in disconnected state",
			"t", "Overlay", "remote", remoteAddr, "err", err)
		conn.Close()
		return
	}

	tlsConn, ok := conn.(peertls.PeerConn)
	if !ok {
		slog.Error("Inbound connection is not peertls", "t", "Overlay", "remote", remoteAddr)
		conn.Close()
		return
	}

	if err := o.performInboundHandshake(ctx, peer, tlsConn); err != nil {
		slog.Info("Inbound handshake failed", "t", "Overlay", "remote", remoteAddr, "err", err)
		conn.Close()
		o.dispatchLifecycle(Event{
			Type:     EventPeerFailed,
			PeerID:   peerID,
			Endpoint: endpoint,
			Inbound:  true,
			Error:    err,
		})
		return
	}

	if o.isConnectedTo(endpoint) {
		conn.Close()
		return
	}

	if !o.hasInboundSlot(peer) {
		slog.Info("Inbound rejected: no slots", "t", "Overlay", "remote", remoteAddr)
		conn.Close()
		return
	}

	peer.setState(PeerStateConnected)
	slog.Info("Inbound peer connected", "t", "Overlay", "remote", remoteAddr)

	o.addPeer(peer)

	o.peerWG.Add(1)
	go func() {
		defer o.peerWG.Done()
		err := peer.Run(ctx)
		if err != nil {
			slog.Info("Inbound peer run ended", "t", "Overlay", "remote", remoteAddr, "err", err)
			o.notePeerRunEnded(err)
		}
		o.removePeer(peerID)
	}()
}

func (o *Overlay) performInboundHandshake(ctx context.Context, peer *Peer, tlsConn peertls.PeerConn) error {
	// The peer is not in o.peers during the handshake, so bad-data
	// charges route through the endpoint Consumer keyed by this address.
	addr := peer.Endpoint().String()

	// Accept() does not drive the handshake; complete it before reading
	// the Finished bytes for SharedValue.
	handshakeCtx, cancel := context.WithTimeout(ctx, o.cfg.HandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return NewHandshakeError(peer.Endpoint(), "tls", err)
	}

	sharedValue, err := tlsConn.SharedValue()
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "shared_value", err)
	}

	deadline := time.Now().Add(o.cfg.HandshakeTimeout)
	tlsConn.SetDeadline(deadline)
	defer tlsConn.SetDeadline(time.Time{})

	bufReader := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(bufReader)
	if err != nil {
		return NewHandshakeError(peer.Endpoint(), "read_request", err)
	}
	req.Body.Close()

	// Server-Domain runs first in the verify chain.
	if _, err := ValidateServerDomain(req.Header); err != nil {
		o.chargeInboundHandshake(addr, "handshake-malformed-extras")
		return NewHandshakeError(peer.Endpoint(), "verify_extras", err)
	}

	// Build the handshake config once and share it with both
	// VerifyPeerHandshake and BuildHandshakeResponse so the inbound
	// and outbound paths cannot diverge.
	hsCfg := o.handshakeConfigFor()

	// Full session-signature verification — the whole point of #269.
	peerPubKey, verifyErr := VerifyPeerHandshake(
		req.Header,
		sharedValue,
		o.identity.EncodedPublicKey(),
		hsCfg,
	)
	if verifyErr != nil {
		if !errors.Is(verifyErr, ErrSelfConnection) && !errors.Is(verifyErr, ErrNetworkMismatch) {
			o.chargeInboundHandshake(addr, "handshake-verify")
		}
		return NewHandshakeError(peer.Endpoint(), "verify", verifyErr)
	}
	peer.mu.Lock()
	peer.remotePubKey = peerPubKey
	peer.mu.Unlock()

	peerRemote := tcpRemoteIP(tlsConn)
	extras, extraErr := ParseHandshakeExtras(
		req.Header,
		o.cfg.PublicIP,
		peerRemote,
	)
	if extraErr != nil {
		o.chargeInboundHandshake(addr, "handshake-malformed-extras")
		return NewHandshakeError(peer.Endpoint(), "verify_extras", extraErr)
	}
	peer.applyHandshakeExtras(extras)

	caps := NewPeerCapabilities()
	caps.Features = ParseProtocolCtlFeatures(req.Header)
	protocol := NegotiateProtocolVersion(req.Header.Get(HeaderUpgrade))
	if protocol == "" {
		o.chargeInboundHandshake(addr, "handshake-protocol-negotiation")
		// Write a 400 Bad Request back so a misconfigured peer sees
		// the rejection reason instead of a TCP RST. Best-effort: a
		// write error here is shadowed by the negotiation failure we
		// are already returning.
		var remoteAddr string
		if peerRemote != nil {
			remoteAddr = peerRemote.String()
		}
		errResp := BuildHandshakeErrorResponse(
			hsCfg.UserAgent,
			remoteAddr,
			"Unable to agree on a protocol version",
		)
		_ = errResp.Write(tlsConn)
		return NewHandshakeError(peer.Endpoint(), "verify",
			fmt.Errorf("%w: unable to agree on a protocol version (peer offered %q)",
				ErrInvalidHandshake, req.Header.Get(HeaderUpgrade)))
	}

	peer.mu.Lock()
	peer.bufReader = bufReader
	peer.capabilities = caps
	peer.protocolVersion = protocol
	peer.mu.Unlock()

	resp := BuildHandshakeResponse(o.identity, sharedValue, hsCfg, protocol)
	addAddressHeaders(resp.Header, hsCfg, peerRemote)
	if err := resp.Write(tlsConn); err != nil {
		return NewHandshakeError(peer.Endpoint(), "send_response", err)
	}

	return nil
}

// handshakeConfigFor builds the per-handshake config used by both
// inbound and outbound paths so they cannot drift.
func (o *Overlay) handshakeConfigFor() HandshakeConfig {
	return HandshakeConfig{
		UserAgent:           o.cfg.UserAgent,
		NetworkID:           o.cfg.NetworkID,
		CrawlPublic:         false,
		EnableLedgerReplay:  o.cfg.EnableLedgerReplay,
		EnableCompression:   o.cfg.EnableCompression,
		EnableVPReduceRelay: o.cfg.EnableVPReduceRelay,
		EnableTxReduceRelay: o.cfg.EnableTxReduceRelay,
		InstanceCookie:      o.instanceCookie,
		ServerDomain:        o.cfg.ServerDomain,
		PublicIP:            o.cfg.PublicIP,
		LedgerHintProvider:  o.ledgerHintProviderSnapshot(),
	}
}
