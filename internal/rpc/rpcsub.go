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
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

const (
	// rpcSubQueueLimit bounds each URL's outbound event queue so a dead or slow
	// endpoint cannot grow memory indefinitely. Overflowing events are dropped
	// by Connection.TrySend.
	rpcSubQueueLimit = 256

	// rpcSubRequestTimeout matches rippled's RPC_WEBHOOK_TIMEOUT.
	rpcSubRequestTimeout = 30 * time.Second

	// These limits bound both the registry memory footprint and the number of
	// delivery goroutines an administrator can create. A zero/negative custom
	// limit disables that dimension; the constructor installs bounded defaults.
	rpcSubMaxEntries      = 256
	rpcSubMaxWorkers      = 256
	rpcSubMaxPerPrincipal = 32
)

type rpcSubMetrics struct {
	queued           atomic.Uint64
	dropped          atomic.Uint64
	deliveryFailures atomic.Uint64
	capacityRejects  atomic.Uint64
}

type rpcSubMetricsSnapshot struct {
	Queued           uint64
	Dropped          uint64
	DeliveryFailures uint64
	CapacityRejects  uint64
}

func (m *rpcSubMetrics) snapshot() rpcSubMetricsSnapshot {
	if m == nil {
		return rpcSubMetricsSnapshot{}
	}
	return rpcSubMetricsSnapshot{
		Queued:           m.queued.Load(),
		Dropped:          m.dropped.Load(),
		DeliveryFailures: m.deliveryFailures.Load(),
		CapacityRejects:  m.capacityRejects.Load(),
	}
}

// rpcSubMetricMilestone keeps runtime telemetry useful without emitting one
// log record for every event on a busy subscription. The first observation
// and powers of two provide an inexpensive cumulative signal as pressure
// grows.
func rpcSubMetricMilestone(value uint64) bool {
	return value == 1 || value&(value-1) == 0
}

func (m *rpcSubMetrics) recordQueued(endpoint string) {
	value := m.queued.Add(1)
	if rpcSubMetricMilestone(value) {
		wsLog().Debug("rpcsub: outbound event queued", "url", endpoint, "count", value, "queue_limit", rpcSubQueueLimit)
	}
}

func (m *rpcSubMetrics) recordDropped(endpoint string) {
	value := m.dropped.Add(1)
	if rpcSubMetricMilestone(value) {
		wsLog().Warn("rpcsub: outbound queue drops", "url", endpoint, "count", value, "queue_limit", rpcSubQueueLimit)
	}
}

func (m *rpcSubMetrics) recordDeliveryFailure(endpoint, reason string, details ...any) {
	value := m.deliveryFailures.Add(1)
	if !rpcSubMetricMilestone(value) {
		return
	}
	args := make([]any, 0, 6+len(details))
	args = append(args, "url", endpoint, "count", value, "reason", reason)
	args = append(args, details...)
	wsLog().Warn("rpcsub: delivery failures", args...)
}

// URLSubscriptionRegistry owns URL-based admin subscriptions. Each URL maps
// to one subscriber in the shared manager and one delivery goroutine.
type URLSubscriptionRegistry struct {
	ws     *WebSocketServer
	client *http.Client
	// ctx cancels in-flight deliveries on Close so shutdown isn't held
	// hostage by a stalled endpoint.
	ctx    context.Context
	cancel context.CancelFunc

	mu               sync.Mutex
	subs             map[string]*rpcSub
	principalCounts  map[string]int
	maxEntries       int
	maxWorkers       int
	maxPerPrincipal  int
	workers          int
	principalWorkers map[string]int
	workerWG         sync.WaitGroup
	metrics          rpcSubMetrics
	closed           bool
	closeDone        chan struct{}
}

func newURLSubscriptionRegistry(ws *WebSocketServer) *URLSubscriptionRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	return &URLSubscriptionRegistry{
		ws: ws,
		client: &http.Client{
			Timeout: rpcSubRequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ctx:              ctx,
		cancel:           cancel,
		subs:             make(map[string]*rpcSub),
		principalCounts:  make(map[string]int),
		maxEntries:       rpcSubMaxEntries,
		maxWorkers:       rpcSubMaxWorkers,
		maxPerPrincipal:  rpcSubMaxPerPrincipal,
		closeDone:        make(chan struct{}),
		principalWorkers: make(map[string]int),
	}
}

func (r *URLSubscriptionRegistry) metricsSnapshot() rpcSubMetricsSnapshot {
	return r.metrics.snapshot()
}

