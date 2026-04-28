package peermanagement

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// Sentinel errors returned by LedgerProvider.GetProofPath so the handler
// can map them back to the appropriate TMReplyError code on the wire.
//
// Mirrors rippled's reNO_LEDGER (ledger unknown / not yet immutable) and
// reNO_NODE (the requested key is not present in the selected map) at
// rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:62-90.
var (
	// ErrLedgerNotFound signals the requested ledger is unknown to the
	// provider or not yet immutable. The handler responds with
	// ReplyErrorNoLedger.
	ErrLedgerNotFound = errors.New("ledger not found")
	// ErrKeyNotFound signals the ledger exists but the requested key has
	// no leaf in the selected map. The handler responds with
	// ReplyErrorNoNode.
	ErrKeyNotFound = errors.New("key not found in ledger map")
	// ErrPeerBadRequest is returned by LedgerSyncHandler.HandleMessage
	// when the inbound request was malformed (e.g. bad field lengths,
	// invalid enum values) and we replied with ReplyErrorBadRequest. The
	// overlay dispatcher uses it to attribute the failure to the
	// originating peer via IncPeerBadData. Mirrors rippled's
	// fee.update(feeInvalidData) path for reBAD_REQUEST replies.
	ErrPeerBadRequest = errors.New("peer sent bad request")
)

// LedgerDataType represents the type of ledger data being requested.
type LedgerDataType int

const (
	// LedgerDataTypeUnknown is an unknown data type.
	LedgerDataTypeUnknown LedgerDataType = iota
	// LedgerDataTypeHeader is the ledger header.
	LedgerDataTypeHeader
	// LedgerDataTypeAccountState is account state data.
	LedgerDataTypeAccountState
	// LedgerDataTypeTransactionNode is transaction tree nodes.
	LedgerDataTypeTransactionNode
	// LedgerDataTypeTransactionSetCandidate is transaction set candidates.
	LedgerDataTypeTransactionSetCandidate
)

// RequestState tracks the state of a ledger data request.
type RequestState int

const (
	// RequestStatePending means the request is waiting to be sent.
	RequestStatePending RequestState = iota
	// RequestStateSent means the request has been sent.
	RequestStateSent
	// RequestStateReceived means the response has been received.
	RequestStateReceived
	// RequestStateFailed means the request failed.
	RequestStateFailed
	// RequestStateTimeout means the request timed out.
	RequestStateTimeout
)

// Ledger sync constants.
const (
	// DefaultRequestTimeout is the default timeout for ledger data requests.
	DefaultRequestTimeout = 30 * time.Second

	// MaxConcurrentRequests is the maximum number of concurrent requests per peer.
	MaxConcurrentRequests = 5

	// MaxReplayDeltaResponseBytes caps the total uncompressed payload size of
	// a single mtREPLAY_DELTA_RESPONSE we will emit. Rippled does not enforce
	// an upstream cap, but our framing layer enforces its own limit
	// (message.MaxMessageSize = 64 MiB) and any response above that boundary
	// would be dropped at the codec layer. A 16 MiB ceiling leaves comfortable
	// headroom for the wire envelope and protects the event channel from
	// arbitrarily large allocations driven by remote requests.
	MaxReplayDeltaResponseBytes = 16 * 1024 * 1024
)

// LedgerRequest represents a request for ledger data.
type LedgerRequest struct {
	LedgerHash   []byte
	LedgerSeq    uint32
	DataType     LedgerDataType
	NodeIDs      [][]byte // Specific nodes to request
	State        RequestState
	CreatedAt    time.Time
	SentAt       time.Time
	CompletedAt  time.Time
	Peer         PeerID
	ResponseData [][]byte
	Error        error
}

