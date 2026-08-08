package resource

import (
	"container/heap"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"sort"
	"sync"
	"time"
)

type Clock func() time.Time

type key struct {
	kind Kind
	addr string
}

type entry struct {
	k             key
	localRefs     int
	importRefs    int
	inflight      int
	reservedCost  int64
	localBalance  decayingSample
	remoteBalance int64
	lastWarning   time.Time
	warningSet    bool
	expiry        *expiryItem
}

func (e *entry) balance(now time.Time) int64 {
	return saturatingAdd(e.localBalance.valueAt(now), e.remoteBalance)
}

func (e *entry) add(charge int64, now time.Time) int64 {
	return saturatingAdd(e.localBalance.add(charge, now), e.remoteBalance)
}

func (e *entry) isUnlimited() bool { return e.k.kind == KindUnlimited }

type importRecord struct {
	items  []importItem
	expiry *expiryItem
}

type importItem struct {
	entry   *entry
	balance int64
}

type Manager struct {
	mu sync.Mutex

	clock   Clock
	journal *slog.Logger
	limits  Limits

	entries  map[key]*entry
	imports  map[string]*importRecord
	expiries expiryQueue
	inflight int
	stats    counters

	stop     chan struct{}
	stopped  bool
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func NewManager(clock Clock, journal *slog.Logger) *Manager {
	return NewManagerWithLimits(clock, journal, DefaultLimits())
}

func NewManagerWithLimits(clock Clock, journal *slog.Logger, limits Limits) *Manager {
	if clock == nil {
		clock = time.Now
	}
	if journal == nil {
		journal = slog.Default()
	}
	m := &Manager{
		clock:   clock,
		journal: journal,
		limits:  limits.withDefaults(),
		entries: make(map[key]*entry),
		imports: make(map[string]*importRecord),
	}
	heap.Init(&m.expiries)
	return m
}

func (m *Manager) Start() {
	m.mu.Lock()
	if m.stopped || m.stop != nil {
		m.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	m.stop = stop
	m.wg.Add(1)
	m.mu.Unlock()

	go m.run(stop)
}

func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.stopped = true
		stop := m.stop
		m.mu.Unlock()
		if stop != nil {
			close(stop)
		}
		m.wg.Wait()
	})
}

func (m *Manager) run(stop <-chan struct{}) {
	defer m.wg.Done()
	m.PeriodicActivity()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.PeriodicActivity()
		}
	}
}

func (m *Manager) NewInboundEndpoint(addr string) *Consumer {
	return m.acquire(KindInbound, addr)
}

func (m *Manager) NewOutboundEndpoint(addr string) *Consumer {
	return m.acquire(KindOutbound, addr)
}

func (m *Manager) NewUnlimitedEndpoint(addr string) *Consumer {
	return m.acquire(KindUnlimited, addr)
}

func (m *Manager) acquire(kind Kind, raw string) *Consumer {
	addr, ok := canonicalEndpoint(kind, raw, m.limits.MaxEndpointLength)
	if !ok {
		return nil
	}
	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()

	ek := key{kind: kind, addr: addr}
	e := m.entries[ek]
	if e == nil {
		m.expireLocked(now, 1)
		if len(m.entries) >= m.limits.MaxEntries {
			m.stats.entryCapRejections++
			return nil
		}
		e = &entry{k: ek, localBalance: newDecayingSample(now, DecayWindowSeconds)}
		m.entries[ek] = e
	}
	m.cancelEntryExpiryLocked(e)
	e.localRefs++
	return &Consumer{m: m, e: e}
}

func canonicalEndpoint(kind Kind, raw string, maxLength int) (string, bool) {
	if raw == "" || len(raw) > maxLength {
		return "", false
	}
	if kind == KindOutbound {
		endpoint, err := netip.ParseAddrPort(raw)
		if err != nil {
			return "", false
		}
		return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port()).String(), true
	}

	var addr netip.Addr
	if endpoint, err := netip.ParseAddrPort(raw); err == nil {
		addr = endpoint.Addr()
	} else {
		var parseErr error
		addr, parseErr = netip.ParseAddr(raw)
		if parseErr != nil {
			return "", false
		}
	}
	addr = addr.Unmap()
	if kind == KindUnlimited {
		return netip.AddrPortFrom(addr, 1).String(), true
	}
	return addr.String(), true
}

