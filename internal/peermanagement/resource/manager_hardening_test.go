package resource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestConsumerConcurrentReleaseIsExactlyOnce(t *testing.T) {
	m, _ := newTestManager()
	first := m.NewInboundEndpoint("192.0.2.1:1000")
	second := m.NewInboundEndpoint("192.0.2.1:2000")
	if first == nil || second == nil {
		t.Fatal("failed to acquire consumers")
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				first.Charge(NewCharge(10, "race"), "")
				_ = first.Balance()
				_ = first.Disconnect()
				first.SetPublicKey("n9-test")
			}
		}()
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first.Release()
		}()
	}
	wg.Wait()

	if got := m.Stats().Active; got != 1 {
		t.Fatalf("active entries after repeated release = %d, want second handle active", got)
	}
	second.Charge(NewCharge(DecayWindowSeconds, "proof"), "")
	if second.Balance() == 0 {
		t.Fatal("second same-key handle lost shared reputation")
	}
	second.Release()
	if got := m.Stats().Retained; got != 1 {
		t.Fatalf("retained entries = %d, want 1", got)
	}
}

func TestExportConsumersTracksLocalActivity(t *testing.T) {
	m, _ := newTestManager()
	const address = "192.0.2.2:1000"
	c := m.NewInboundEndpoint(address)
	c.Charge(NewCharge(MinimumGossipBalance*DecayWindowSeconds*2, "seed"), "")
	if got := len(m.ExportConsumers().Items); got != 1 {
		t.Fatalf("active export count = %d, want 1", got)
	}
	c.Release()
	if got := len(m.ExportConsumers().Items); got != 0 {
		t.Fatalf("released export count = %d, want 0", got)
	}

	if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{{Address: address, Balance: 1500}}}); err != nil {
		t.Fatal(err)
	}
	if got := len(m.ExportConsumers().Items); got != 0 {
		t.Fatalf("import-pinned released export count = %d, want 0", got)
	}

	reacquired := m.NewInboundEndpoint("192.0.2.2:2000")
	if got := len(m.ExportConsumers().Items); got != 1 {
		t.Fatalf("reacquired export count = %d, want 1", got)
	}
	reacquired.Release()
}

func TestDecayRetainsFractionalElapsedAndResetBoundary(t *testing.T) {
	d := newDecayingSample(0, DecayWindowSeconds)
	d.value = 3200
	if got := d.valueAt(900 * time.Millisecond); got != 100 {
		t.Fatalf("sub-second value = %d, want 100", got)
	}
	first := d.valueAt(1100 * time.Millisecond)
	if d.when != time.Second {
		t.Fatalf("anchor = %s, want 1s", d.when)
	}
	if got := d.valueAt(1900 * time.Millisecond); got != first {
		t.Fatalf("fractional elapsed was discarded: got %d want %d", got, first)
	}
	if got := d.valueAt(2100 * time.Millisecond); got >= first {
		t.Fatalf("second elapsed tick did not decay: got %d previous %d", got, first)
	}
	anchor, value := d.when, d.value
	d.decay(time.Second)
	if d.when != anchor || d.value != value {
		t.Fatalf("backward time changed sample: anchor=%s value=%d", d.when, d.value)
	}

	d = newDecayingSample(0, DecayWindowSeconds)
	d.value = 3200
	d.decay(time.Duration(4*DecayWindowSeconds+1) * time.Second)
	if d.value != 0 {
		t.Fatalf("sample beyond reset boundary = %d, want 0", d.value)
	}
}

func TestManagerClockIsIndependentOfWallTime(t *testing.T) {
	var monotonic time.Duration
	m := NewManager(func() time.Duration { return monotonic }, testLogger())
	c := m.NewInboundEndpoint("192.0.2.3")
	c.Charge(NewCharge(DecayWindowSeconds*100, "seed"), "")
	initial := c.Balance()
	if got := c.Balance(); got != initial {
		t.Fatalf("unchanged monotonic clock changed balance: got %d want %d", got, initial)
	}
	monotonic += time.Second
	if got := c.Balance(); got >= initial {
		t.Fatalf("monotonic advance did not decay: got %d initial %d", got, initial)
	}
	c.Release()
}