// LedgerProvider is called to retrieve ledger data for responses.
type LedgerProvider interface {
	// GetLedgerHeader returns the header for a ledger.
	GetLedgerHeader(hash []byte, seq uint32) ([]byte, error)
	// GetAccountStateNode returns an account state node.
	GetAccountStateNode(ledgerHash []byte, nodeID []byte) ([]byte, error)
	// GetTransactionNode returns a transaction tree node.
	GetTransactionNode(ledgerHash []byte, nodeID []byte) ([]byte, error)
	// GetReplayDelta returns the serialized ledger header and every
	// transaction leaf blob (in tx-map order) for the given ledger hash.
	// Implementations must only return data for closed/immutable ledgers
	// (mirrors rippled's ledger->isImmutable() check in
	// LedgerReplayMsgHandler::processReplayDeltaRequest). When the ledger
	// is unknown or not yet immutable, return (nil, nil, nil) so the
	// handler can reply with reNO_LEDGER.
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
	//     yet immutable. The handler emits ReplyErrorNoLedger.
	//   - (nil, nil, ErrKeyNotFound) when the ledger exists but the key
	//     has no leaf in the selected map. The handler emits
	//     ReplyErrorNoNode. Rippled returns reNO_NODE without a header
	//     in this case (LedgerReplayMsgHandler.cpp:84-90, where header
	//     packing happens AFTER the no-path early-return), so we mirror
	//     that and do not require the header here.
	//   - (header, path, nil) on success.
	//   - any other error → handler emits ReplyErrorBadRequest and logs
	//     at warn.
	GetProofPath(ledgerHash []byte, key []byte, mapType message.LedgerMapType) (header []byte, path [][]byte, err error)
}

// LedgerSyncHandler handles ledger synchronization messages.
type LedgerSyncHandler struct {
	mu sync.RWMutex

	// Pending requests
	requests map[uint64]*LedgerRequest

	// Request callbacks. mtREPLAY_DELTA_RESPONSE and mtPROOF_PATH_RESPONSE
	// are intentionally NOT dispatched to callbacks here — orchestration
	// (state machine, hash verification, adoption) lives in the consensus
	// router, which reads those responses via the overlay's Messages()
	// channel. Dispatching them twice would race the inbound-acquisition
	// state. See the comment on HandleMessage below.
	onLedgerData func(ctx context.Context, peerID PeerID, data *message.LedgerData)

	// Data provider for responding to requests
	provider LedgerProvider

	// Request ID counter
	nextRequestID uint64

	// Event channel for sending responses
	events chan<- Event

	// droppedResponses counts how many response events we had to drop
	// because the events channel was full (slow consumer). Exposed via
	// DroppedResponses so the overlay can aggregate into server_info.
	droppedResponses atomic.Uint64

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
		requests: make(map[uint64]*LedgerRequest),
		events:   events,
	}
}

// SetLedgerDataCallback sets the callback for incoming ledger data.
func (h *LedgerSyncHandler) SetLedgerDataCallback(fn func(ctx context.Context, peerID PeerID, data *message.LedgerData)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onLedgerData = fn
}

