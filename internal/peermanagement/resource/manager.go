package resource

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Clock func() time.Time

type Option func(*Limits)

func WithLimits(limits Limits) Option {
	return func(configured *Limits) {
		*configured = limits
	}
}

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
	publicKey     string
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

func (e *entry) fingerprint() string {
	fingerprint := "IP Address: " + e.k.addr
	if e.publicKey != "" {
		fingerprint += ", Public Key: " + e.publicKey
	}
	return fingerprint
}

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

	entries         map[key]*entry
	imports         map[string]*importRecord
	expiries        expiryQueue
	inflight        int
	retainedEntries int
	importedEntries int
	importItems     int
	stats           counters

	stop     chan struct{}
	stopped  bool
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func NewManager(clock Clock, journal *slog.Logger, options ...Option) *Manager {
	limits := DefaultLimits()
	for _, option := range options {
		if option != nil {
			option(&limits)
		}
	}
	return NewManagerWithLimits(clock, journal, limits)
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
		evictions, ok := m.planEntryEvictionsLocked(1, now, map[key]struct{}{ek: {}}, nil)
		if !ok {
			m.stats.entryCapRejections++
			return nil
		}
		m.evictEntriesLocked(evictions)
		e = &entry{k: ek, localBalance: newDecayingSample(now, DecayWindowSeconds)}
		m.entries[ek] = e
	}
	if e.localRefs == 0 && e.importRefs == 0 && e.expiry != nil {
		m.retainedEntries--
	}
	m.cancelEntryExpiryLocked(e)
	e.localRefs++
	return &Consumer{state: &consumerState{m: m, e: e}}
}

func canonicalEndpoint(kind Kind, raw string, maxLength int) (string, bool) {
	if len(raw) > maxLength {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if kind == KindOutbound {
		if endpoint, err := netip.ParseAddrPort(raw); err == nil {
			return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port()).String(), true
		}
		host, portText, err := net.SplitHostPort(raw)
		if err != nil || strings.TrimSpace(host) != host || host == "" {
			return "", false
		}
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			return "", false
		}
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if host == "" {
			return "", false
		}
		endpoint := net.JoinHostPort(host, strconv.FormatUint(port, 10))
		return endpoint, len(endpoint) <= maxLength
	}

	var addr netip.Addr
	if endpoint, err := netip.ParseAddrPort(raw); err == nil {
		addr = endpoint.Addr()
	} else {
		var parseErr error
		addr, parseErr = netip.ParseAddr(raw)
		if parseErr != nil && strings.HasSuffix(raw, ":") {
			addr, parseErr = netip.ParseAddr(strings.TrimSuffix(raw, ":"))
		}
		if parseErr != nil {
			return "", false
		}
	}
	if addr.Zone() != "" {
		return "", false
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
	if e.localRefs == 0 && e.importRefs == 0 && e.expiry == nil {
		m.retainedEntries++
		m.scheduleEntryExpiryLocked(e, now.Add(SecondsUntilExpiration))
	}
}

type projectedRelease struct {
	refs    int
	balance int64
}

func (m *Manager) planEntryEvictionsLocked(
	needed int,
	now time.Time,
	protected map[key]struct{},
	releases map[*entry]projectedRelease,
) ([]*entry, bool) {
	required := len(m.entries) + needed - m.limits.MaxEntries
	if required <= 0 {
		return nil, true
	}

	candidates := make([]*entry, 0, m.retainedEntries+len(releases))
	for _, e := range m.entries {
		if _, keep := protected[e.k]; keep {
			continue
		}
		refs := e.localRefs + e.importRefs
		balance := e.balance(now)
		if release, ok := releases[e]; ok {
			refs -= release.refs
			balance = max(0, balance-release.balance)
		}
		if refs == 0 && e.inflight == 0 && balance < WarningThreshold {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) < required {
		return nil, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftExpiry := now.Add(SecondsUntilExpiration)
		if candidates[i].expiry != nil {
			leftExpiry = candidates[i].expiry.when
		}
		rightExpiry := now.Add(SecondsUntilExpiration)
		if candidates[j].expiry != nil {
			rightExpiry = candidates[j].expiry.when
		}
		if !leftExpiry.Equal(rightExpiry) {
			return leftExpiry.Before(rightExpiry)
		}
		if candidates[i].k.kind != candidates[j].k.kind {
			return candidates[i].k.kind < candidates[j].k.kind
		}
		return candidates[i].k.addr < candidates[j].k.addr
	})
	return candidates[:required], true
}