func TestCanonicalEndpointIdentity(t *testing.T) {
	m, _ := newTestManager()
	pairs := [][2]string{
		{"[2001:0DB8:0:0:0:0:0:1]:1000", "2001:db8::1"},
		{"[::ffff:192.0.2.4]:1000", "192.0.2.4:2000"},
	}
	for i, pair := range pairs {
		first := m.NewInboundEndpoint(pair[0])
		second := m.NewInboundEndpoint(pair[1])
		if first == nil || second == nil {
			t.Fatalf("pair %d failed canonical acquisition", i)
		}
		first.Charge(NewCharge(DecayWindowSeconds, "shared"), "")
		if second.Balance() == 0 {
			t.Fatalf("pair %d did not share reputation", i)
		}
		first.Release()
		second.Release()
	}
	if got := m.Stats().Entries; got != len(pairs) {
		t.Fatalf("canonical entry count = %d, want %d", got, len(pairs))
	}
	if c := m.NewInboundEndpoint("203.0.113.1:"); c != nil {
		t.Fatal("malformed endpoint was accepted")
	}
	if c := m.NewOutboundEndpoint("2001:db8::1"); c != nil {
		t.Fatal("outbound endpoint without port was accepted")
	}
	if c := m.NewInboundEndpoint("fe80::1%en0"); c != nil {
		t.Fatal("zone-scoped endpoint was accepted")
	}
}

func TestImportReplacementIsTransactional(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(clock.Now, testLogger(), WithLimits(Limits{
		MaxEntries: 2, MaxImportedEntries: 2, MaxImportOrigins: 1, MaxImportItems: 2,
	}))
	if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{{Address: "192.0.2.1", Balance: 900}}}); err != nil {
		t.Fatal(err)
	}
	active := m.NewInboundEndpoint("192.0.2.2")
	defer active.Release()

	err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{
		{Address: "192.0.2.3", Balance: 1000},
		{Address: "192.0.2.4", Balance: 1100},
	}})
	if !errors.Is(err, ErrEntryLimit) {
		t.Fatalf("replacement error = %v, want %v", err, ErrEntryLimit)
	}
	old := m.NewInboundEndpoint("192.0.2.1")
	if old == nil {
		t.Fatal("rejected replacement removed prior import")
	}
	if old.Balance() != 900 {
		t.Fatalf("rejected replacement changed prior balance: got %d", old.Balance())
	}
	old.Release()

	clock.Advance(GossipExpiration + time.Second)
	m.periodicActivity()
	if got := m.Stats().ImportOrigins; got != 0 {
		t.Fatalf("prior import expiry was cancelled by rejected replacement: origins=%d", got)
	}
}

func TestImportReplacementCanReuseReleasedEntryCapacity(t *testing.T) {
	m := NewManager(nil, testLogger(), WithLimits(Limits{
		MaxEntries: 1, MaxImportedEntries: 1, MaxImportOrigins: 1, MaxImportItems: 1,
	}))
	if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{{Address: "192.0.2.10", Balance: 100}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{{Address: "192.0.2.11", Balance: 200}}}); err != nil {
		t.Fatalf("full-capacity replacement failed: %v", err)
	}
	if stats := m.Stats(); stats.Entries != 1 || stats.ImportedEntries != 1 || stats.ImportItems != 1 {
		t.Fatalf("replacement stats = %+v", stats)
	}
}

