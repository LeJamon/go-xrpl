package peermanagement

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

// Sentinel errors returned by LedgerProvider methods so handlers can map
// them to protocol responses and resource charges.
//
// Mirrors rippled's reNO_LEDGER (ledger unknown / not yet immutable) and
// reNO_NODE (the requested key is not present in the selected map) at
// rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:62-90.
var (
	// ErrLedgerNotFound signals the requested ledger is unknown to the
	// provider or not yet immutable. The handler charges and drops the request.
	ErrLedgerNotFound = errors.New("ledger not found")
	// ErrKeyNotFound signals the ledger exists but the requested key has
	// no leaf in the selected map. The handler charges and drops the request.
	ErrKeyNotFound = errors.New("key not found in ledger map")
	// ErrFetchPackTooEarly signals that the requested HAVE ledger is below
	// the configured peer-serving range.
	ErrFetchPackTooEarly = errors.New("fetch pack ledger is too early")
	// ErrFetchPackOpen signals that the requested HAVE ledger is known but
	// still open. Rippled treats this malformed fetch-pack request differently
	// from an unknown ledger when charging the requester.
	ErrFetchPackOpen = errors.New("fetch pack ledger is open")
	// ErrFetchPackBusy signals that local ledger state is temporarily busy or
	// stale for fetch-pack construction. Busy requests are dropped before the
	// heavy-burden charge and are retried by the requester later.
	ErrFetchPackBusy = errors.New("fetch pack provider busy")
	// ErrPeerBadRequest is returned by LedgerSyncHandler.HandleMessage
	// when the inbound request was malformed (e.g. bad field lengths,
	// invalid enum values). The handler charges and drops the request; the
	// overlay dispatcher uses this error to attribute the
	// failure to the originating peer, mirroring rippled's malformed-request
	// charge path.
	ErrPeerBadRequest = errors.New("peer sent bad request")
)

// ContextLedgerProvider is implemented by providers that can stop storage
// traversal when the serving request is canceled. The legacy methods remain
// part of LedgerProvider so embedders and tests can migrate independently.
type ContextLedgerProvider interface {
	GetReplayDeltaContext(context.Context, []byte) (header []byte, txLeaves [][]byte, err error)
	GetProofPathContext(context.Context, []byte, []byte, message.LedgerMapType) (header []byte, path [][]byte, err error)
}

// ContextFetchPackProvider is the cancellation-aware fetch-pack extension of
// LedgerProvider. Providers that do not implement it are still bounded by the
// scheduler and receive a pre-call cancellation check.
type ContextFetchPackProvider interface {
	MakeFetchPackContext(context.Context, [32]byte, int) ([]message.IndexedObject, error)
}

