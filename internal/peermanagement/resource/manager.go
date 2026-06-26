package resource

import (
	"log/slog"
	"net"
	"sync"
	"time"
)

// Clock is the time source for the Manager — time.Now in production,
// a fake in tests.
type Clock func() time.Time

// key identifies an Entry. Endpoints carry a port for outbound (the
// port distinguishes peers behind a NAT making multiple outbound
// connections) and are normalized to port 0 for inbound (so a client
// that reconnects on a fresh ephemeral port inherits its prior
// balance).
type key struct {
	kind Kind
	addr string
}

// entry is one consumer's bookkeeping inside the Manager. refcount
// reflects how many Consumer handles point at this entry; when it
// drops to zero the entry is moved to the inactive list and aged out
// by periodicActivity after SecondsUntilExpiration.
type entry struct {
	k             key
	refcount      int
	localBalance  decayingSample
	remoteBalance int
	lastWarning   time.Time
	whenExpires   time.Time
	active        bool
}

func (e *entry) balance(now time.Time) int {
	return e.localBalance.valueAt(now) + e.remoteBalance
}

func (e *entry) add(charge int, now time.Time) int {
	return e.localBalance.add(charge, now) + e.remoteBalance
}

func (e *entry) isUnlimited() bool { return e.k.kind == KindUnlimited }

// importRecord tracks an applied gossip snapshot so its contribution
// can be subtracted when the next snapshot from the same origin
// arrives, or when it expires.
type importRecord struct {
	whenExpires time.Time
	items       []importItem
}

type importItem struct {
	consumer *Consumer
	balance  int
}

// Manager owns the per-endpoint consumer table and is the only type
// outside this package callers need to construct. New a Manager once
// at process startup, mint a Consumer per peer, and call Charge on the
// Consumer as the peer does work.
type Manager struct {
	mu sync.Mutex

	clock   Clock
	journal *slog.Logger

	entries map[key]*entry
	imports map[string]*importRecord

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewManager returns a Manager that uses now() for its clock. If clock
// is nil, time.Now is used. journal may be nil — internal events are
// then logged at debug via the default slog handler.
func NewManager(clock Clock, journal *slog.Logger) *Manager {
	if clock == nil {
		clock = time.Now
	}
	if journal == nil {
		journal = slog.Default()
	}
	return &Manager{
		clock:   clock,
		journal: journal,
		entries: make(map[key]*entry),
		imports: make(map[string]*importRecord),
	}
}

// Start launches the periodic-activity goroutine. Idempotent; safe to
// skip in tests that drive PeriodicActivity manually. Stop must be
// called for clean shutdown.
func (m *Manager) Start() {
	m.mu.Lock()
	if m.stop != nil {
		m.mu.Unlock()
		return
	}
	m.stop = make(chan struct{})
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run()
}

// Stop signals the periodic-activity goroutine to exit and blocks until it has.
func (m *Manager) Stop() {
	m.mu.Lock()
	stop := m.stop
	m.stop = nil
	m.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	m.wg.Wait()
}

func (m *Manager) run() {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		stop := m.stop
		m.mu.Unlock()
		if stop == nil {
			return
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.PeriodicActivity()
		}
	}
}

// NewInboundEndpoint mints (or reattaches) a Consumer for an inbound
// peer. The port is dropped from the key so a client that reconnects
// from a fresh ephemeral port inherits its prior balance — without
// this, a misbehaving peer could bypass the blacklist by reconnecting.
func (m *Manager) NewInboundEndpoint(addr string) *Consumer {
	return m.acquire(KindInbound, normalizeAddr(addr))
}

// NewOutboundEndpoint mints (or reattaches) a Consumer for an outbound
// peer. The full address (host:port) is retained because outbound
// connections are configured per-target.
func (m *Manager) NewOutboundEndpoint(addr string) *Consumer {
	return m.acquire(KindOutbound, addr)
}