func TestFailedCapacityPlanDoesNotPartiallyEvict(t *testing.T) {
	m := NewManager(nil, testLogger(), WithLimits(Limits{
		MaxEntries: 3, MaxImportedEntries: 3, MaxImportOrigins: 1, MaxImportItems: 3,
	}))
	for _, address := range []string{"192.0.2.20", "192.0.2.21"} {
		consumer := m.NewInboundEndpoint(address)
		consumer.Release()
	}
	active := m.NewInboundEndpoint("192.0.2.22")
	defer active.Release()
	err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{
		{Address: "192.0.2.23", Balance: 1},
		{Address: "192.0.2.24", Balance: 1},
		{Address: "192.0.2.25", Balance: 1},
	}})
	if !errors.Is(err, ErrEntryLimit) {
		t.Fatalf("import error = %v, want %v", err, ErrEntryLimit)
	}
	if stats := m.Stats(); stats.Entries != 3 || stats.Evictions != 0 {
		t.Fatalf("failed plan mutated entries: %+v", stats)
	}
}

func TestInactiveHighBalanceBecomesEvictableAfterDecay(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(clock.Now, testLogger(), WithLimits(Limits{
		MaxEntries: 1, MaxImportedEntries: 1, MaxImportOrigins: 1, MaxImportItems: 1,
	}))
	consumer := m.NewInboundEndpoint("192.0.2.30")
	consumer.Charge(NewCharge(WarningThreshold*DecayWindowSeconds, "high"), "")
	consumer.Release()
	clock.Advance(time.Duration(4*DecayWindowSeconds+1) * time.Second)
	replacement := m.NewInboundEndpoint("192.0.2.31")
	if replacement == nil {
		t.Fatal("fully decayed inactive entry remained non-evictable")
	}
	replacement.Release()
}

func TestExportConsumersIsBoundedAndKeepsStrongest(t *testing.T) {
	m := NewManager(nil, testLogger(), WithLimits(Limits{
		MaxEntries: 4, MaxImportedEntries: 4, MaxImportOrigins: 1, MaxImportItems: 2,
	}))
	for i, address := range []string{"192.0.2.40", "192.0.2.41", "192.0.2.42"} {
		consumer := m.NewInboundEndpoint(address)
		consumer.Charge(NewCharge((i+1)*MinimumGossipBalance*DecayWindowSeconds, "seed"), "")
	}
	items := m.ExportConsumers().Items
	if len(items) != 2 || items[0].Address != "192.0.2.41" || items[1].Address != "192.0.2.42" {
		t.Fatalf("bounded export = %+v", items)
	}

	m.mu.Lock()
	key, _ := canonicalKey(KindInbound, "192.0.2.42")
	m.entries[key].localBalance.value = (int64(math.MaxUint32) + 1) * DecayWindowSeconds
	m.mu.Unlock()
	items = m.ExportConsumers().Items
	if items[1].Balance != math.MaxUint32 {
		t.Fatalf("exported balance = %d, want uint32 cap", items[1].Balance)
	}
}

func TestRemoteBalanceAggregationDoesNotOverflow(t *testing.T) {
	m := NewManager(nil, testLogger(), WithLimits(Limits{
		MaxEntries: 1, MaxImportedEntries: 1, MaxImportOrigins: 3, MaxImportItems: 1,
	}))
	for _, origin := range []string{"one", "two", "three"} {
		if err := m.ImportConsumers(origin, Gossip{Items: []GossipItem{{Address: "192.0.2.50", Balance: math.MaxUint32}}}); err != nil {
			t.Fatal(err)
		}
	}
	consumer := m.NewInboundEndpoint("192.0.2.50")
	defer consumer.Release()
	if got := consumer.Charge(Charge{}, ""); got != Drop {
		t.Fatalf("aggregate disposition = %v, want drop", got)
	}
	want := int64(math.MaxUint32) * 3
	if entries := m.Snapshot(0); len(entries) != 1 || entries[0].Remote != want {
		t.Fatalf("remote aggregate = %+v, want %d", entries, want)
	}
}

func TestConsumerAliasAndConcurrentDisconnectAreExactlyOnce(t *testing.T) {
	m, _ := newTestManager()
	consumer := m.NewInboundEndpoint("192.0.2.60")
	alias := *consumer
	consumer.Charge(NewCharge(DropThreshold*DecayWindowSeconds, "drop"), "")
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			alias.Disconnect()
		}()
	}
	wg.Wait()
	if got := m.Stats().Drops; got != 1 {
		t.Fatalf("disconnect drops = %d, want 1", got)
	}
	consumer.Release()
	alias.Release()
	if stats := m.Stats(); stats.Active != 0 || stats.Retained != 1 {
		t.Fatalf("alias release stats = %+v", stats)
	}
}

