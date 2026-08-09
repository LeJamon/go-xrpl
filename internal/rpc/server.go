package rpc

import (
	"encoding/json"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// maxRequestBytes caps the size of a single JSON-RPC request body.
// Matches rippled's RPC::Tuning::maxRequestSize (Tuning.h) exactly so
// goxrpl and rippled reject the same oversized payloads on the wire.
const maxRequestBytes = 1_000_000

// maxBatchElements caps one batch envelope to prevent request amplification.
const maxBatchElements = 256

const jsonContentType = "application/json; charset=UTF-8"

// The size-only rippled rejection has no parser detail. Use that same fixed
// response for every parse-boundary failure so Go decoder diagnostics and
// request fragments never reach clients.
const unableToParseRequest = "Unable to parse request: "

// rpcLog returns the logger for the HTTP JSON-RPC server.
// Resolved lazily so it picks up the root logger set during CLI bootstrap.
func rpcLog() xrpllog.Logger { return xrpllog.Named(xrpllog.PartitionRPC) }

type Server struct {
	peerSourceHolder
	registry        *types.MethodRegistry
	timeout         time.Duration
	services        *types.ServiceContainer
	resourceManager *resource.Manager
}

// ServerOptions controls construction of an HTTP JSON-RPC server.
// Services may be nil for routing-only tests; constructors never mutate it.
type ServerOptions struct {
	Timeout         time.Duration
	Services        *types.ServiceContainer
	ResourceManager *resource.Manager
	PeerSource      types.PeerSource
	Registry        *types.MethodRegistry
}

var _ types.MethodDispatcher = (*Server)(nil)

// peerSourceHolder is the atomic peer-source slot embedded by both the HTTP
// and WebSocket servers, exposed through the `peers` RPC.
type peerSourceHolder struct {
	peerSource atomic.Pointer[types.PeerSource]
}

func (h *peerSourceHolder) setPeerSource(src types.PeerSource) {
	if src == nil {
		h.peerSource.Store(nil)
		return
	}
	h.peerSource.Store(&src)
}

func (h *peerSourceHolder) loadPeerSource() types.PeerSource {
	if p := h.peerSource.Load(); p != nil {
		return *p
	}
	return nil
}

// NewServer creates a new RPC server. The service container is read by
// handlers through ctx.Services and is never changed by this constructor.
func NewServer(options ServerOptions) *Server {
	manager := options.ResourceManager
	if manager == nil {
		manager = resource.NewManager(nil, nil)
	}
	server := &Server{
		registry:        options.Registry,
		timeout:         options.Timeout,
		services:        options.Services,
		resourceManager: manager,
	}
	if server.registry == nil {
		server.registry = defaultMethodRegistry()
	}
	server.setPeerSource(options.PeerSource)

	return server
}

// jsonRPCResponseOptions contains optional fields for JSON-RPC responses
// These fields are at the top level, not inside the result object
type jsonRPCResponseOptions struct {
	Warning   string                // "load" when approaching rate limit
	Warnings  []types.WarningObject // Array of warning objects
	Forwarded bool                  // True if forwarded from Clio to P2P server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			rpcLog().Error("rpc transport panic", "err", rec, "stack", string(debug.Stack()), "method", r.Method, "remote", r.RemoteAddr)
			s.writeXrplResponseWithOptions(w, nil, nil, rpcInternalError(), nil)
		}
	}()

	if !transportAuthorized(r.Context()) {
		if !authorizeTransport(w, r, GetPortContext(r.Context())) {
			return
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Content-Type", jsonContentType)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "POST" {
		writePlainHTTPError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	s.handlePostRequest(w, r)
}

type jsonReal float64

// rpcMethodNotFoundCode is the JSON-RPC error code rippled attaches to malformed
// batch elements (ServerHandler.cpp:605, method_not_found = -32601). It is
// distinct from go-xrpl's XRPL-token error model and appears only inside the
// batch malformed-element replies, to match rippled byte-for-byte.
const rpcMethodNotFoundCode = -32601

// forbiddenJSONRPCCode is the JSON-RPC error code rippled attaches to a batch
// element refused at the role layer (ServerHandler.cpp:607, forbidden = -32605).
// It is distinct from method_not_found and appears only inside batch
// forbidden-element replies, matching rippled byte-for-byte.
const forbiddenJSONRPCCode = -32605

// serverOverloadedJSONRPCCode is the JSON-RPC error code rippled attaches to a
// batch element whose caller is over its per-IP resource budget
// (ServerHandler.cpp:606, server_overloaded = -32604). It appears only inside
// batch overload-element replies, matching rippled byte-for-byte.
const serverOverloadedJSONRPCCode = -32604

// credentialKeys are request fields masked in error envelopes. Matching is
// case-insensitive and recursive so alternate client spellings cannot bypass
// redaction.
var credentialKeys = []string{
	"secret",
	"seed",
	"passphrase",
	"seed_hex",
	"seedhex",
	"password",
	"url_password",
	"urlpassword",
	"admin_user",
	"adminuser",
	"admin_password",
	"adminpassword",
}

// maskedValue is the literal rippled writes in place of credential
// values (ServerHandler.cpp:536). Masking preserves the key so a
// debugging client can see a credential was supplied.
const maskedValue = "<masked>"

type methodResolution struct {
	handler  types.MethodHandler
	resolved bool
}

type syncFailure uint8

const (
	syncFailureNoNetwork syncFailure = iota
	syncFailureNoCurrent
	syncFailureNoClosed
)

// ExecuteMethod implements types.MethodDispatcher, allowing the 'json' RPC
// method to forward calls through the same method registry. The caller's
// context is reused so the forwarded method keeps the request's timeout,
// role, client IP and api version, and is charged for load under the real
// client IP, so forwarded calls remain subject to the same per-IP charging
// and request timeout as their parent request.
func (s *Server) ExecuteMethod(ctx *types.RpcContext, method string, params []byte) (any, *types.RpcError) {
	return dispatchNestedMethod(s.registry, ctx, method, json.RawMessage(params), rpcLog())
}
