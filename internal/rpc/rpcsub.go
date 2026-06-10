package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

const (
	// rpcSubQueueLimit bounds the per-url outbound event queue. rippled
	// buffers RPCSub events without limit; a bound keeps a dead or slow
	// endpoint from growing memory indefinitely. Overflowing events are
	// dropped by Connection.TrySend (the registry never installs a
	// Disconnect callback, so a slow endpoint is throttled, not removed —
	// rippled keeps retrying forever too).
	rpcSubQueueLimit = 256

	// rpcSubRequestTimeout matches rippled's RPC_WEBHOOK_TIMEOUT.
	rpcSubRequestTimeout = 30 * time.Second
)

// URLSubscriptionRegistry implements rippled's url-based (RPCSub) admin
// subscriptions: each url maps to one long-lived subscriber registered with
// the shared subscription manager, so stream/account/book broadcasts fan
// out to urls exactly like they do to WebSocket connections. A per-url
// delivery goroutine drains the subscriber's queue and POSTs every event to
// the url as a JSON-RPC "event" call with an injected per-url sequence
// number and basic auth.
type URLSubscriptionRegistry struct {
	ws     *WebSocketServer
	client *http.Client
	// ctx cancels in-flight deliveries on Close so shutdown isn't held
	// hostage by a stalled endpoint.
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	subs   map[string]*rpcSub
	closed bool
}

func newURLSubscriptionRegistry(ws *WebSocketServer) *URLSubscriptionRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	return &URLSubscriptionRegistry{
		ws:     ws,
		client: &http.Client{Timeout: rpcSubRequestTimeout},
		ctx:    ctx,
		cancel: cancel,
		subs:   make(map[string]*rpcSub),
	}
}

// Subscribe finds or creates the url's subscriber, applies the requested
// streams/accounts/books to it, and returns the subscribe ack. Mirrors
// doSubscribe's url branch: an unparseable url or unsupported scheme is
// rpcINVALID_PARAMS; on reuse, credentials are only updated via the
// deprecated username/password members. The caller has already verified the
// admin role.
func (r *URLSubscriptionRegistry) Subscribe(ctx *types.RpcContext, request types.SubscriptionRequest) (map[string]any, *types.RpcError) {
	sub, rpcErr := r.findOrCreate(request)
	if rpcErr != nil {
		return nil, rpcErr
	}
	// Like rippled, a failing stream/account/book parse leaves the freshly
	// created registry entry in place.
	if rpcErr := r.ws.subscriptionManager.HandleSubscribe(sub.conn, request, true); rpcErr != nil {
		return nil, rpcErr
	}
	return r.ws.buildSubscribeAck(ctx, request), nil
}

// Unsubscribe removes the listed streams/accounts/books from the url's
// subscriber and drops the registry entry once no stream subscriptions
// remain. An unknown url is silent success (Unsubscribe.cpp:52-53).
func (r *URLSubscriptionRegistry) Unsubscribe(ctx *types.RpcContext, request types.SubscriptionRequest) (map[string]any, *types.RpcError) {
	r.mu.Lock()
	sub, ok := r.subs[request.URL]
	r.mu.Unlock()
	if !ok {
		return map[string]any{}, nil
	}
	if rpcErr := r.ws.subscriptionManager.HandleUnsubscribe(sub.conn, request, true); rpcErr != nil {
		return nil, rpcErr
	}
	r.tryRemove(request.URL)
	return map[string]any{}, nil
}

func (r *URLSubscriptionRegistry) findOrCreate(request types.SubscriptionRequest) (*rpcSub, *types.RpcError) {
	username, password, usernameSet, passwordSet := request.URLCredentials()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, types.RpcErrorInternal("Internal error.")
	}
	if sub, ok := r.subs[request.URL]; ok {
		// Credentials on an existing url subscription are only updated via
		// the deprecated username/password members; url_username and
		// url_password are ignored on reuse, exactly like doSubscribe.
		sub.updateCredentials(username, usernameSet, password, passwordSet)
		return sub, nil
	}

	endpoint, rpcErr := parseRPCSubURL(request.URL)
	if rpcErr != nil {
		return nil, rpcErr
	}
	sub := &rpcSub{
		endpoint: endpoint,
		client:   r.client,
		ctx:      r.ctx,
		username: username,
		password: password,
		conn: &types.Connection{
			ID:            "rpcsub:" + request.URL,
			Subscriptions: make(map[types.SubscriptionType]types.SubscriptionConfig),
			SendChannel:   make(chan []byte, rpcSubQueueLimit),
			CloseChannel:  make(chan struct{}),
		},
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	r.ws.subscriptionManager.AddConnection(sub.conn)
	go sub.run()
	r.subs[request.URL] = sub
	return sub, nil
}