func TestImportedEntryLimitHasDistinctError(t *testing.T) {
	m := NewManager(nil, testLogger(), WithLimits(Limits{
		MaxEntries: 2, MaxImportedEntries: 1, MaxImportOrigins: 2, MaxImportItems: 1,
	}))
	if err := m.ImportConsumers("one", Gossip{Items: []GossipItem{{Address: "192.0.2.70", Balance: 1}}}); err != nil {
		t.Fatal(err)
	}
	err := m.ImportConsumers("two", Gossip{Items: []GossipItem{{Address: "192.0.2.71", Balance: 1}}})
	if !errors.Is(err, ErrImportedEntryLimit) || errors.Is(err, ErrEntryLimit) {
		t.Fatalf("imported-entry error = %v", err)
	}
}

func TestDispositionString(t *testing.T) {
	for disposition, want := range map[Disposition]string{Ok: "ok", Warn: "warn", Drop: "drop", 99: "unknown"} {
		if got := disposition.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", disposition, got, want)
		}
	}
}

func TestCanonicalKindsRemainDistinct(t *testing.T) {
	m, _ := newTestManager()
	inbound := m.NewInboundEndpoint("[::ffff:192.0.2.5]:5000")
	outbound := m.NewOutboundEndpoint("192.0.2.5:5000")
	admin := m.NewUnlimitedEndpoint("192.0.2.5")
	if inbound == nil || outbound == nil || admin == nil {
		t.Fatal("failed to acquire canonical kinds")
	}
	inbound.Charge(NewCharge(DecayWindowSeconds*100, "inbound"), "")
	if outbound.Balance() != 0 || admin.Balance() != 0 {
		t.Fatal("directional/admin keys shared reputation")
	}
	inbound.Release()
	outbound.Release()
	admin.Release()
}

func TestImportValidationDeduplicationAndReplacement(t *testing.T) {
	m, _ := newTestManager()
	if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{{Address: "2001:db8::1", Balance: 1200}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{
		{Address: "[2001:0DB8::1]:0", Balance: 1500},
		{Address: "2001:db8::1", Balance: 1400},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := m.Stats().ImportItems; got != 1 {
		t.Fatalf("deduplicated import items = %d, want 1", got)
	}
	c := m.NewInboundEndpoint("[2001:db8::1]:9999")
	if got := c.Balance(); got != 1500 {
		t.Fatalf("deduplicated balance = %d, want max 1500", got)
	}
	c.Release()

	if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{{Address: "bad", Balance: 2000}}}); err == nil {
		t.Fatal("malformed replacement accepted")
	}
	c = m.NewInboundEndpoint("2001:db8::1")
	if got := c.Balance(); got != 1500 {
		t.Fatalf("rejected replacement mutated prior balance: got %d", got)
	}
	c.Release()
}

