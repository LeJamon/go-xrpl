package resource

import (
	"math"
	"testing"
	"time"
)

func TestExportOnlyIncludesActiveInboundConsumers(t *testing.T) {
	m, _ := newTestManager()
	inbound := m.NewInboundEndpoint("192.0.2.50:5000")
	outbound := m.NewOutboundEndpoint("192.0.2.51:51235")
	if inbound == nil || outbound == nil {
		t.Fatal("consumer acquisition failed")
	}
	load := NewCharge(MinimumGossipBalance*DecayWindowSeconds, "export")
	inbound.Charge(load, "")
	outbound.Charge(load, "")
	if got := m.ExportConsumers().Items; len(got) != 1 || got[0].Address != "192.0.2.50" {
		t.Fatalf("active export = %+v", got)
	}

	inbound.Release()
	if got := m.ExportConsumers().Items; len(got) != 0 {
		t.Fatalf("released export = %+v, want empty", got)
	}

	inbound = m.NewInboundEndpoint("192.0.2.50:6000")
	if inbound == nil {
		t.Fatal("reacquisition failed")
	}
	defer inbound.Release()
	defer outbound.Release()
	if got := m.ExportConsumers().Items; len(got) != 1 || got[0].Address != "192.0.2.50" {
		t.Fatalf("reacquired export = %+v", got)
	}
}

func TestCanonicalEndpointIdentity(t *testing.T) {
	m, _ := newTestManager()
	v4 := m.NewInboundEndpoint("192.0.2.9:5000")
	mapped := m.NewInboundEndpoint("[::ffff:192.0.2.9]:6000")
	if v4 == nil || mapped == nil {
		t.Fatal("IPv4 consumer acquisition failed")
	}
	defer v4.Release()
	defer mapped.Release()
	v4.Charge(NewCharge(DecayWindowSeconds*100, "canonical"), "")
	if mapped.Balance() != 100 {
		t.Fatalf("IPv4-mapped balance = %d, want 100", mapped.Balance())
	}

	expanded := m.NewInboundEndpoint("[2001:0db8:0:0:0:0:0:1]:5000")
	compressed := m.NewInboundEndpoint("2001:db8::1")
	if expanded == nil || compressed == nil {
		t.Fatal("IPv6 consumer acquisition failed")
	}
	defer expanded.Release()
	defer compressed.Release()
	expanded.Charge(NewCharge(DecayWindowSeconds*50, "canonical"), "")
	if compressed.Balance() != 50 {
		t.Fatalf("compressed IPv6 balance = %d, want 50", compressed.Balance())
	}
	if invalid := m.NewInboundEndpoint("not-an-endpoint"); invalid != nil {
		invalid.Release()
		t.Fatal("invalid endpoint was retained")
	}

	hostname := m.NewOutboundEndpoint(" R.RIPPLE.COM.:51235 ")
	canonicalHostname := m.NewOutboundEndpoint("r.ripple.com:51235")
	if hostname == nil || canonicalHostname == nil {
		t.Fatal("DNS endpoint acquisition failed")
	}
	defer hostname.Release()
	defer canonicalHostname.Release()
	if hostname.Endpoint() != "r.ripple.com:51235" || canonicalHostname.Endpoint() != hostname.Endpoint() {
		t.Fatalf("outbound DNS endpoints = %q and %q", hostname.Endpoint(), canonicalHostname.Endpoint())
	}

	trailingColon := m.NewInboundEndpoint(" 192.0.2.10: ")
	if trailingColon == nil || trailingColon.Endpoint() != "192.0.2.10" {
		t.Fatalf("trailing-colon endpoint = %v", trailingColon)
	}
	trailingColon.Release()
}

func TestImportedActiveEntryExportsLocalBalance(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(clock.Now, nil)
	consumer := m.NewInboundEndpoint("192.0.2.60")
	consumer.Charge(NewCharge(MinimumGossipBalance*DecayWindowSeconds, "export"), "")
	consumer.Release()

	if err := m.ImportConsumers("cluster-a", Gossip{Items: []GossipItem{{Address: "192.0.2.60", Balance: 1}}}); err != nil {
		t.Fatal(err)
	}
	items := m.ExportConsumers().Items
	if len(items) != 1 || items[0].Address != "192.0.2.60" {
		t.Fatalf("import-retained export = %+v", items)
	}
	if err := m.ImportConsumers("cluster-a", Gossip{}); err != nil {
		t.Fatal(err)
	}
	if items := m.ExportConsumers().Items; len(items) != 0 {
		t.Fatalf("inactive export = %+v", items)
	}
}

func TestDecaySaturationDoesNotOverflow(t *testing.T) {
	now := time.Unix(1_000, 0)
	sample := newDecayingSample(now, DecayWindowSeconds)
	sample.value = math.MaxInt64
	want := int64(math.MaxInt64 - math.MaxInt64/DecayWindowSeconds - 1)
	if got := sample.valueAt(now.Add(time.Second)) * DecayWindowSeconds; got < 0 || sample.value != want {
		t.Fatalf("decayed saturated sample = %d (normalized %d), want %d", sample.value, got, want)
	}
}