// tryRemove drops the url's registry entry once it holds no stream
// subscriptions, mirroring NetworkOPs::tryRemoveRpcSub. Account and book
// subscriptions don't keep the entry alive: in rippled the registry holds
// the only strong reference, so removal destroys the subscriber and its
// remaining subscriptions with it — here the manager connection and the
// delivery goroutine are torn down the same way.
func (r *URLSubscriptionRegistry) tryRemove(rawURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub, ok := r.subs[rawURL]
	if !ok {
		return
	}
	if r.ws.subscriptionManager.HasStreamSubscriptions(sub.conn.ID) {
		return
	}
	delete(r.subs, rawURL)
	r.ws.subscriptionManager.RemoveConnection(sub.conn.ID)
	sub.stop()
}

// Close stops every url subscription, cancelling in-flight deliveries, and
// waits for the delivery goroutines to exit.
func (r *URLSubscriptionRegistry) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	subs := r.subs
	r.subs = make(map[string]*rpcSub)
	r.mu.Unlock()

	r.cancel()
	for _, sub := range subs {
		r.ws.subscriptionManager.RemoveConnection(sub.conn.ID)
		sub.stop()
	}
	for _, sub := range subs {
		<-sub.finished
	}
}

// parseRPCSubURL validates a subscription url the way RPCSub's constructor
// does — http or https only, default ports 80/443 — and returns the
// normalised endpoint to POST events to. The invalidParams messages match
// rippled's verbatim ("Failed to parse url." / "Only http and https is
// supported.").
func parseRPCSubURL(raw string) (string, *types.RpcError) {
	parseErr := types.RpcErrorInvalidParams("Failed to parse url.")
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", parseErr
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", types.RpcErrorInvalidParams("Only http and https is supported.")
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", parseErr
	}
	return scheme + "://" + net.JoinHostPort(u.Hostname(), port) + u.RequestURI(), nil
}

// rpcSub is one url subscription: a subscription-manager connection whose
// send channel is drained by a delivery goroutine POSTing each event to the
// url, one at a time and in order, like RPCSub::sendThread.
type rpcSub struct {
	endpoint string
	client   *http.Client
	ctx      context.Context
	conn     *types.Connection
	done     chan struct{}
	finished chan struct{}

	credMu   sync.Mutex
	username string
	password string

	// seq numbers delivered events per url, starting at 1 (RPCSub::mSeq).
	seq uint64
}

func (s *rpcSub) updateCredentials(username string, usernameSet bool, password string, passwordSet bool) {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	if usernameSet {
		s.username = username
	}
	if passwordSet {
		s.password = password
	}
}

func (s *rpcSub) credentials() (string, string) {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	return s.username, s.password
}

func (s *rpcSub) stop() {
	close(s.done)
}

func (s *rpcSub) run() {
	defer close(s.finished)
	for {
		select {
		case data := <-s.conn.SendChannel:
			s.deliver(data)
		case <-s.done:
			return
		}
	}
}

// deliver wraps one broadcast event in the JSON-RPC call rippled's RPCSub
// emits — {"method":"event","params":{...,"seq":N},"id":1} — and POSTs it.
// Failures are logged and dropped (fire-and-forget), like sendThread's
// catch-and-log around RPCCall::fromNetwork.
func (s *rpcSub) deliver(data []byte) {
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		wsLog().Error("rpcsub: undecodable broadcast event", "url", s.endpoint, "err", err)
		return
	}
	s.seq++
	event["seq"] = s.seq
	body, err := json.Marshal(map[string]any{
		"method": "event",
		"params": event,
		"id":     1,
	})
	if err != nil {
		wsLog().Error("rpcsub: event marshal failed", "url", s.endpoint, "err", err)
		return
	}
	body = append(body, '\n')

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		wsLog().Error("rpcsub: request build failed", "url", s.endpoint, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// rippled always sends basic auth, even with empty credentials.
	username, password := s.credentials()
	req.SetBasicAuth(username, password)

	resp, err := s.client.Do(req)
	if err != nil {
		wsLog().Info("rpcsub: event delivery failed", "url", s.endpoint, "err", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