func (m *Manager) release(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseLocalLocked(e, m.clock())
}

func (m *Manager) releaseLocalLocked(e *entry, now time.Time) {
	if e.localRefs == 0 {
		return
	}
	e.localRefs--
	m.scheduleIfInactiveLocked(e, now)
}

func (m *Manager) scheduleIfInactiveLocked(e *entry, now time.Time) {
	if e.localRefs == 0 && e.importRefs == 0 {
		m.scheduleEntryExpiryLocked(e, now.Add(SecondsUntilExpiration))
	}
}

func (m *Manager) charge(e *entry, fee Charge, context string) Disposition {
	if e.isUnlimited() {
		return Ok
	}
	m.mu.Lock()
	now := m.clock()
	balance := e.add(int64(fee.Cost()), now)
	result := disposition(balance)
	endpoint := e.k.addr
	m.mu.Unlock()

	if context == "" {
		m.journal.Debug("resource charge", "endpoint", endpoint, "fee", fee.String(), "balance", balance)
	} else {
		m.journal.Debug("resource charge", "endpoint", endpoint, "fee", fee.String(), "balance", balance, "context", context)
	}
	return result
}

func disposition(balance int64) Disposition {
	switch {
	case balance >= DropThreshold:
		return Drop
	case balance >= WarningThreshold:
		return Warn
	default:
		return Ok
	}
}

func (m *Manager) warn(e *entry) bool {
	if e.isUnlimited() {
		return false
	}
	m.mu.Lock()
	now := m.clock()
	if e.balance(now) < WarningThreshold || e.warningSet && now.Sub(e.lastWarning) < time.Second {
		m.mu.Unlock()
		return false
	}
	_ = e.add(int64(FeeWarning.Cost()), now)
	e.lastWarning = now
	e.warningSet = true
	m.stats.warnings++
	endpoint := e.k.addr
	m.mu.Unlock()
	m.journal.Info("resource load warning", "endpoint", endpoint)
	return true
}

func (m *Manager) disconnect(e *entry) bool {
	if e.isUnlimited() {
		return false
	}
	m.mu.Lock()
	now := m.clock()
	if e.balance(now) < DropThreshold {
		m.mu.Unlock()
		return false
	}
	balance := e.add(int64(FeeDrop.Cost()), now)
	m.stats.drops++
	endpoint := e.k.addr
	m.mu.Unlock()
	m.journal.Warn("resource consumer dropped", "endpoint", endpoint, "balance", balance, "threshold", DropThreshold)
	return true
}

func (m *Manager) balance(e *entry) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return e.balance(m.clock())
}

func (m *Manager) PeriodicActivity() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(m.clock(), m.limits.MaxCleanupPerTick)
}

func (m *Manager) ExportConsumers() Gossip {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	items := make([]GossipItem, 0)
	for _, e := range m.entries {
		if e.k.kind != KindInbound || e.localRefs == 0 {
			continue
		}
		balance := e.localBalance.valueAt(now)
		if balance < MinimumGossipBalance {
			continue
		}
		items = append(items, GossipItem{Address: e.k.addr, Balance: uint32(min(balance, math.MaxUint32))})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Balance != items[j].Balance {
			return items[i].Balance > items[j].Balance
		}
		return items[i].Address < items[j].Address
	})
	if len(items) > m.limits.MaxGossipItems {
		items = items[:m.limits.MaxGossipItems]
	}
	return Gossip{Items: items}
}

