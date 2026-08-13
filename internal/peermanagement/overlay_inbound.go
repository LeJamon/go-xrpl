package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

// inboundBacklogSlack caps the accept-side goroutine count to
// MaxInbound + slack so a burst of accepts cannot fan out unbounded.
const inboundBacklogSlack = 8

// acceptBackoff throttles the retry rate when listener.Accept returns
// a non-fatal error (typically EMFILE-class) so the loop does not
// spin at CPU speed under FD pressure.
const acceptBackoff = 100 * time.Millisecond

const (
	maxHandshakeHeaderBytes = 8 * 1024
	maxHandshakeBodyBytes   = 1 << 20
)

// handshakeHeaderReader bounds the bytes consumed by http.ReadRequest until
// the end-of-headers marker. Once the marker is seen it becomes transparent,
// preserving any already-buffered peer frames for the Peer read loop.
type handshakeHeaderReader struct {
	r       io.Reader
	used    int64
	limit   int64
	tail    [3]byte
	tailLen int
	done    bool
}

func (r *handshakeHeaderReader) Read(p []byte) (int, error) {
	if r.done {
		return r.r.Read(p)
	}
	if r.used >= r.limit {
		return 0, ErrHandshakeHeadersTooLarge
	}
	remaining := r.limit - r.used
	if int64(len(p)) > remaining {
		p = p[:int(remaining)]
	}
	n, err := r.r.Read(p)
	if n == 0 {
		return n, err
	}
	r.used += int64(n)
	var marker bytes.Buffer
	marker.Grow(r.tailLen + n)
	marker.Write(r.tail[:r.tailLen])
	marker.Write(p[:n])
	// net/http accepts both conventional CRLF and the LF-only form. Once
	// either header terminator is parsed, stop enforcing the handshake limit so
	// binary peer frames cannot be mistaken for oversized headers.
	if bytes.Contains(marker.Bytes(), []byte("\r\n\r\n")) || bytes.Contains(marker.Bytes(), []byte("\n\n")) {
		r.done = true
		return n, err
	}
	if marker.Len() > len(r.tail) {
		copy(r.tail[:], marker.Bytes()[marker.Len()-len(r.tail):])
		r.tailLen = len(r.tail)
	} else {
		copy(r.tail[:], marker.Bytes())
		r.tailLen = marker.Len()
	}
	return n, err
}

func readHandshakeBody(req *http.Request) error {
	if req == nil {
		return nil
	}
	// A declared length is available before reading the body. Reject it now so
	// http.body.Close cannot drain an oversized fixed-length payload.
	if req.ContentLength > maxHandshakeBodyBytes {
		return fmt.Errorf("%w: declared length %d", ErrHandshakeBodyTooLarge, req.ContentLength)
	}
	if req.Body == nil {
		if req.ContentLength > 0 || len(req.TransferEncoding) > 0 {
			return fmt.Errorf("%w: body is missing", ErrInvalidHandshake)
		}
		return nil
	}
	readN, readErr := io.Copy(io.Discard, io.LimitReader(req.Body, maxHandshakeBodyBytes+1))
	if readErr != nil {
		return fmt.Errorf("%w: read request body: %w", ErrInvalidHandshake, readErr)
	}
	if readN > maxHandshakeBodyBytes {
		// The connection is closed by the caller after a failed handshake. Do not
		// call http.body.Close here: for fixed and chunked bodies it may drain an
		// arbitrary amount of unread input before returning.
		return fmt.Errorf("%w: got at least %d bytes", ErrHandshakeBodyTooLarge, readN)
	}
	if req.ContentLength >= 0 && readN != req.ContentLength {
		return fmt.Errorf("%w: body length %d, want %d", ErrInvalidHandshake, readN, req.ContentLength)
	}
	// At this point the body was read to EOF, so Close cannot perform an
	// unbounded drain. Preserve close errors for callers that provide them.
	closeErr := req.Body.Close()
	if closeErr != nil {
		return fmt.Errorf("%w: close request body: %w", ErrInvalidHandshake, closeErr)
	}
	return nil
}