// SetProvider sets the ledger data provider for responding to requests.
func (h *LedgerSyncHandler) SetProvider(provider LedgerProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.provider = provider
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

// HandleMessage handles a ledger sync message.
//
// mtREPLAY_DELTA_RESPONSE intentionally has no arm here: orchestration of an
// outbound replay-delta acquisition (state machine, hash verification,
// adoption) lives in the consensus router, which receives the response via
// the overlay's Messages() channel. Delivering the response twice — once to
// the router, once to this handler — would create competing consumers and a
// race on the inbound-acquisition state.
func (h *LedgerSyncHandler) HandleMessage(ctx context.Context, peerID PeerID, msg message.Message) error {
	switch m := msg.(type) {
	case *message.GetLedger:
		return h.handleGetLedger(ctx, peerID, m)
	case *message.LedgerData:
		return h.handleLedgerData(ctx, peerID, m)
	case *message.ProofPathRequest:
		return h.handleProofPathRequest(ctx, peerID, m)
	case *message.ReplayDeltaRequest:
		return h.handleReplayDeltaRequest(ctx, peerID, m)
	}
	return nil
}

// handleGetLedger handles incoming ledger data requests.
func (h *LedgerSyncHandler) handleGetLedger(ctx context.Context, peerID PeerID, req *message.GetLedger) error {
	h.mu.RLock()
	provider := h.provider
	h.mu.RUnlock()

	if provider == nil {
		// No provider, can't respond
		return nil
	}

	// Build response
	response := &message.LedgerData{
		LedgerSeq:  req.LedgerSeq,
		LedgerHash: req.LedgerHash,
	}

	// Get requested data based on query type
	if req.QueryType == message.QueryTypeLedgerHeader {
		header, err := provider.GetLedgerHeader(req.LedgerHash, req.LedgerSeq)
		if err == nil && header != nil {
			response.Nodes = append(response.Nodes, message.LedgerNode{NodeData: header})
		}
	} else if req.QueryType == message.QueryTypeAccountState {
		for _, nodeID := range req.NodeIDs {
			node, err := provider.GetAccountStateNode(req.LedgerHash, nodeID)
			if err == nil && node != nil {
				response.Nodes = append(response.Nodes, message.LedgerNode{NodeData: node, NodeID: nodeID})
			}
		}
	} else if req.QueryType == message.QueryTypeTransactionData {
		for _, nodeID := range req.NodeIDs {
			node, err := provider.GetTransactionNode(req.LedgerHash, nodeID)
			if err == nil && node != nil {
				response.Nodes = append(response.Nodes, message.LedgerNode{NodeData: node, NodeID: nodeID})
			}
		}
	}

	// Send response via events channel. We ship the fully-framed wire
	// message (6-byte header + protobuf body) so Overlay.onLedgerResponse
	// can hand it straight to the peer's send queue without having to
	// know the message type. Matches the handlePing round-trip in
	// overlay.go which also writes through BuildWireMessage.
	if h.events != nil && len(response.Nodes) > 0 {
		encoded, err := message.Encode(response)
		if err != nil {
			return nil
		}
		frame, err := message.BuildWireMessage(message.TypeLedgerData, encoded)
		if err != nil {
			return nil
		}
		h.events <- Event{
			Type:    EventLedgerResponse,
			PeerID:  peerID,
			Payload: frame,
		}
	}

	return nil
}

// handleLedgerData handles incoming ledger data responses.
func (h *LedgerSyncHandler) handleLedgerData(ctx context.Context, peerID PeerID, data *message.LedgerData) error {
	h.mu.RLock()
	callback := h.onLedgerData
	h.mu.RUnlock()

	if callback != nil {
		callback(ctx, peerID, data)
	}

	return nil
}

// handleProofPathRequest serves an inbound mtPROOF_PATH_REQ.
//
// Mirrors rippled's LedgerReplayMsgHandler::processProofPathRequest
// (rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:40-104):
//  1. Validate len(key) == 32, len(ledgerHash) == 32, type ∈ {1, 2}.
//     Any failure → reply with reBAD_REQUEST. The fields key/ledgerHash/
//     type are echoed back on every reply, even on validation errors —
//     rippled sets them before any further checks.
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
// The encoded response is pushed onto the events channel as
// EventLedgerResponse so the overlay can ship it to the requesting peer
// (mirrors handleGetLedger and handleReplayDeltaRequest).
func (h *LedgerSyncHandler) handleProofPathRequest(_ context.Context, peerID PeerID, req *message.ProofPathRequest) error {
	// Validate up-front: independent of any configured provider, matching
	// rippled's ordering at LedgerReplayMsgHandler.cpp:46-54.
	if len(req.Key) != 32 || len(req.LedgerHash) != 32 ||
		(req.MapType != message.LedgerMapTransaction && req.MapType != message.LedgerMapAccountState) {
		h.sendProofPathResponse(peerID, &message.ProofPathResponse{
			Key:        req.Key,
			LedgerHash: req.LedgerHash,
			MapType:    req.MapType,
			Error:      message.ReplyErrorBadRequest,
		})
		return ErrPeerBadRequest
	}

	h.mu.RLock()
	provider := h.provider
	h.mu.RUnlock()

	if provider == nil {
		// No provider wired: silently drop (matches handleGetLedger and
		// handleReplayDeltaRequest).
		return nil
	}

	header, path, err := provider.GetProofPath(req.LedgerHash, req.Key, req.MapType)
	switch {
	case errors.Is(err, ErrLedgerNotFound):
		h.sendProofPathResponse(peerID, &message.ProofPathResponse{
			Key:        req.Key,
			LedgerHash: req.LedgerHash,
			MapType:    req.MapType,
			Error:      message.ReplyErrorNoLedger,
		})
		return nil
	case errors.Is(err, ErrKeyNotFound):
		// Rippled does not pack the header on the no-node path —
		// LedgerReplayMsgHandler.cpp:84-90 returns before the header is
		// serialized at line 92. Mirror that here.
		h.sendProofPathResponse(peerID, &message.ProofPathResponse{
			Key:        req.Key,
			LedgerHash: req.LedgerHash,
			MapType:    req.MapType,
			Error:      message.ReplyErrorNoNode,
		})
		return nil
	case err != nil:
		slog.Warn("ProofPath provider error",
			"t", "LedgerSync",
			"peer", peerID,
			"err", err,
		)
		// Provider returned an unexpected error; we reply with
		// reBAD_REQUEST but the fault is ours, not the peer's, so do
		// not signal ErrPeerBadRequest here.
		h.sendProofPathResponse(peerID, &message.ProofPathResponse{
			Key:        req.Key,
			LedgerHash: req.LedgerHash,
			MapType:    req.MapType,
			Error:      message.ReplyErrorBadRequest,
		})
		return nil
	}

	h.sendProofPathResponse(peerID, &message.ProofPathResponse{
		Key:          req.Key,
		LedgerHash:   req.LedgerHash,
		MapType:      req.MapType,
		LedgerHeader: header,
		Path:         path,
	})
	return nil
}

// sendProofPathResponse encodes the response, wraps it in the XRPL
// wire-frame header (6-byte type/size envelope), and best-effort delivers
// it onto the events channel for the overlay to ship to the requesting
// peer. Drops the response (with a warn log) if the events channel is
// full — same non-blocking pattern as sendReplayDeltaResponse.
//
// The wire-frame wrap lives here (not in the overlay) so the Event
// payload is a fully-formed frame that Overlay.onLedgerResponse can
// hand straight to the peer's send queue. Mirrors the handlePing
// round-trip in overlay.go, which also writes through BuildWireMessage.
func (h *LedgerSyncHandler) sendProofPathResponse(peerID PeerID, resp *message.ProofPathResponse) {
	if h.events == nil {
		return
	}
	encoded, err := message.Encode(resp)
	if err != nil {
		slog.Warn("ProofPath encode failed", "t", "LedgerSync", "peer", peerID, "err", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeProofPathResponse, encoded)
	if err != nil {
		slog.Warn("ProofPath frame build failed", "t", "LedgerSync", "peer", peerID, "err", err)
		return
	}
	select {
	case h.events <- Event{Type: EventLedgerResponse, PeerID: peerID, Payload: frame}:
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
//  1. Validate ledger_hash length == 32, else reply with reBAD_REQUEST.
//  2. Look up the ledger and require it to be immutable, else reply with
//     reNO_LEDGER. Both checks are folded into LedgerProvider.GetReplayDelta.
//  3. Pack the ledger header (addRaw on LedgerInfo) and every leaf blob in
//     the tx map, in tx-map iteration order.
//  4. Defensive size cap: if the response payload would exceed
//     MaxReplayDeltaResponseBytes, reply with reNO_LEDGER and drop the
//     populated transaction list. TMReplyError has no reTOO_BUSY; we
//     pick reNO_LEDGER over reBAD_REQUEST because the request itself is
//     well-formed — we just can't serve a response of this size. The
//     lighter error avoids charging the requester feeMalformedRequest
//     on rippled's side (PeerImp.cpp:1545-1548).
//
// The encoded response is pushed onto the events channel as
// EventLedgerResponse so the overlay can ship it to the requesting peer
// (mirrors handleGetLedger).
func (h *LedgerSyncHandler) handleReplayDeltaRequest(_ context.Context, peerID PeerID, req *message.ReplayDeltaRequest) error {
	// Validate ledger_hash length first — this check is independent of any
	// configured provider, matching the rippled ordering.
	if len(req.LedgerHash) != 32 {
		h.sendReplayDeltaResponse(peerID, &message.ReplayDeltaResponse{
			LedgerHash: req.LedgerHash,
			Error:      message.ReplyErrorBadRequest,
		})
		return ErrPeerBadRequest
	}

	h.mu.RLock()
	provider := h.provider
	h.mu.RUnlock()

	if provider == nil {
		// No provider wired: silently drop (matches handleGetLedger).
		return nil
	}

	header, txLeaves, err := provider.GetReplayDelta(req.LedgerHash)
	if err != nil || len(header) == 0 {
		h.sendReplayDeltaResponse(peerID, &message.ReplayDeltaResponse{
			LedgerHash: req.LedgerHash,
			Error:      message.ReplyErrorNoLedger,
		})
		return nil
	}

	// Defensive size cap: refuse to encode a response above our ceiling.
	// Use ReplyErrorNoLedger rather than ReplyErrorBadRequest — the
	// request isn't malformed, we just can't serve the response at this
	// size. Rippled's PeerImp.cpp:1545-1548 charges feeMalformedRequest
	// (200 drops) for reBAD_REQUEST vs feeRequestNoReply (10 drops) for
	// everything else, so the lighter error code avoids wrongly fee-
	// charging an honest requester.
	total := len(header)
	for _, tx := range txLeaves {
		total += len(tx)
	}
	if total > MaxReplayDeltaResponseBytes {
		slog.Warn("ReplayDelta response oversized; refusing",
			"t", "LedgerSync",
			"peer", peerID,
			"size", total,
			"limit", MaxReplayDeltaResponseBytes,
		)
		h.sendReplayDeltaResponse(peerID, &message.ReplayDeltaResponse{
			LedgerHash: req.LedgerHash,
			Error:      message.ReplyErrorNoLedger,
		})
		return nil
	}

	h.sendReplayDeltaResponse(peerID, &message.ReplayDeltaResponse{
		LedgerHash:   req.LedgerHash,
		LedgerHeader: header,
		Transactions: txLeaves,
	})
	return nil
}

// sendReplayDeltaResponse encodes the response, wraps it in the XRPL
// wire-frame header (6-byte type/size envelope), and best-effort delivers
// it onto the events channel for the overlay to ship to the requesting
// peer. Drops the response (with a warn log) if the events channel is
// full — same non-blocking pattern as Overlay.onMessageReceived, to keep
// a slow consumer from deadlocking the message dispatch path.
//
// The wire-frame wrap lives here (not in the overlay) so the Event
// payload is a fully-formed frame that Overlay.onLedgerResponse can
// hand straight to the peer's send queue. Without the wire header the
// peer on the other end parses the first 6 protobuf bytes as a garbage
// frame header and stalls reading the phantom payload — the regression
// this commit fixes.
func (h *LedgerSyncHandler) sendReplayDeltaResponse(peerID PeerID, resp *message.ReplayDeltaResponse) {
	if h.events == nil {
		return
	}
	encoded, err := message.Encode(resp)
	if err != nil {
		slog.Warn("ReplayDelta encode failed", "t", "LedgerSync", "peer", peerID, "err", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeReplayDeltaResponse, encoded)
	if err != nil {
		slog.Warn("ReplayDelta frame build failed", "t", "LedgerSync", "peer", peerID, "err", err)
		return
	}
	select {
	case h.events <- Event{Type: EventLedgerResponse, PeerID: peerID, Payload: frame}:
	default:
		h.droppedResponses.Add(1)
		slog.Warn("ReplayDelta response dropped: events channel full",
			"t", "LedgerSync", "peer", peerID, "bytes", len(frame))
	}
}

// CreateRequest creates a new ledger data request.
func (h *LedgerSyncHandler) CreateRequest(ledgerHash []byte, ledgerSeq uint32, dataType LedgerDataType) *LedgerRequest {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextRequestID++
	req := &LedgerRequest{
		LedgerHash: ledgerHash,
		LedgerSeq:  ledgerSeq,
		DataType:   dataType,
		State:      RequestStatePending,
		CreatedAt:  time.Now(),
	}
	h.requests[h.nextRequestID] = req

	return req
}

// GetPendingRequests returns all pending requests for a peer.
func (h *LedgerSyncHandler) GetPendingRequests(peerID PeerID) []*LedgerRequest {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var result []*LedgerRequest
	for _, req := range h.requests {
		if req.State == RequestStateSent && req.Peer == peerID {
			result = append(result, req)
		}
	}
	return result
}

// CleanupExpiredRequests removes timed-out requests.
func (h *LedgerSyncHandler) CleanupExpiredRequests() {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	for id, req := range h.requests {
		if req.State == RequestStateSent && now.Sub(req.SentAt) > DefaultRequestTimeout {
			req.State = RequestStateTimeout
			delete(h.requests, id)
		}
	}
}

// PendingRequestCount returns the number of pending requests.
func (h *LedgerSyncHandler) PendingRequestCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	count := 0
	for _, req := range h.requests {
		if req.State == RequestStatePending || req.State == RequestStateSent {
			count++
		}
	}
	return count
}