// Subscribe finds or creates the url's subscriber, applies the requested
// streams/accounts/books to it transactionally, and returns the subscribe ack.
// Mirrors doSubscribe's URL branch for credential reuse: on an existing
// destination only deprecated username/password members update credentials.
// The caller has already verified the admin role.
func (r *URLSubscriptionRegistry) Subscribe(ctx *types.RpcContext, request types.SubscriptionRequest) (map[string]any, *types.RpcError) {
	if ctx != nil {
		request.ApiVersion = ctx.ApiVersion
	}
	principal := rpcSubPrincipal(ctx)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		wsLog().Error("rpcsub: subscription requested after registry closed")
		return nil, types.RpcErrorInternal()
	}
	key, rpcErr := canonicalRPCSubURL(request.URL)
	if rpcErr != nil {
		r.mu.Unlock()
		return nil, rpcErr
	}
	lookup, rpcErr := r.findOrCreateLocked(request, key, principal)
	if rpcErr != nil {
		r.mu.Unlock()
		return nil, rpcErr
	}

	if rpcErr := r.ws.subscriptionManager.HandleSubscribeTransactional(lookup.sub.conn, request, true); rpcErr != nil {
		var stopped *rpcSub
		if lookup.created {
			r.removeLocked(key, lookup.sub)
			stopped = lookup.sub
		} else {
			lookup.sub.updateCredentials(lookup.username, true, lookup.password, true)
		}
		r.mu.Unlock()
		waitRPCSub(stopped)
		return nil, rpcErr
	}
	for _, book := range request.Books {
		setSubscriptionLoadCost(ctx, types.SubscriptionRequest{Books: []types.BookRequest{book}})
	}
	r.mu.Unlock()
	return r.ws.buildSubscribeAck(ctx, request), nil
}

// Unsubscribe removes the listed streams/accounts/books from the url's
// subscriber and drops the registry entry once no stream subscriptions
// remain. An unknown URL is a silent success.
func (r *URLSubscriptionRegistry) Unsubscribe(ctx *types.RpcContext, request types.SubscriptionRequest) (map[string]any, *types.RpcError) {
	key, rpcErr := canonicalRPCSubURL(request.URL)
	if rpcErr != nil {
		// Unsubscribe has always treated an unknown destination as a no-op.
		// Preserve that behavior for malformed or unsupported URLs too; valid
		// forms still pass through canonical lookup below so equivalent spellings
		// reach an existing subscription.
		return map[string]any{}, nil
	}
	r.mu.Lock()
	sub, ok := r.subs[key]
	if !ok {
		r.mu.Unlock()
		return map[string]any{}, nil
	}
	if rpcErr := r.ws.subscriptionManager.HandleUnsubscribeTransactional(sub.conn, request, true); rpcErr != nil {
		r.mu.Unlock()
		return nil, rpcErr
	}
	stopped := r.tryRemoveLocked(key)
	r.mu.Unlock()
	waitRPCSub(stopped)
	return map[string]any{}, nil
}

type rpcSubLookup struct {
	sub      *rpcSub
	created  bool
	username string
	password string
}

func (r *URLSubscriptionRegistry) findOrCreateLocked(request types.SubscriptionRequest, key, principal string) (rpcSubLookup, *types.RpcError) {
	if r.subs == nil {
		r.subs = make(map[string]*rpcSub)
	}
	if r.principalCounts == nil {
		r.principalCounts = make(map[string]int)
	}
	if r.principalWorkers == nil {
		r.principalWorkers = make(map[string]int)
	}
	username, password, usernameSet, passwordSet := request.URLCredentials()
	if sub, ok := r.subs[key]; ok {
		oldUsername, oldPassword := sub.credentials()
		// Credentials on an existing URL subscription are only updated via the
		// deprecated username/password members; url_username/url_password are
		// ignored on reuse, exactly like doSubscribe.
		sub.updateCredentials(username, usernameSet, password, passwordSet)
		return rpcSubLookup{sub: sub, username: oldUsername, password: oldPassword}, nil
	}
	if r.maxEntries > 0 && len(r.subs) >= r.maxEntries {
		return rpcSubLookup{}, r.capacityError("registry entries", principal)
	}
	if r.maxWorkers > 0 && r.workers >= r.maxWorkers {
		return rpcSubLookup{}, r.capacityError("delivery workers", principal)
	}
	if r.maxPerPrincipal > 0 && r.principalCounts[principal] >= r.maxPerPrincipal {
		return rpcSubLookup{}, r.capacityError("principal entries", principal)
	}
	if r.maxPerPrincipal > 0 && r.principalWorkers[principal] >= r.maxPerPrincipal {
		return rpcSubLookup{}, r.capacityError("principal workers", principal)
	}

	subCtx, subCancel := context.WithCancel(r.ctx) //nolint:gosec // The subscription owns and calls cancel during teardown.
	metrics := &r.metrics
	sub := &rpcSub{
		endpoint:  key,
		client:    r.client,
		ctx:       subCtx,
		cancel:    subCancel,
		registry:  r,
		principal: principal,
		metrics:   metrics,
		username:  username,
		password:  password,
		conn:      types.NewConnection("rpcsub:"+key, make(chan []byte, rpcSubQueueLimit)),
		done:      make(chan struct{}),
		finished:  make(chan struct{}),
	}
	sub.conn.EncodeOutbound = sub.encodeOutbound
	sub.conn.SendObserver = func(queued bool) {
		if queued {
			metrics.recordQueued(key)
		} else {
			metrics.recordDropped(key)
		}
	}
	r.subs[key] = sub
	r.principalCounts[principal]++
	r.principalWorkers[principal]++
	r.workers++
	r.workerWG.Add(1)
	r.ws.subscriptionManager.AddConnection(sub.conn)
	go sub.run()
	return rpcSubLookup{sub: sub, created: true}, nil
}