// admitInboundEndpoint reports whether an inbound connection from addr
// may proceed. It refuses an endpoint whose resource Consumer is already
// at the drop threshold — balance accrued from prior bad-data charges on
// the same host, which persists in the manager keyed by address — before
// spending a handshake on it. A failed handshake itself is never charged:
// rippled gates inbound admission the same way, checking the endpoint
// Consumer for disconnect at accept and refusing the connection only when
// it is already over budget. Always admitted when no resource manager is
// wired.
func (o *Overlay) admitInboundEndpoint(addr string) bool {
	consumer, admitted := o.acquireInboundUsage(addr)
	if consumer != nil {
		consumer.Release()
	}
	return admitted
}

func (o *Overlay) acquireInboundUsage(addr string) (*resource.Consumer, bool) {
	if o.resourceManager == nil {
		return nil, true
	}
	c := o.resourceManager.NewInboundEndpoint(addr)
	if c == nil {
		return nil, false
	}
	if c.Disconnect() {
		c.Release()
		return nil, false
	}
	return c, true
}

// startListener creates the TCP/TLS listener without publishing it. Run owns
// publication under lifecycleMu so Stop cannot miss a socket that is still
// being prepared.
func (o *Overlay) startListener(ctx context.Context) (net.Listener, error) {
	var lc net.ListenConfig
	listen := lc.Listen
	if o.listenFunc != nil {
		listen = o.listenFunc
	}
	tcpListener, err := listen(ctx, "tcp", o.cfg.ListenAddr)
	if err != nil {
		return nil, err
	}

	certPEM, keyPEM, err := o.identity.TLSCertificatePEM()
	if err != nil {
		tcpListener.Close()
		return nil, fmt.Errorf("overlay: build TLS cert: %w", err)
	}

	tlsListener, err := peertls.NewListener(tcpListener, &peertls.Config{
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		CipherList: o.cfg.SSLCiphers,
	})
	if err != nil {
		_ = tcpListener.Close()
		return nil, fmt.Errorf("overlay: build TLS listener: %w", err)
	}
	return tlsListener, nil
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
			// A closed listener is terminal — exit instead of spinning the
			// backoff.
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

		startDone, ok := o.beginPeerStart()
		if !ok {
			<-o.inboundSem
			conn.Close()
			return ErrConnectionClosed
		}
		go func(c net.Conn, startDone func()) {
			defer startDone()
			defer func() { <-o.inboundSem }()
			o.handleInbound(ctx, c)
		}(conn, startDone)
	}
}