// NewUnlimitedEndpoint mints a Consumer that will never reach Drop.
// Charges on an unlimited Consumer are no-ops — local balance stays
// at zero and the Manager's charge / warn / disconnect entries are
// never touched. Used for cluster members and admin sources. The key
// is canonicalised to port 1 so an admin endpoint never shares a key
// — or a black_list output address — with the port-0 inbound entry
// for the same host.
func (m *Manager) NewUnlimitedEndpoint(addr string) *Consumer {
	return m.acquire(KindUnlimited, adminAddr(addr))
}

// normalizeAddr canonicalises an inbound endpoint key by dropping the
// numeric port so a peer that reconnects on a fresh ephemeral port
// inherits its prior balance — without this, the blacklist would be
// trivially defeated. Uses net.SplitHostPort so IPv6 brackets and
// bare addresses are handled correctly; falls back to the input
// verbatim when there is no port to strip.
func normalizeAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// adminAddr canonicalises an unlimited/admin endpoint key to port 1.
// The distinct port keeps admin entries from colliding with the
// port-0 inbound key for the same host, both internally and in the
// address-keyed black_list output.
func adminAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return net.JoinHostPort(host, "1")
}

func (m *Manager) acquire(k Kind, addr string) *Consumer {
	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()

	ek := key{kind: k, addr: addr}
	e, ok := m.entries[ek]
	if !ok {
		e = &entry{
			k:            ek,
			localBalance: newDecayingSample(now, DecayWindowSeconds),
		}
		m.entries[ek] = e
	}
	e.refcount++
	e.active = true
	// Re-acquiring an entry that was previously inactive clears the
	// stale expiry — periodicActivity only erases entries with
	// refcount==0 so the field is unobservable while active, but
	// keeping it zero is a hygiene win.
	e.whenExpires = time.Time{}
	return &Consumer{m: m, e: e}
}

// release drops a Consumer's reference. When refcount hits zero the
// entry is marked inactive and its expiration timestamp is set; the
// entry itself stays in the table so a reconnect inherits its balance.
// periodicActivity will erase it after SecondsUntilExpiration.
func (m *Manager) release(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseLocked(e)
}

// releaseLocked is the body of release for callers that already hold
// m.mu (the gossip import-expiry and import-replacement paths). It must
// drive every refcount decrement so an entry that hits zero is marked
// inactive and given an expiry timestamp; a raw refcount-- would strand
// a zero-balance entry in the table forever, since periodicActivity only
// erases entries whose whenExpires is set.
func (m *Manager) releaseLocked(e *entry) {
	if e.refcount == 0 {
		return
	}
	e.refcount--
	if e.refcount == 0 {
		e.active = false
		e.whenExpires = m.clock().Add(SecondsUntilExpiration)
	}
}