func (r *URLSubscriptionRegistry) capacityError(kind, principal string) *types.RpcError {
	value := r.metrics.capacityRejects.Add(1)
	if rpcSubMetricMilestone(value) {
		wsLog().Warn("rpcsub: subscription capacity exhausted", "kind", kind, "principal", principal, "count", value, "entries", len(r.subs), "workers", r.workers)
	}
	return types.RpcErrorTooBusy()
}

func (r *URLSubscriptionRegistry) tryRemoveLocked(key string) *rpcSub {
	sub, ok := r.subs[key]
	if !ok {
		return nil
	}
	if r.ws.subscriptionManager.HasStreamSubscriptions(sub.conn.ID) {
		return nil
	}
	return r.removeLocked(key, sub)
}

func (r *URLSubscriptionRegistry) removeLocked(key string, sub *rpcSub) *rpcSub {
	if current, ok := r.subs[key]; !ok || current != sub {
		return nil
	}
	delete(r.subs, key)
	if r.principalCounts[sub.principal] > 0 {
		r.principalCounts[sub.principal]--
		if r.principalCounts[sub.principal] == 0 {
			delete(r.principalCounts, sub.principal)
		}
	}
	r.ws.subscriptionManager.RemoveConnection(sub.conn.ID)
	sub.stop()
	return sub
}

func waitRPCSub(sub *rpcSub) {
	if sub != nil && sub.finished != nil {
		<-sub.finished
	}
}

// Close stops every url subscription, cancelling in-flight deliveries, and
// waits for the delivery goroutines to exit.
func (r *URLSubscriptionRegistry) Close() {
	r.mu.Lock()
	if r.closed {
		closeDone := r.closeDone
		r.mu.Unlock()
		<-closeDone
		return
	}
	r.closed = true
	subs := r.subs
	r.subs = make(map[string]*rpcSub)
	r.principalCounts = make(map[string]int)
	r.mu.Unlock()
	defer close(r.closeDone)

	r.cancel()
	for _, sub := range subs {
		r.ws.subscriptionManager.RemoveConnection(sub.conn.ID)
		sub.stop()
	}
	for _, sub := range subs {
		if sub.finished != nil {
			<-sub.finished
		}
	}
	r.workerWG.Wait()
}

// canonicalRPCSubURL validates and canonicalises a destination before it is
// used as a registry key or HTTP endpoint. Scheme/host casing and implicit
// default ports are equivalent; path and query remain part of the identity.
// HTTP basic-auth fields are mutable subscription state rather than identity,
// matching the reuse semantics above; URL userinfo is rejected. Fragments,
// opaque URLs, whitespace and empty hosts are also rejected because they do
// not identify an unambiguous HTTP destination.
func canonicalRPCSubURL(raw string) (string, *types.RpcError) {
	parseErr := types.RpcErrorInvalidParams("Failed to parse url.")
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", parseErr
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return "", parseErr
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", types.RpcErrorInvalidParams("Only http and https is supported.")
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return "", parseErr
	}
	if ip := net.ParseIP(hostname); ip != nil {
		hostname = ip.String()
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", parseErr
		}
		port = strconv.Itoa(n)
	}
	requestURI := u.RequestURI()
	if requestURI == "/" && u.RawQuery == "" && !u.ForceQuery {
		requestURI = ""
	}
	return scheme + "://" + net.JoinHostPort(hostname, port) + requestURI, nil
}