func (o *Overlay) handleInbound(ctx context.Context, conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in inbound handler", "t", "Overlay", "panic", r)
			conn.Close()
		}
	}()

	// The inbound slot limit is enforced after the handshake because
	// reserved/cluster peers are admitted beyond the cap and their node key is
	// unknown until the handshake completes.
	// Concurrent handshakes stay bounded by inboundSem regardless.
	remoteAddr := conn.RemoteAddr().String()
	endpoint, _ := ParseEndpoint(remoteAddr)

	inboundUsage, admitted := o.acquireInboundUsage(endpoint.String())
	if !admitted {
		slog.Info("Inbound rejected: endpoint over resource drop threshold",
			"t", "Overlay", "remote", remoteAddr)
		conn.Close()
		return
	}
	defer func() {
		if inboundUsage != nil {
			inboundUsage.Release()
		}
	}()

	peerID := PeerID(o.nextID.Add(1))
	peer := NewPeer(peerID, endpoint, true, o.identity, o.events)
	peer.SetDroppedEventsCounter(&o.droppedEvents)
	peer.SetConsensusEvents(o.consensusEvents)
	peer.SetConsensusControlEvents(o.consensusControlEvents)
	peer.SetAcquisitionEvents(o.acquisitionEvents)
	peer.SetManifestMessages(o.manifestMessages)
	peer.SetInboundReadBudget(o.inboundReadBudget)
	peer.SetManifestSpoolDir(o.manifestSpoolDir)
	peer.SetManifestPayloadLimit(o.cfg.MaxManifestPayload)
	if !o.reserveInboundIP(endpoint.Host) {
		slog.Info("Inbound rejected: IP connection limit reached",
			"t", "Overlay", "remote", remoteAddr)
		conn.Close()
		return
	}
	added := false
	defer func() {
		if !added {
			o.releasePeerKey(peer)
			o.releaseInboundIP(endpoint.Host)
		}
	}()
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
		conn.Close()
		// Admission rejections (non-peer Connect-As, slot-full, duplicate)
		// already emitted their response in lieu of the 101 upgrade and are
		// not handshake failures, so they raise no lifecycle event.
		if errors.Is(err, errInboundRejected) {
			return
		}
		slog.Info("Inbound handshake failed", "t", "Overlay", "remote", remoteAddr, "err", err)
		o.dispatchLifecycle(Event{
			Type:     EventPeerFailed,
			PeerID:   peerID,
			Endpoint: endpoint,
			Inbound:  true,
			Error:    err,
		})
		return
	}

	peer.setState(PeerStateConnected)
	slog.Info("Inbound peer connected", "t", "Overlay", "remote", remoteAddr)

	if err := o.addPeerWithUsage(peer, inboundUsage); err != nil {
		slog.Info("Inbound rejected after handshake", "t", "Overlay", "remote", remoteAddr, "err", err)
		conn.Close()
		return
	}
	inboundUsage = nil
	added = true

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
	reservedKey := false
	defer func() {
		if reservedKey {
			o.releasePeerKey(peer)
		}
	}()

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
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := tlsConn.SetDeadline(deadline); err != nil {
		return NewHandshakeError(peer.Endpoint(), "set_deadline", err)
	}
	defer func() {
		if err := tlsConn.SetDeadline(time.Time{}); err != nil {
			slog.Debug("Inbound handshake deadline reset failed", "t", "Overlay", "err", err)
		}
	}()

	bufReader := bufio.NewReader(&handshakeHeaderReader{
		r:     tlsConn,
		limit: maxHandshakeHeaderBytes,
	})
	hsCfg := o.handshakeConfigFor()
	req, err := http.ReadRequest(bufReader)
	if err != nil {
		writeInboundHandshakeError(tlsConn, hsCfg, tcpRemoteIP(tlsConn), err)
		return NewHandshakeError(peer.Endpoint(), "read_request", err)
	}
	if err := readHandshakeBody(req); err != nil {
		writeInboundHandshakeError(tlsConn, hsCfg, tcpRemoteIP(tlsConn), err)
		return NewHandshakeError(peer.Endpoint(), "read_body", err)
	}
	if err := validateHandshakeRequest(req); err != nil {
		writeInboundHandshakeError(tlsConn, hsCfg, tcpRemoteIP(tlsConn), err)
		return NewHandshakeError(peer.Endpoint(), "validate_envelope", err)
	}

	// A dialer that does not advertise the "peer" role (a crawler or a
	// misdirected client) is handed alternates and closed rather than
	// upgraded. rippled gates this in onHandoff before the rest of the
	// handshake.
	if !connectAsIncludesPeer(req.Header.Get(HeaderConnectAs)) {
		slog.Info("Inbound rejected: non-peer Connect-As",
			"t", "Overlay", "remote", peer.Endpoint().Host,
			"connect-as", req.Header.Get(HeaderConnectAs))
		o.writeInboundRedirect(tlsConn)
		return errInboundRejected
	}
	protocol := NegotiateProtocolVersion(req.Header.Get(HeaderUpgrade))
	if protocol == "" {
		err := fmt.Errorf("%w: unable to agree on a protocol version (peer offered %q)",
			ErrInvalidHandshake, req.Header.Get(HeaderUpgrade))
		writeInboundHandshakeError(tlsConn, hsCfg, tcpRemoteIP(tlsConn), err)
		return NewHandshakeError(peer.Endpoint(), "negotiate", err)
	}

	// Server-Domain runs first in the verify chain.
	if _, err := ValidateServerDomain(req.Header); err != nil {
		writeInboundHandshakeError(tlsConn, hsCfg, tcpRemoteIP(tlsConn), err)
		return NewHandshakeError(peer.Endpoint(), "verify_extras", err)
	}

	// Full session-signature verification — the whole point of #269.
	peerPubKey, verifyErr := VerifyPeerHandshake(
		req.Header,
		sharedValue,
		o.identity.EncodedPublicKey(),
		hsCfg,
	)
	if verifyErr != nil {
		writeInboundHandshakeError(tlsConn, hsCfg, tcpRemoteIP(tlsConn), verifyErr)
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
		writeInboundHandshakeError(tlsConn, hsCfg, tcpRemoteIP(tlsConn), extraErr)
		return NewHandshakeError(peer.Endpoint(), "verify_extras", extraErr)
	}
	peer.applyHandshakeExtras(extras)

	// Decide admission before sending the 101 upgrade, mirroring rippled's
	// onHandoff: a duplicate or slot-full dialer gets a rejection in lieu
	// of the upgrade, never both on the same stream. The slot check needs
	// the verified public key (set above) so reserved/cluster peers still
	// bypass the inbound cap.
	if o.isConnectedTo(postHandshakeEndpoint(peer, peer.Endpoint())) {
		slog.Info("Inbound rejected: already connected",
			"t", "Overlay", "remote", peer.Endpoint().Host)
		o.writeInboundRedirect(tlsConn)
		return errInboundRejected
	}
	bypassInboundLimit := o.isClusterPeer(peer) || o.isReservedPeer(peer)
	if err := o.reservePeerKey(peer, bypassInboundLimit); err != nil {
		if errors.Is(err, ErrMaxPeersReached) {
			slog.Info("Inbound rejected: no slots",
				"t", "Overlay", "remote", peer.Endpoint().Host)
		} else {
			slog.Info("Inbound rejected: duplicate public key",
				"t", "Overlay", "remote", peer.Endpoint().Host)
		}
		o.writeInboundRedirect(tlsConn)
		return errInboundRejected
	}
	reservedKey = true

	resp := BuildHandshakeResponse(req, o.identity, sharedValue, hsCfg, protocol) //nolint:bodyclose // locally-built response serialized via Write; nothing to close
	setInboundHandshakeState(peer, bufReader, protocol, hsCfg, resp.Header)
	addAddressHeaders(resp.Header, hsCfg, peerRemote)
	if err := resp.Write(tlsConn); err != nil {
		return NewHandshakeError(peer.Endpoint(), "send_response", err)
	}

	reservedKey = false
	return nil
}

func writeInboundHandshakeError(tlsConn peertls.PeerConn, cfg HandshakeConfig, remoteIP net.IP, err error) {
	remoteAddr := ""
	if remoteIP != nil {
		remoteAddr = remoteIP.String()
	}
	resp := BuildHandshakeErrorResponse(cfg.UserAgent, remoteAddr, err.Error()) //nolint:bodyclose // locally-built response serialized via Write
	_ = resp.Write(tlsConn)
}

func setInboundHandshakeState(
	peer *Peer,
	reader *bufio.Reader,
	protocol string,
	cfg HandshakeConfig,
	negotiatedHeaders http.Header,
) {
	caps := NewPeerCapabilities()
	caps.Features = ParseProtocolCtlFeatures(negotiatedHeaders)

	peer.mu.Lock()
	peer.bufReader = reader
	peer.capabilities = caps
	peer.protocolVersion = protocol
	peer.handshakeCfg = cfg
	peer.mu.Unlock()
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