// LedgerProvider is called to retrieve ledger data for responses.
type LedgerProvider interface {
	// GetReplayDelta returns the serialized ledger header and every
	// transaction leaf blob (in tx-map order) for the given ledger hash.
	// Implementations must only return data for closed/immutable ledgers
	// (mirrors rippled's ledger->isImmutable() check in
	// LedgerReplayMsgHandler::processReplayDeltaRequest). When the ledger
	// is unknown or not yet immutable, return (nil, nil, nil) so the
	// handler can charge and drop the request.
	GetReplayDelta(ledgerHash []byte) (header []byte, txLeaves [][]byte, err error)
	// GetProofPath returns the serialized ledger header and the wire-order
	// node path proving the existence of `key` in the requested map of
	// the given ledger. mapType selects the source map:
	//   - LedgerMapTransaction (1)  → tx map
	//   - LedgerMapAccountState (2) → account-state map
	//
	// Wire path orientation is leaf-to-root, matching both
	// shamap.GetProofPath and rippled's SHAMap::getProofPath
	// (rippled/src/xrpld/shamap/detail/SHAMapSync.cpp:800-833) — that
	// implementation pops a stack whose top is the leaf, yielding
	// leaf-first blobs which are then verified by reverse iteration in
	// SHAMap::verifyProofPath (same file, line 847).
	//
	// Return contract:
	//   - (nil, nil, ErrLedgerNotFound) when the ledger is unknown or not
	//     yet immutable. The handler charges and drops the request.
	//   - (nil, nil, ErrKeyNotFound) when the ledger exists but the key
	//     has no leaf in the selected map. The handler charges and drops
	//     the request. Rippled returns reNO_NODE without a header in this
	//     case (LedgerReplayMsgHandler.cpp:84-90, where header packing
	//     happens AFTER the no-path early-return), so we retain the
	//     provider distinction for accounting without serializing an error.
	//   - (header, path, nil) on success.
	//   - any other error → handler charges the no-reply fee, logs at warn,
	//     and drops the request.
	GetProofPath(ledgerHash []byte, key []byte, mapType message.LedgerMapType) (header []byte, path [][]byte, err error)
	// MakeFetchPack builds a fetch-pack for the predecessor of the ledger
	// named by haveLedgerHash, mirroring rippled's LedgerMaster::makeFetchPack
	// (LedgerMaster.cpp:2096-2225): the requester supplies a ledger hash it
	// HAS, and the provider returns the SHAMap nodes of its parent ("want") —
	// a header object followed by the account-state and transaction tree
	// nodes, each tagged with want's sequence. Returns ErrFetchPackTooEarly
	// when HAVE is below the configured range. A provider that is locally busy
	// or stale returns ErrFetchPackBusy before charging; unknown or parentless
	// ledgers return (nil, nil), while an open HAVE returns ErrFetchPackOpen so
	// the handler can apply rippled's malformed-request charge. maxObjects <= 0
	// lets the provider apply its own cap.
	MakeFetchPack(haveLedgerHash [32]byte, maxObjects int) ([]message.IndexedObject, error)
}

// LedgerSyncHandler handles ledger synchronization messages.
type LedgerSyncHandler struct {
	mu sync.RWMutex

	// Data provider for responding to requests
	provider LedgerProvider

	// Event channel for sending responses
	events chan<- Event

	// prioritySender delivers completed responses directly to the peer's
	// bounded acquisition lane. It avoids routing replies through the lossy
	// shared event channel when the overlay is running.
	prioritySender func(context.Context, PeerID, []byte) error

	encodeFrame func(message.Message) ([]byte, error)

	// droppedResponses counts how many response events we had to drop
	// because the events channel was full (slow consumer). Exposed via
	// DroppedResponses so the overlay can aggregate into server_info.
	droppedResponses atomic.Uint64

	// chargePeer is wired by the Overlay so request accounting stays with the
	// peer that originated the work. It is optional for standalone handlers.
	chargePeer func(PeerID, resource.Charge, string)

	// peerHintLookup is wired by the Overlay (see SetPeerLedgerHintLookup).
	peerHintLookup func(target [32]byte) []PeerID
}

// DroppedResponses returns the cumulative count of ledger-sync
// responses dropped due to back-pressure on the events channel.
// Surfaced by the overlay's DroppedLedgerResponses.
func (h *LedgerSyncHandler) DroppedResponses() uint64 {
	return h.droppedResponses.Load()
}

// NewLedgerSyncHandler creates a new ledger sync handler.
func NewLedgerSyncHandler(events chan<- Event) *LedgerSyncHandler {
	return &LedgerSyncHandler{
		events:      events,
		encodeFrame: message.EncodeFrame,
	}
}

// SetProvider sets the ledger data provider for responding to requests.
func (h *LedgerSyncHandler) SetProvider(provider LedgerProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.provider = provider
}