func rpcSubPrincipal(ctx *types.RpcContext) string {
	if ctx == nil {
		return "unknown"
	}
	if principal := strings.TrimSpace(ctx.ClientIP); principal != "" {
		return principal
	}
	return "unknown"
}

// rpcSub is one url subscription: a subscription-manager connection whose
// send channel is drained by a delivery goroutine POSTing each event to the
// url, one at a time and in order, like RPCSub::sendThread.
type rpcSub struct {
	endpoint  string
	client    *http.Client
	ctx       context.Context
	cancel    context.CancelFunc
	registry  *URLSubscriptionRegistry
	principal string
	metrics   *rpcSubMetrics
	conn      *types.Connection
	done      chan struct{}
	finished  chan struct{}
	stopOnce  sync.Once

	credMu   sync.Mutex
	username string
	password string

	// seq numbers events per url, starting at 1 (RPCSub::mSeq). Stamped
	// at enqueue, so a queue-limit drop still consumes a number and the
	// remote sees a gap. Accessed from the broadcaster goroutine via
	// encodeOutbound, hence atomic.
	seq atomic.Uint64
}

// encodeOutbound stamps the next per-url sequence number into a broadcast
// event before it is queued, mirroring rippled's mSeq++ at enqueue
// (RPCSub::send). An undecodable event is queued unchanged so the delivery
// goroutine logs and drops it. Returns a fresh slice — the input is shared
// across subscribers and must not be mutated.
func (s *rpcSub) encodeOutbound(data []byte) []byte {
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		return data
	}
	event["seq"] = s.seq.Add(1)
	body, err := json.Marshal(map[string]any{
		"method": "event",
		"params": event,
		"id":     1,
	})
	if err != nil {
		return data
	}
	return append(body, '\n')
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
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.done != nil {
			close(s.done)
		}
	})
}

func (s *rpcSub) run() {
	defer func() {
		if s.registry != nil {
			s.registry.workerStopped(s.principal)
		}
		if s.finished != nil {
			close(s.finished)
		}
		if s.registry != nil {
			s.registry.workerWG.Done()
		}
	}()
	for {
		select {
		case <-s.done:
			return
		default:
		}
		select {
		case data := <-s.conn.Outbound():
			s.deliver(data)
		case <-s.done:
			return
		}
	}
}

func (r *URLSubscriptionRegistry) workerStopped(principal string) {
	r.mu.Lock()
	if r.workers > 0 {
		r.workers--
	}
	if r.principalWorkers[principal] > 0 {
		r.principalWorkers[principal]--
		if r.principalWorkers[principal] == 0 {
			delete(r.principalWorkers, principal)
		}
	}
	r.mu.Unlock()
}

// deliver POSTs one already-encoded event — the JSON-RPC call rippled's
// RPCSub emits, {"method":"event","params":{...,"seq":N},"id":1}, framed
// by encodeOutbound at enqueue. Failures are logged and dropped
// (fire-and-forget), like sendThread's catch-and-log around
// RPCCall::fromNetwork.
func (s *rpcSub) deliver(body []byte) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		s.recordDeliveryFailure("request_build", "err", err)
		wsLog().Error("rpcsub: request build failed", "url", s.endpoint, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// rippled posts with this fixed User-Agent (RPCCall::createHTTPPost).
	req.Header.Set("User-Agent", "ripple-json-rpc/v1")
	// rippled always sends basic auth, even with empty credentials.
	username, password := s.credentials()
	req.SetBasicAuth(username, password)

	resp, err := s.client.Do(req)
	if err != nil {
		if s.ctx.Err() == nil {
			s.recordDeliveryFailure("request", "err", err)
			wsLog().Debug("rpcsub: event delivery failed", "url", s.endpoint, "err", err)
		}
		return
	}
	failed := resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices
	if failed {
		s.recordDeliveryFailure("status", "status", resp.StatusCode)
		wsLog().Debug("rpcsub: event delivery returned failure", "url", s.endpoint, "status", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		if !failed {
			s.recordDeliveryFailure("response_read", "err", err)
		}
		wsLog().Debug("rpcsub: event response read failed", "url", s.endpoint, "err", err)
	}
	_ = resp.Body.Close()
}

func (s *rpcSub) recordDeliveryFailure(reason string, details ...any) {
	if s.metrics != nil {
		s.metrics.recordDeliveryFailure(s.endpoint, reason, details...)
	}
}