func TestManagerLimitsEvictOnlyLowInactiveEntries(t *testing.T) {
	clock := newFakeClock()
	limits := Limits{MaxEntries: 3, MaxImportedEntries: 2, MaxImportOrigins: 1, MaxImportItems: 2}
	m := NewManager(clock.Now, testLogger(), WithLimits(limits))

	low := m.NewInboundEndpoint("192.0.2.10")
	low.Release()
	high := m.NewInboundEndpoint("192.0.2.11")
	high.Charge(NewCharge(WarningThreshold*DecayWindowSeconds, "high"), "")
	high.Release()
	active := m.NewInboundEndpoint("192.0.2.12")

	newConsumer := m.NewInboundEndpoint("192.0.2.13")
	if newConsumer == nil {
		t.Fatal("low inactive entry was not evicted")
	}
	stats := m.Stats()
	if stats.Entries != 3 || stats.Evictions != 1 {
		t.Fatalf("stats after eviction = %+v", stats)
	}
	retainedHigh := m.NewInboundEndpoint("192.0.2.11")
	if retainedHigh == nil || retainedHigh.Balance() < WarningThreshold {
		t.Fatal("high-balance reputation was evicted")
	}
	if active.Balance() != 0 {
		t.Fatal("active consumer was corrupted during eviction")
	}
	active.Release()
	newConsumer.Release()
	retainedHigh.Release()

	if err := m.ImportConsumers("one", Gossip{Items: []GossipItem{
		{Address: "198.51.100.1", Balance: 1000},
		{Address: "198.51.100.2", Balance: 1000},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.ImportConsumers("two", Gossip{}); err == nil {
		t.Fatal("origin cap was not enforced")
	}
	if err := m.ImportConsumers("one", Gossip{Items: []GossipItem{
		{Address: "198.51.100.1", Balance: 1000},
		{Address: "198.51.100.2", Balance: 1000},
		{Address: "198.51.100.3", Balance: 1000},
	}}); err == nil {
		t.Fatal("raw item cap was not enforced")
	}
}

func TestManagerRejectsWhenOnlyHighReputationRemains(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(clock.Now, testLogger(), WithLimits(Limits{
		MaxEntries: 1, MaxImportedEntries: 1, MaxImportOrigins: 1, MaxImportItems: 1,
	}))
	c := m.NewInboundEndpoint("192.0.2.20")
	c.Charge(NewCharge(WarningThreshold*DecayWindowSeconds, "high"), "")
	c.Release()
	if got := m.NewInboundEndpoint("192.0.2.21"); got != nil {
		t.Fatal("high-balance entry was evicted")
	}
	if got := m.Stats().EntryLimitDrops; got != 1 {
		t.Fatalf("entry limit drops = %d, want 1", got)
	}
}

func TestExpiryHeapIgnoresStaleDeadlines(t *testing.T) {
	m, clock := newTestManager()
	c := m.NewInboundEndpoint("192.0.2.30")
	c.Release()
	clock.Advance(100 * time.Second)
	c = m.NewInboundEndpoint("192.0.2.30")
	c.Release()
	clock.Advance(201 * time.Second)
	m.periodicActivity()
	if got := m.Stats().Entries; got != 1 {
		t.Fatalf("stale expiry removed re-released entry: entries=%d", got)
	}
	clock.Advance(100 * time.Second)
	m.periodicActivity()
	if got := m.Stats().Entries; got != 0 {
		t.Fatalf("current expiry did not remove entry: entries=%d", got)
	}
}

func TestExpiryHeapRemainsBoundedUnderReplacementChurn(t *testing.T) {
	m, _ := newTestManager()
	for range 1000 {
		consumer := m.NewInboundEndpoint("192.0.2.31")
		consumer.Release()
	}
	for i := range 1000 {
		if err := m.ImportConsumers("origin", Gossip{Items: []GossipItem{{
			Address: "198.51.100.31",
			Balance: int64(1000 + i),
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	deadlines := m.expiries.Len()
	m.mu.Unlock()
	if deadlines != 2 {
		t.Fatalf("deadline heap size = %d, want one retained entry and one import", deadlines)
	}
}

type reentrantLogHandler struct {
	manager *Manager
}

func (*reentrantLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *reentrantLogHandler) Handle(context.Context, slog.Record) error {
	if h.manager != nil {
		_ = h.manager.Stats()
	}
	return nil
}

func (h *reentrantLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *reentrantLogHandler) WithGroup(string) slog.Handler { return h }

func TestLoggingOccursOutsideManagerLock(t *testing.T) {
	handler := &reentrantLogHandler{}
	m := NewManager(nil, slog.New(handler))
	handler.manager = m
	c := m.NewInboundEndpoint("192.0.2.40")
	done := make(chan struct{})
	go func() {
		c.Charge(NewCharge(100, "reentrant"), fmt.Sprintf("test-%d", 1))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant logger deadlocked resource manager")
	}
	c.Release()
}