func (m *Manager) evictEntriesLocked(entries []*entry) {
	for _, e := range entries {
		if m.entries[e.k] != e || e.localRefs != 0 || e.importRefs != 0 || e.inflight != 0 {
			panic("resource: invalid eviction plan")
		}
		if e.expiry != nil {
			m.cancelEntryExpiryLocked(e)
			m.retainedEntries--
		}
		delete(m.entries, e.k)
		m.stats.evictions++
	}
}

func (m *Manager) charge(e *entry, fee Charge, chargeContext string) Disposition {
	if e.isUnlimited() {
		return Ok
	}
	logContext := context.Background()
	level := chargeLevel(fee.Cost())
	logEnabled := m.journal.Enabled(logContext, level)
	m.mu.Lock()
	now := m.clock()
	balance := e.add(int64(fee.Cost()), now)
	result := disposition(balance)
	var endpoint string
	if logEnabled {
		endpoint = e.fingerprint()
	}
	m.mu.Unlock()
	if logEnabled {
		m.logCharge(logContext, level, endpoint, fee, balance, chargeContext)
	}
	return result
}

const resourceTraceLevel = slog.LevelDebug - 4

func chargeLevel(cost int) slog.Level {
	switch {
	case cost >= 3000:
		return slog.LevelWarn
	case cost >= 1000:
		return slog.LevelInfo
	case cost >= 100:
		return slog.LevelDebug
	default:
		return resourceTraceLevel
	}
}

func (m *Manager) logCharge(ctx context.Context, level slog.Level, endpoint string, fee Charge, balance int64, chargeContext string) {
	attrs := []slog.Attr{
		slog.String("endpoint", endpoint),
		slog.String("fee", fee.String()),
		slog.Int64("balance", balance),
	}
	if chargeContext != "" {
		attrs = append(attrs, slog.String("context", chargeContext))
	}
	m.journal.LogAttrs(ctx, level, "resource charge", attrs...)
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
	ctx := context.Background()
	logEnabled := m.journal.Enabled(ctx, slog.LevelInfo)
	m.mu.Lock()
	now := m.clock()
	if e.balance(now) < WarningThreshold || e.warningSet && now.Sub(e.lastWarning) < time.Second {
		m.mu.Unlock()
		return false
	}
	_ = e.add(int64(FeeWarning().Cost()), now)
	e.lastWarning = now
	e.warningSet = true
	m.stats.warnings++
	var endpoint string
	if logEnabled {
		endpoint = e.fingerprint()
	}
	m.mu.Unlock()
	if logEnabled {
		m.journal.LogAttrs(ctx, slog.LevelInfo, "resource load warning", slog.String("endpoint", endpoint))
	}
	return true
}

func (m *Manager) disconnect(e *entry) bool {
	if e.isUnlimited() {
		return false
	}
	ctx := context.Background()
	logEnabled := m.journal.Enabled(ctx, slog.LevelWarn)
	m.mu.Lock()
	now := m.clock()
	if e.balance(now) < DropThreshold {
		m.mu.Unlock()
		return false
	}
	balance := e.add(int64(FeeDrop().Cost()), now)
	m.stats.drops++
	var endpoint string
	if logEnabled {
		endpoint = e.fingerprint()
	}
	m.mu.Unlock()
	if logEnabled {
		m.journal.LogAttrs(ctx, slog.LevelWarn, "resource consumer dropped",
			slog.String("endpoint", endpoint), slog.Int64("balance", balance), slog.Int("threshold", DropThreshold))
	}
	return true
}

func (m *Manager) balance(e *entry) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return e.balance(m.clock())
}

func (m *Manager) setPublicKey(e *entry, publicKey string) {
	m.mu.Lock()
	e.publicKey = publicKey
	m.mu.Unlock()
}

func (m *Manager) PeriodicActivity() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(m.clock(), m.limits.MaxCleanupPerTick)
}

func (m *Manager) periodicActivity() {
	m.PeriodicActivity()
}