func TestExportIsBounded(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxGossipItems = 2
	m := NewManagerWithLimits(nil, nil, limits)
	consumers := make([]*Consumer, 0, 3)
	for _, address := range []string{"192.0.2.3", "192.0.2.1", "192.0.2.2"} {
		consumer := m.NewInboundEndpoint(address)
		if consumer == nil {
			t.Fatalf("consumer %s acquisition failed", address)
		}
		consumer.Charge(NewCharge(MinimumGossipBalance*DecayWindowSeconds, "export"), "")
		consumers = append(consumers, consumer)
	}
	defer func() {
		for _, consumer := range consumers {
			consumer.Release()
		}
	}()

	items := m.ExportConsumers().Items
	if len(items) != limits.MaxGossipItems {
		t.Fatalf("export items = %d, want %d", len(items), limits.MaxGossipItems)
	}
	if items[0].Address != "192.0.2.1" || items[1].Address != "192.0.2.2" {
		t.Fatalf("bounded export = %+v", items)
	}
}

func TestEntryCapRejectsWithoutEvictingActiveState(t *testing.T) {
	clock := newFakeClock()
	limits := DefaultLimits()
	limits.MaxEntries = 2
	m := NewManagerWithLimits(clock.Now, nil, limits)
	first := m.NewInboundEndpoint("192.0.2.1")
	second := m.NewInboundEndpoint("192.0.2.2")
	if first == nil || second == nil {
		t.Fatal("initial consumers were rejected")
	}
	if third := m.NewInboundEndpoint("192.0.2.3"); third != nil {
		third.Release()
		t.Fatal("entry cap admitted a third active endpoint")
	}
	if stats := m.Stats(); stats.Entries != 2 || stats.EntryCapRejections != 1 {
		t.Fatalf("cap stats = %+v", stats)
	}

	first.Release()
	clock.Advance(SecondsUntilExpiration + time.Second)
	m.PeriodicActivity()
	third := m.NewInboundEndpoint("192.0.2.3")
	if third == nil {
		t.Fatal("expired capacity was not reusable")
	}
	third.Release()
	second.Release()
}

func TestImportIsBoundedDeduplicatedAndTransactional(t *testing.T) {
	clock := newFakeClock()
	limits := DefaultLimits()
	limits.MaxGossipItems = 2
	m := NewManagerWithLimits(clock.Now, nil, limits)
	if err := m.ImportConsumers("", Gossip{Items: []GossipItem{
		{Address: "192.0.2.10:0", Balance: math.MaxUint32},
		{Address: "192.0.2.10", Balance: math.MaxUint32},
	}}); err != nil {
		t.Fatalf("deduplicated import failed: %v", err)
	}
	consumer := m.NewInboundEndpoint("192.0.2.10:9000")
	if consumer == nil {
		t.Fatal("imported consumer acquisition failed")
	}
	defer consumer.Release()
	if got := consumer.Balance(); got != math.MaxUint32 {
		t.Fatalf("saturated duplicate balance = %d, want %d", got, uint64(math.MaxUint32))
	}

	invalid := Gossip{Items: []GossipItem{{Address: "invalid", Balance: 1}}}
	if err := m.ImportConsumers("", invalid); err == nil {
		t.Fatal("invalid replacement was accepted")
	}
	if got := consumer.Balance(); got != math.MaxUint32 {
		t.Fatalf("invalid replacement mutated balance to %d", got)
	}

	if err := m.ImportConsumers("", Gossip{}); err != nil {
		t.Fatalf("empty-origin clear failed: %v", err)
	}
	if got := consumer.Balance(); got != 0 {
		t.Fatalf("empty snapshot left remote balance %d", got)
	}
	if stats := m.Stats(); stats.Imports != 0 || stats.ImportRejections != 1 {
		t.Fatalf("import stats = %+v", stats)
	}
}

func TestEmptyOriginImportReplacesAndExpires(t *testing.T) {
	clock := newFakeClock()
	m := NewManager(clock.Now, nil)
	first := Gossip{Items: []GossipItem{{Address: "192.0.2.40", Balance: 2000}}}
	second := Gossip{Items: []GossipItem{{Address: "192.0.2.40", Balance: 3000}}}
	if err := m.ImportConsumers("", first); err != nil {
		t.Fatal(err)
	}
	if err := m.ImportConsumers("", second); err != nil {
		t.Fatal(err)
	}
	consumer := m.NewInboundEndpoint("192.0.2.40")
	if consumer == nil {
		t.Fatal("consumer acquisition failed")
	}
	defer consumer.Release()
	if got := consumer.Balance(); got != 3000 {
		t.Fatalf("empty-origin replacement balance = %d, want 3000", got)
	}

	clock.Advance(GossipExpiration + time.Second)
	m.PeriodicActivity()
	if got := consumer.Balance(); got != 0 {
		t.Fatalf("expired empty-origin balance = %d, want 0", got)
	}
	if stats := m.Stats(); stats.Imports != 0 {
		t.Fatalf("expired empty-origin imports = %d, want 0", stats.Imports)
	}
}

func TestStartedManagerExpiresIdleEntriesWithoutTraffic(t *testing.T) {
	clock := newFakeClock()
	limits := DefaultLimits()
	limits.MaxEntries = 1
	limits.MaxCleanupPerTick = 1
	m := NewManagerWithLimits(clock.Now, nil, limits)
	m.Start()
	defer m.Stop()

	consumer := m.NewInboundEndpoint("192.0.2.50")
	if consumer == nil {
		t.Fatal("consumer acquisition failed")
	}
	consumer.Release()
	clock.Advance(SecondsUntilExpiration + time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for m.Stats().Entries != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if stats := m.Stats(); stats.Entries != 0 || stats.Evictions != 1 {
		t.Fatalf("autonomous expiry stats = %+v", stats)
	}
}