func (m *Manager) ImportConsumers(origin string, gossip Gossip) error {
	items, err := m.validateGossip(origin, gossip)
	if err != nil {
		m.mu.Lock()
		m.stats.importRejections++
		m.mu.Unlock()
		return err
	}

	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(now, m.limits.MaxCleanupPerTick)
	previous := m.imports[origin]
	if len(items) == 0 {
		if previous != nil {
			m.removeImportLocked(origin, previous, now)
		}
		return nil
	}
	if previous == nil && len(m.imports) >= m.limits.MaxImports {
		m.stats.importRejections++
		return fmt.Errorf("resource gossip origin cap reached")
	}

	newEntries := 0
	for _, item := range items {
		if m.entries[key{kind: KindInbound, addr: item.Address}] == nil {
			newEntries++
		}
	}
	if len(m.entries)+newEntries > m.limits.MaxEntries {
		m.stats.importRejections++
		m.stats.entryCapRejections++
		return fmt.Errorf("resource entry cap reached")
	}

	next := &importRecord{}
	for _, item := range items {
		ek := key{kind: KindInbound, addr: item.Address}
		e := m.entries[ek]
		if e == nil {
			e = &entry{k: ek, localBalance: newDecayingSample(now, DecayWindowSeconds)}
			m.entries[ek] = e
		}
		m.cancelEntryExpiryLocked(e)
		e.importRefs++
		e.remoteBalance = saturatingAdd(e.remoteBalance, item.Balance)
		next.items = append(next.items, importItem{entry: e, balance: item.Balance})
	}
	if previous != nil {
		m.removeImportLocked(origin, previous, now)
	}
	next.expiry = &expiryItem{when: now.Add(GossipExpiration), kind: expireImport, origin: origin, index: -1}
	heap.Push(&m.expiries, next.expiry)
	m.imports[origin] = next
	return nil
}

type validatedGossipItem struct {
	Address string
	Balance int64
}

func (m *Manager) validateGossip(origin string, gossip Gossip) ([]validatedGossipItem, error) {
	if len(origin) > m.limits.MaxOriginLength {
		return nil, fmt.Errorf("resource gossip origin exceeds %d bytes", m.limits.MaxOriginLength)
	}
	if len(gossip.Items) > m.limits.MaxGossipItems {
		return nil, fmt.Errorf("resource gossip has %d items, limit %d", len(gossip.Items), m.limits.MaxGossipItems)
	}
	dedup := make(map[string]uint64, len(gossip.Items))
	for _, item := range gossip.Items {
		if item.Balance == 0 {
			continue
		}
		addr, ok := canonicalEndpoint(KindInbound, item.Address, m.limits.MaxEndpointLength)
		if !ok {
			return nil, fmt.Errorf("invalid resource gossip endpoint %q", item.Address)
		}
		total := dedup[addr] + uint64(item.Balance)
		if total > math.MaxUint32 {
			total = math.MaxUint32
		}
		dedup[addr] = total
	}
	keys := make([]string, 0, len(dedup))
	for addr := range dedup {
		keys = append(keys, addr)
	}
	sort.Strings(keys)
	items := make([]validatedGossipItem, 0, len(keys))
	for _, addr := range keys {
		items = append(items, validatedGossipItem{Address: addr, Balance: int64(dedup[addr])})
	}
	return items, nil
}

func (m *Manager) removeImportLocked(origin string, rec *importRecord, now time.Time) {
	if rec.expiry != nil && rec.expiry.index >= 0 {
		heap.Remove(&m.expiries, rec.expiry.index)
	}
	for _, item := range rec.items {
		item.entry.remoteBalance -= item.balance
		if item.entry.remoteBalance < 0 {
			item.entry.remoteBalance = 0
		}
		if item.entry.importRefs > 0 {
			item.entry.importRefs--
		}
		m.scheduleIfInactiveLocked(item.entry, now)
	}
	delete(m.imports, origin)
}

func (m *Manager) EntryCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func (m *Manager) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Stats{
		Entries:            len(m.entries),
		Imports:            len(m.imports),
		Inflight:           m.inflight,
		Evictions:          m.stats.evictions,
		EntryCapRejections: m.stats.entryCapRejections,
		ImportRejections:   m.stats.importRejections,
		InflightRejections: m.stats.inflightRejections,
		Warnings:           m.stats.warnings,
		Drops:              m.stats.drops,
	}
}

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}