func (m *Manager) ExportConsumers() Gossip {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	items := make([]GossipItem, 0)
	for _, e := range m.entries {
		if e.k.kind != KindInbound || e.localRefs == 0 && e.importRefs == 0 {
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
		m.logImportRejection(origin, err)
		return err
	}

	now := m.clock()
	m.mu.Lock()
	m.expireLocked(now, m.limits.MaxCleanupPerTick)
	previous := m.imports[origin]
	if len(items) == 0 {
		if previous != nil {
			m.removeImportLocked(origin, previous, now)
		}
		m.mu.Unlock()
		return nil
	}
	if previous == nil && len(m.imports) >= m.limits.MaxImports {
		m.stats.importRejections++
		m.mu.Unlock()
		m.logImportRejection(origin, ErrImportOriginLimit)
		return ErrImportOriginLimit
	}

	nextSet := make(map[*entry]struct{}, len(items))
	releases := make(map[*entry]projectedRelease)
	newImported := 0
	newEntries := 0
	for _, item := range items {
		e := m.entries[key{kind: KindInbound, addr: item.Address}]
		if e == nil {
			newEntries++
			newImported++
			continue
		}
		nextSet[e] = struct{}{}
		if e.importRefs == 0 {
			newImported++
		}
	}
	releasedImported := 0
	if previous != nil {
		for _, item := range previous.items {
			release := releases[item.entry]
			release.refs++
			release.balance = saturatingAdd(release.balance, item.balance)
			releases[item.entry] = release
			if _, retained := nextSet[item.entry]; !retained && item.entry.importRefs == 1 {
				releasedImported++
			}
		}
	}
	if m.importedEntries+newImported-releasedImported > m.limits.MaxImportedEntries {
		m.stats.importRejections++
		m.mu.Unlock()
		m.logImportRejection(origin, ErrImportedEntryLimit)
		return ErrImportedEntryLimit
	}
	protected := make(map[key]struct{}, len(items))
	for _, item := range items {
		protected[key{kind: KindInbound, addr: item.Address}] = struct{}{}
	}
	evictions, ok := m.planEntryEvictionsLocked(newEntries, now, protected, releases)
	if !ok {
		m.stats.importRejections++
		m.stats.entryCapRejections++
		m.mu.Unlock()
		m.logImportRejection(origin, ErrEntryLimit)
		return ErrEntryLimit
	}

	if previous != nil {
		m.removeImportLocked(origin, previous, now)
	}
	m.evictEntriesLocked(evictions)

	next := &importRecord{items: make([]importItem, 0, len(items))}
	for _, item := range items {
		ek := key{kind: KindInbound, addr: item.Address}
		e := m.entries[ek]
		if e == nil {
			e = &entry{k: ek, localBalance: newDecayingSample(now, DecayWindowSeconds)}
			m.entries[ek] = e
		}
		if e.localRefs == 0 && e.importRefs == 0 && e.expiry != nil {
			m.retainedEntries--
		}
		m.cancelEntryExpiryLocked(e)
		if e.importRefs == 0 {
			m.importedEntries++
		}
		e.importRefs++
		e.remoteBalance = saturatingAdd(e.remoteBalance, item.Balance)
		next.items = append(next.items, importItem{entry: e, balance: item.Balance})
	}
	next.expiry = &expiryItem{when: now.Add(GossipExpiration), kind: expireImport, origin: origin, index: -1}
	heap.Push(&m.expiries, next.expiry)
	m.imports[origin] = next
	m.importItems += len(next.items)
	m.mu.Unlock()
	return nil
}

func (m *Manager) logImportRejection(origin string, err error) {
	ctx := context.Background()
	if !m.journal.Enabled(ctx, slog.LevelWarn) {
		return
	}
	m.journal.LogAttrs(ctx, slog.LevelWarn, "resource import rejected",
		slog.String("origin", origin), slog.Any("err", err))
}

type validatedGossipItem struct {
	Address string
	Balance int64
}

func (m *Manager) validateGossip(origin string, gossip Gossip) ([]validatedGossipItem, error) {
	if len(origin) > m.limits.MaxOriginLength {
		return nil, fmt.Errorf("%w: origin exceeds %d bytes", ErrInvalidImport, m.limits.MaxOriginLength)
	}
	if len(gossip.Items) > m.limits.MaxGossipItems {
		return nil, fmt.Errorf("%w: got %d items, limit %d", ErrImportItemLimit, len(gossip.Items), m.limits.MaxGossipItems)
	}
	dedup := make(map[string]uint64, len(gossip.Items))
	for _, item := range gossip.Items {
		if item.Balance == 0 {
			continue
		}
		addr, ok := canonicalEndpoint(KindInbound, item.Address, m.limits.MaxEndpointLength)
		if !ok {
			return nil, fmt.Errorf("%w: endpoint %q", ErrInvalidImport, item.Address)
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
	m.importItems -= len(rec.items)
	for _, item := range rec.items {
		item.entry.remoteBalance -= item.balance
		if item.entry.remoteBalance < 0 {
			item.entry.remoteBalance = 0
		}
		if item.entry.importRefs > 0 {
			if item.entry.importRefs == 1 {
				m.importedEntries--
			}
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
		Active:             len(m.entries) - m.retainedEntries,
		Retained:           m.retainedEntries,
		ImportedEntries:    m.importedEntries,
		ImportOrigins:      len(m.imports),
		ImportItems:        m.importItems,
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