// charge applies fee to entry e and returns the resulting disposition.
// Unlimited entries short-circuit at the Consumer boundary (see
// Consumer.Charge); the defensive check here keeps the invariant
// "unlimited local_balance stays at zero" even if a future caller
// reaches this method directly.
func (m *Manager) charge(e *entry, fee Charge, context string) Disposition {
	if e.isUnlimited() {
		return Ok
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	bal := e.add(fee.Cost(), now)
	if context == "" {
		m.journal.Debug("resource charge", "endpoint", e.k.addr, "fee", fee.String(), "balance", bal)
	} else {
		m.journal.Debug("resource charge", "endpoint", e.k.addr, "fee", fee.String(), "balance", bal, "context", context)
	}
	return disposition(bal)
}

func disposition(balance int) Disposition {
	switch {
	case balance >= DropThreshold:
		return Drop
	case balance >= WarningThreshold:
		return Warn
	default:
		return Ok
	}
}

// warn issues a warning charge if the consumer has crossed the
// warning threshold and not been warned in the last second. Returns
// true if a warning was issued. Unlimited consumers never warn.
//
// The rate-limit is integer-second granularity: rippled's
// second-resolution clock makes its warning gate fire at most once
// per second. Go's wall clock is nanosecond-precision, so comparing
// equal time.Time values would collapse the limit; truncating to the
// second restores parity.
func (m *Manager) warn(e *entry) bool {
	if e.isUnlimited() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	nowSec := now.Truncate(time.Second)
	if e.balance(now) >= WarningThreshold && !nowSec.Equal(e.lastWarning) {
		_ = e.add(FeeWarning.Cost(), now)
		e.lastWarning = nowSec
		m.journal.Info("resource load warning", "endpoint", e.k.addr)
		return true
	}
	return false
}

// disconnect tests whether a consumer's balance has reached the drop
// threshold and, if so, applies a feeDrop penalty so an immediate
// reconnect from the same endpoint stays blacklisted for a while.
// Returns true when the caller should disconnect. Unlimited consumers
// never disconnect.
func (m *Manager) disconnect(e *entry) bool {
	if e.isUnlimited() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	if e.balance(now) >= DropThreshold {
		_ = e.add(FeeDrop.Cost(), now)
		m.journal.Warn("resource consumer dropped",
			"endpoint", e.k.addr, "balance", e.balance(now), "threshold", DropThreshold)
		return true
	}
	return false
}

func (m *Manager) balance(e *entry) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return e.balance(m.clock())
}

// PeriodicActivity expires inactive entries and import records. Called
// once per second by the goroutine launched by Start, and exposed for
// tests that need deterministic stepping.
func (m *Manager) PeriodicActivity() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()

	for k, e := range m.entries {
		if e.refcount == 0 && !e.whenExpires.IsZero() && !now.Before(e.whenExpires) {
			delete(m.entries, k)
		}
	}

	for origin, rec := range m.imports {
		if !now.Before(rec.whenExpires) {
			for _, it := range rec.items {
				it.consumer.e.remoteBalance -= it.balance
				m.releaseLocked(it.consumer.e)
			}
			delete(m.imports, origin)
		}
	}
}

// ExportConsumers returns a Gossip snapshot of every inbound consumer
// whose balance is at or above MinimumGossipBalance.
func (m *Manager) ExportConsumers() Gossip {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	var g Gossip
	for _, e := range m.entries {
		if e.k.kind != KindInbound {
			continue
		}
		bal := e.localBalance.valueAt(now)
		if bal >= MinimumGossipBalance {
			g.Items = append(g.Items, GossipItem{Address: e.k.addr, Balance: bal})
		}
	}
	return g
}

// ImportConsumers absorbs a peer's Gossip snapshot. A subsequent import
// from the same origin replaces the prior contribution: each item's
// previous remote balance is subtracted and the new one added.
func (m *Manager) ImportConsumers(origin string, g Gossip) {
	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()

	prev := m.imports[origin]
	next := &importRecord{whenExpires: now.Add(GossipExpiration)}
	for _, it := range g.Items {
		ek := key{kind: KindInbound, addr: normalizeAddr(it.Address)}
		e, ok := m.entries[ek]
		if !ok {
			e = &entry{k: ek, localBalance: newDecayingSample(now, DecayWindowSeconds)}
			m.entries[ek] = e
		}
		// Keep entry resident so the imported balance is observable
		// even when no local Consumer references it.
		e.refcount++
		e.remoteBalance += it.Balance
		next.items = append(next.items, importItem{
			consumer: &Consumer{m: m, e: e},
			balance:  it.Balance,
		})
	}
	if prev != nil {
		for _, it := range prev.items {
			it.consumer.e.remoteBalance -= it.balance
			// Mirror the +1 we did when prev was created, via release
			// semantics so an entry that drops to zero gets an expiry
			// timestamp instead of lingering forever.
			m.releaseLocked(it.consumer.e)
		}
	}
	m.imports[origin] = next
}

// EntryCount returns the number of tracked entries. Test-only.
func (m *Manager) EntryCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
