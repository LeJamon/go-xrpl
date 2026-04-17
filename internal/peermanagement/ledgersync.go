package peermanagement

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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

	// Request callbacks
	onLedgerData func(ctx context.Context, peerID PeerID, data *message.LedgerData)
	onProofPath  func(ctx context.Context, peerID PeerID, response *message.ProofPathResponse)

	// Data provider for responding to requests
	provider LedgerProvider

	// Request ID counter
	nextRequestID uint64

	// Event channel for sending responses
	events chan<- Event
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

// SetProofPathCallback sets the callback for incoming proof paths.
func (h *LedgerSyncHandler) SetProofPathCallback(fn func(ctx context.Context, peerID PeerID, response *message.ProofPathResponse)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onProofPath = fn
}

// SetProvider sets the ledger data provider for responding to requests.
func (h *LedgerSyncHandler) SetProvider(provider LedgerProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.provider = provider
}

// HandleMessage handles a ledger sync message.
func (h *LedgerSyncHandler) HandleMessage(ctx context.Context, peerID PeerID, msg message.Message) error {
	switch m := msg.(type) {
	case *message.GetLedger:
		return h.handleGetLedger(ctx, peerID, m)
	case *message.LedgerData:
		return h.handleLedgerData(ctx, peerID, m)
	case *message.ProofPathRequest:
		return h.handleProofPathRequest(ctx, peerID, m)
	case *message.ProofPathResponse:
		return h.handleProofPathResponse(ctx, peerID, m)
	case *message.ReplayDeltaRequest:
		return h.handleReplayDeltaRequest(ctx, peerID, m)
	case *message.ReplayDeltaResponse:
		return h.handleReplayDeltaResponse(ctx, peerID, m)
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

	// Send response via events channel
	if h.events != nil && len(response.Nodes) > 0 {
		encoded, err := message.Encode(response)
		if err == nil {
			h.events <- Event{
				Type:    EventLedgerResponse,
				PeerID:  peerID,
				Payload: encoded,
			}
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
		return nil
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

// sendProofPathResponse encodes the response and best-effort delivers it
// onto the events channel for the overlay to ship to the requesting peer.
// Drops the response (with a warn log) if the events channel is full —
// same non-blocking pattern as sendReplayDeltaResponse.
func (h *LedgerSyncHandler) sendProofPathResponse(peerID PeerID, resp *message.ProofPathResponse) {
	if h.events == nil {
		return
	}
	encoded, err := message.Encode(resp)
	if err != nil {
		slog.Warn("ProofPath encode failed", "t", "LedgerSync", "peer", peerID, "err", err)
		return
	}
	select {
	case h.events <- Event{Type: EventLedgerResponse, PeerID: peerID, Payload: encoded}:
	default:
		slog.Warn("ProofPath response dropped: events channel full",
			"t", "LedgerSync", "peer", peerID, "bytes", len(encoded))
	}
}

// handleProofPathResponse handles proof path responses.
func (h *LedgerSyncHandler) handleProofPathResponse(ctx context.Context, peerID PeerID, resp *message.ProofPathResponse) error {
	h.mu.RLock()
	callback := h.onProofPath
	h.mu.RUnlock()

	if callback != nil {
		callback(ctx, peerID, resp)
	}

	return nil
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
//     MaxReplayDeltaResponseBytes, reply with reBAD_REQUEST (closest match
//     in TMReplyError, which has no reTOO_BUSY) and drop the populated
//     transaction list. Logged at warn level.
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
		return nil
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
			Error:      message.ReplyErrorBadRequest,
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

// sendReplayDeltaResponse encodes the response and best-effort delivers
// it onto the events channel for the overlay to ship to the requesting
// peer. Drops the response (with a warn log) if the events channel is
// full — same non-blocking pattern as Overlay.onMessageReceived, to keep
// a slow consumer from deadlocking the message dispatch path.
func (h *LedgerSyncHandler) sendReplayDeltaResponse(peerID PeerID, resp *message.ReplayDeltaResponse) {
	if h.events == nil {
		return
	}
	encoded, err := message.Encode(resp)
	if err != nil {
		slog.Warn("ReplayDelta encode failed", "t", "LedgerSync", "peer", peerID, "err", err)
		return
	}
	select {
	case h.events <- Event{Type: EventLedgerResponse, PeerID: peerID, Payload: encoded}:
	default:
		slog.Warn("ReplayDelta response dropped: events channel full",
			"t", "LedgerSync", "peer", peerID, "bytes", len(encoded))
	}
}

// handleReplayDeltaResponse handles replay delta responses.
func (h *LedgerSyncHandler) handleReplayDeltaResponse(ctx context.Context, peerID PeerID, resp *message.ReplayDeltaResponse) error {
	// TODO: Process replay delta
	return nil
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