func (h *LedgerSyncHandler) SetChargePeer(fn func(PeerID, resource.Charge, string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.chargePeer = fn
}

func (h *LedgerSyncHandler) SetPrioritySender(fn func(context.Context, PeerID, []byte) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prioritySender = fn
}

func (h *LedgerSyncHandler) charge(peerID PeerID, fee resource.Charge, reason string) {
	h.mu.RLock()
	fn := h.chargePeer
	h.mu.RUnlock()
	if fn != nil {
		fn(peerID, fee, reason)
	}
}

// MakeFetchPack delegates to the configured provider so the overlay's
// otFETCH_PACK serve path can build a pack without importing the ledger
// layer. Returns (nil, nil) when no provider is wired (the handler then drops
// the request), matching the silent-drop stance of the other serve paths.
func (h *LedgerSyncHandler) MakeFetchPack(haveLedgerHash [32]byte, maxObjects int) ([]message.IndexedObject, error) {
	h.mu.RLock()
	provider := h.provider
	h.mu.RUnlock()
	if provider == nil {
		return nil, nil
	}
	return provider.MakeFetchPack(haveLedgerHash, maxObjects)
}

func (h *LedgerSyncHandler) MakeFetchPackContext(ctx context.Context, haveLedgerHash [32]byte, maxObjects int) ([]message.IndexedObject, error) {
	h.mu.RLock()
	provider := h.provider
	h.mu.RUnlock()
	if provider == nil {
		return nil, nil
	}
	if contextProvider, ok := provider.(ContextFetchPackProvider); ok {
		return contextProvider.MakeFetchPackContext(ctx, haveLedgerHash, maxObjects)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return provider.MakeFetchPack(haveLedgerHash, maxObjects)
}

// PreferredPeersForLedger returns peer IDs whose last-known
// Closed-Ledger hash matches target. Empty when no lookup is wired or
// no peer matches. Filters by a single advertised hash only — does not
// replicate rippled's PeerImp::hasLedger(hash, seq) range/recent-set
// logic used by InboundLedger catchup.
func (h *LedgerSyncHandler) PreferredPeersForLedger(target [32]byte) []PeerID {
	h.mu.RLock()
	lookup := h.peerHintLookup
	h.mu.RUnlock()
	if lookup == nil {
		return nil
	}
	return lookup(target)
}

func (h *LedgerSyncHandler) SetPeerLedgerHintLookup(fn func(target [32]byte) []PeerID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.peerHintLookup = fn
}

// HandleMessage handles a ledger sync request the overlay dispatches to this
// handler: mtPROOF_PATH_REQ and mtREPLAY_DELTA_REQ.
//
// mtGET_LEDGER serving and the mtLEDGER_DATA / mtREPLAY_DELTA_RESPONSE /
// mtPROOF_PATH_RESPONSE replies have no arm here: serving and orchestration
// (state machine, hash verification, adoption) live in the consensus router,
// which receives those frames via the overlay's Messages() channel. Handling
// them here too would create competing consumers and a race on the
// inbound-acquisition state.
func (h *LedgerSyncHandler) HandleMessage(ctx context.Context, peerID PeerID, msg message.Message) error {
	switch m := msg.(type) {
	case *message.ProofPathRequest:
		return h.handleProofPathRequest(ctx, peerID, m)
	case *message.ReplayDeltaRequest:
		return h.handleReplayDeltaRequest(ctx, peerID, m)
	}
	return nil
}

// handleProofPathRequest serves an inbound mtPROOF_PATH_REQ.
//
// Mirrors rippled's LedgerReplayMsgHandler::processProofPathRequest
// (rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:40-104):
//  1. Validate len(key) == 32, len(ledgerHash) == 32, type ∈ {1, 2}.
//     Any failure is charged and dropped by PeerImp; no error response is
//     sent. The decoded request fields are not echoed on this path.
//  2. Look up the ledger by hash. Missing → reBAD_REQUEST is wrong; the
//     spec says reNO_LEDGER. Provider returns ErrLedgerNotFound here.
//  3. Walk the selected map (tx or account-state) toward the key. If the
//     key has no leaf → reNO_NODE (provider returns ErrKeyNotFound).
//     Note: rippled does NOT pack the ledger header on this path — the
//     header packing at LedgerReplayMsgHandler.cpp:92-95 runs only after
//     the no-node early-return, so the reply contains key/ledgerHash/
//     type and the error code only.
//  4. On success, emit (header, path) with leaf-to-root path order
//     matching rippled's wire format (see GetProofPath docstring).
//
// Successful responses use the peer's bounded acquisition lane when wired by
// the overlay; standalone handlers fall back to EventLedgerResponse.
func (h *LedgerSyncHandler) handleProofPathRequest(ctx context.Context, peerID PeerID, req *message.ProofPathRequest) error {
	// Validate up-front: independent of any configured provider, matching
	// rippled's ordering at LedgerReplayMsgHandler.cpp:46-54.
	if len(req.Key) != 32 || len(req.LedgerHash) != 32 ||
		(req.MapType != message.LedgerMapTransaction && req.MapType != message.LedgerMapAccountState) {
		h.charge(peerID, resource.FeeMalformedRequest(), "proof path request")
		return ErrPeerBadRequest
	}

	h.mu.RLock()
	provider := h.provider
	h.mu.RUnlock()

	if provider == nil {
		h.charge(peerID, resource.FeeRequestNoReply(), "proof path request unavailable")
		return nil
	}

	var header []byte
	var path [][]byte
	var err error
	if contextProvider, ok := provider.(ContextLedgerProvider); ok {
		header, path, err = contextProvider.GetProofPathContext(ctx, req.LedgerHash, req.Key, req.MapType)
	} else {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		header, path, err = provider.GetProofPath(req.LedgerHash, req.Key, req.MapType)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, ErrLedgerNotFound):
		h.charge(peerID, resource.FeeRequestNoReply(), "proof path request no ledger")
		return nil
	case errors.Is(err, ErrKeyNotFound):
		// Rippled does not pack the header on the no-node path —
		// LedgerReplayMsgHandler.cpp:84-90 returns before the header is
		// serialized at line 92. Mirror that here.
		h.charge(peerID, resource.FeeRequestNoReply(), "proof path request no node")
		return nil
	case err != nil:
		slog.Warn("ProofPath provider error",
			"t", "LedgerSync",
			"peer", peerID,
			"err", err,
		)
		// Provider returned an unexpected error; the fault is ours, not the
		// peer's, so charge the no-reply fee without signaling bad input.
		h.charge(peerID, resource.FeeRequestNoReply(), "proof path request provider error")
		return nil
	}

	frame, err := h.encodeFrame(&message.ProofPathResponse{
		Key:          req.Key,
		LedgerHash:   req.LedgerHash,
		MapType:      req.MapType,
		LedgerHeader: header,
		Path:         path,
	})
	if err != nil {
		h.charge(peerID, resource.FeeRequestNoReply(), "proof path response oversized")
		return nil
	}

	h.sendProofPathResponse(ctx, peerID, frame)
	return nil
}

// sendProofPathResponse delivers an encoded wire frame through the bounded
// priority lane. Standalone handlers without a priority sender use the legacy
// non-blocking event fallback.
//
// The handler wraps the response before this delivery boundary so the Event
// payload is a fully formed frame that Overlay.onLedgerResponse can hand
// straight to the peer's send queue.
func (h *LedgerSyncHandler) sendProofPathResponse(ctx context.Context, peerID PeerID, frame []byte) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}
	h.mu.RLock()
	prioritySender := h.prioritySender
	events := h.events
	h.mu.RUnlock()
	if prioritySender != nil {
		if err := prioritySender(ctx, peerID, frame); err != nil && ctx.Err() == nil {
			h.droppedResponses.Add(1)
			h.charge(peerID, resource.FeeRequestNoReply(), "proof path response send failed")
			slog.Debug("ProofPath priority response failed", "t", "LedgerSync", "peer", peerID, "err", err)
		}
		return
	}
	if events == nil {
		return
	}
	select {
	case events <- Event{Type: EventLedgerResponse, PeerID: peerID, Payload: frame}:
	default:
		h.droppedResponses.Add(1)
		slog.Warn("ProofPath response dropped: events channel full",
			"t", "LedgerSync", "peer", peerID, "bytes", len(frame))
	}
}

// handleReplayDeltaRequest serves an inbound mtREPLAY_DELTA_REQUEST.
//
// Mirrors rippled's LedgerReplayMsgHandler::processReplayDeltaRequest
// (rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:179-219):
//  1. Validate ledger_hash length == 32, else charge and drop.
//  2. Look up the ledger and require it to be immutable, else charge and drop.
//  3. Pack the ledger header (addRaw on LedgerInfo) and every leaf blob in
//     the tx map, in tx-map iteration order.
//  4. Refuse a response that cannot be represented within the protocol frame
//     ceiling, and charge the no-reply fee.
//
// Successful responses use the peer's bounded acquisition lane when wired by
// the overlay; standalone handlers fall back to EventLedgerResponse.
func (h *LedgerSyncHandler) handleReplayDeltaRequest(ctx context.Context, peerID PeerID, req *message.ReplayDeltaRequest) error {
	// Validate ledger_hash length first — this check is independent of any
	// configured provider, matching the rippled ordering.
	if len(req.LedgerHash) != 32 {
		h.charge(peerID, resource.FeeMalformedRequest(), "replay delta request")
		return ErrPeerBadRequest
	}

	h.mu.RLock()
	provider := h.provider
	h.mu.RUnlock()

	if provider == nil {
		h.charge(peerID, resource.FeeRequestNoReply(), "replay delta request unavailable")
		return nil
	}

	var header []byte
	var txLeaves [][]byte
	var err error
	if contextProvider, ok := provider.(ContextLedgerProvider); ok {
		header, txLeaves, err = contextProvider.GetReplayDeltaContext(ctx, req.LedgerHash)
	} else {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		header, txLeaves, err = provider.GetReplayDelta(req.LedgerHash)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil || len(header) == 0 {
		h.charge(peerID, resource.FeeRequestNoReply(), "replay delta request no ledger")
		return nil
	}

	frame, err := h.encodeFrame(&message.ReplayDeltaResponse{
		LedgerHash:   req.LedgerHash,
		LedgerHeader: header,
		Transactions: txLeaves,
	})
	if err != nil {
		slog.Warn("ReplayDelta response oversized; refusing",
			"t", "LedgerSync",
			"peer", peerID,
			"size", len(header),
			"limit", message.MaxMessageSize,
		)
		h.charge(peerID, resource.FeeRequestNoReply(), "replay delta response oversized")
		return nil
	}

	h.sendReplayDeltaResponse(ctx, peerID, frame)
	return nil
}

// sendReplayDeltaResponse delivers an encoded wire frame through the bounded
// priority lane. Standalone handlers without a priority sender use the legacy
// non-blocking event fallback.
//
// The handler wraps the response before this delivery boundary so the Event
// payload is a fully formed frame that Overlay.onLedgerResponse can hand
// straight to the peer's send queue.
func (h *LedgerSyncHandler) sendReplayDeltaResponse(ctx context.Context, peerID PeerID, frame []byte) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}
	h.mu.RLock()
	prioritySender := h.prioritySender
	events := h.events
	h.mu.RUnlock()
	if prioritySender != nil {
		if err := prioritySender(ctx, peerID, frame); err != nil && ctx.Err() == nil {
			h.droppedResponses.Add(1)
			h.charge(peerID, resource.FeeRequestNoReply(), "replay delta response send failed")
			slog.Debug("ReplayDelta priority response failed", "t", "LedgerSync", "peer", peerID, "err", err)
		}
		return
	}
	if events == nil {
		return
	}
	select {
	case events <- Event{Type: EventLedgerResponse, PeerID: peerID, Payload: frame}:
	default:
		h.droppedResponses.Add(1)
		slog.Warn("ReplayDelta response dropped: events channel full",
			"t", "LedgerSync", "peer", peerID, "bytes", len(frame))
	}
}
