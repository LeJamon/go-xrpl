package cluster_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
)

const (
	pubA = "n9MDGCfimuyCmKXUAMcR12rv39PE6PY5YfFpNs75ZjtY3UWt31td"
	pubB = "nHU75pVH2Tak7adBWNP3H2CU3wcUtSgf45sKrd1uGyFyRcTozXNm"
)

func mustDecode(t *testing.T, k string) []byte {
	t.Helper()
	b, err := addresscodec.DecodeNodePublicKey(k)
	if err != nil {
		t.Fatalf("DecodeNodePublicKey(%q): %v", k, err)
	}
	return b
}

func identityBytes(fill byte) []byte {
	identity := bytes.Repeat([]byte{fill}, addresscodec.NodePublicKeyLength)
	identity[0] = 0x02
	return identity
}

func TestRegistry_MedianFee(t *testing.T) {
	r := cluster.New()
	now := time.Unix(2_000_000_000, 0)
	r.Update(mustDecode(t, pubA), "a", 320, now)
	r.Update(mustDecode(t, pubB), "b", 400, now)
	// Older-than-window entry is excluded from the median.
	stale := identityBytes(0x01)
	r.Update(stale, "stale", 9999, now.Add(-2*time.Minute))

	fee, ok := r.MedianFee(now.Add(-90 * time.Second))
	if !ok {
		t.Fatal("MedianFee should report ok when fresh entries exist")
	}
	// Two fresh entries {320, 400}; sort.Slice middle index = 1 → 400.
	if fee != 400 {
		t.Fatalf("median = %d; want 400", fee)
	}
}

func TestRegistry_MedianFee_EmptyWindow(t *testing.T) {
	r := cluster.New()
	now := time.Unix(2_000_000_000, 0)
	r.Update(mustDecode(t, pubA), "a", 320, now.Add(-10*time.Minute))
	if _, ok := r.MedianFee(now.Add(-90 * time.Second)); ok {
		t.Fatal("MedianFee with no fresh entries should report ok=false")
	}
}

func TestRegistry_ReceiverSemantics(t *testing.T) {
	var r *cluster.Registry
	id := mustDecode(t, pubA)
	if _, ok := r.Member(id); ok {
		t.Fatalf("nil registry should never report membership")
	}
	if r.Size() != 0 {
		t.Fatalf("nil Size = %d; want 0", r.Size())
	}
	r.ForEach(func(cluster.Member) { t.Fatal("ForEach on nil should be no-op") })
	if r.Update(id, "node", 1, time.Time{}) {
		t.Fatal("Update on nil registry should fail")
	}
	if err := r.Load([]string{pubA}); err == nil {
		t.Fatal("Load on nil registry should return an error")
	}
	if _, ok := r.MedianFee(time.Now()); ok {
		t.Fatal("MedianFee on nil registry should report ok=false")
	}

	var zero cluster.Registry
	if !zero.Update(id, "node", 1, time.Time{}) {
		t.Fatal("zero-value registry Update should succeed")
	}
	if _, ok := zero.Member(id); !ok {
		t.Fatal("zero-value registry should retain updates")
	}
	if err := zero.Load([]string{pubB}); err != nil {
		t.Fatalf("zero-value registry Load: %v", err)
	}
	if zero.Size() != 2 {
		t.Fatalf("zero-value registry Size = %d; want 2", zero.Size())
	}
}

func TestRegistry_LoadAndMember(t *testing.T) {
	r := cluster.New()
	if err := r.Load([]string{
		pubA + " primary-validator",
		pubB,
	}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := r.Size(); got != 2 {
		t.Fatalf("Size = %d; want 2", got)
	}

	mA, ok := r.Member(mustDecode(t, pubA))
	if !ok {
		t.Fatalf("expected pubA in registry")
	}
	if mA.Name != "primary-validator" {
		t.Fatalf("pubA name = %q; want %q", mA.Name, "primary-validator")
	}

	mB, ok := r.Member(mustDecode(t, pubB))
	if !ok {
		t.Fatalf("expected pubB in registry")
	}
	if mB.Name != "" {
		t.Fatalf("pubB name = %q; want empty", mB.Name)
	}
}

func TestRegistry_LoadTrimsCommentWhitespace(t *testing.T) {
	r := cluster.New()
	if err := r.Load([]string{"   " + pubA + "    my  validator   "}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	m, ok := r.Member(mustDecode(t, pubA))
	if !ok {
		t.Fatal("expected member present")
	}
	if m.Name != "my  validator" {
		t.Fatalf("name = %q; want %q", m.Name, "my  validator")
	}
}

func TestRegistry_LoadSkipsBlankLines(t *testing.T) {
	r := cluster.New()
	if err := r.Load([]string{"", "   ", "\t", pubA}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Size() != 1 {
		t.Fatalf("Size = %d; want 1", r.Size())
	}
}

func TestRegistry_LoadRejectsMalformed(t *testing.T) {
	r := cluster.New()
	err := r.Load([]string{"!!! not a pubkey !!!"})
	if err == nil {
		t.Fatal("expected error for malformed entry")
	}
}

func TestRegistry_LoadRejectsInvalidPubkey(t *testing.T) {
	r := cluster.New()
	err := r.Load([]string{"n9NotARealKey"})
	if err == nil {
		t.Fatal("expected error for invalid node pubkey")
	}
}

func TestRegistry_LoadRejectsWrongIdentityLength(t *testing.T) {
	r := cluster.New()
	encoded := addresscodec.Base58CheckEncode(
		identityBytes(0x01)[:addresscodec.NodePublicKeyLength-1],
		addresscodec.NodePublicKeyPrefix,
	)
	if err := r.Load([]string{encoded}); err == nil {
		t.Fatal("expected error for wrong-length node identity")
	}
}

func TestRegistry_LoadRejectsInvalidKeyType(t *testing.T) {
	r := cluster.New()
	identity := identityBytes(0x01)
	identity[0] = 0x01
	encoded := addresscodec.Base58CheckEncode(identity, addresscodec.NodePublicKeyPrefix)
	if err := r.Load([]string{encoded}); err == nil {
		t.Fatal("expected error for invalid node public key type")
	}
}

func TestRegistry_LoadDeduplicates(t *testing.T) {
	r := cluster.New()
	err := r.Load([]string{
		pubA + " first-name",
		pubA + " second-name",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.Size() != 1 {
		t.Fatalf("Size = %d; want 1 (dup must be ignored)", r.Size())
	}
	m, _ := r.Member(mustDecode(t, pubA))
	if m.Name != "first-name" {
		t.Fatalf("dedup kept %q; want first-name", m.Name)
	}
}

func TestRegistry_UpdateReportTime(t *testing.T) {
	r := cluster.New()
	id := mustDecode(t, pubA)

	t1 := time.Unix(1000, 0)
	if !r.Update(id, "alpha", 100, t1) {
		t.Fatal("first Update should return true")
	}

	if r.Update(id, "beta", 999, t1) {
		t.Fatal("Update with same reportTime must return false")
	}
	m, _ := r.Member(id)
	if m.Name != "alpha" || m.LoadFee != 100 {
		t.Fatalf("unchanged member mutated: %+v", m)
	}

	t2 := t1.Add(time.Second)
	if !r.Update(id, "", 250, t2) {
		t.Fatal("Update with later reportTime should return true")
	}
	m, _ = r.Member(id)
	if m.Name != "alpha" {
		t.Fatalf("empty new name should preserve prior name; got %q", m.Name)
	}
	if m.LoadFee != 250 {
		t.Fatalf("LoadFee = %d; want 250", m.LoadFee)
	}
	if !m.ReportTime.Equal(t2) {
		t.Fatalf("ReportTime = %v; want %v", m.ReportTime, t2)
	}
}

func TestRegistry_IdentityBoundariesAndOwnership(t *testing.T) {
	r := cluster.New()
	for _, invalid := range [][]byte{
		nil,
		identityBytes(0x01)[:addresscodec.NodePublicKeyLength-1],
		append(identityBytes(0x01), 0x01),
		append([]byte{0x01}, identityBytes(0x01)[1:]...),
	} {
		if r.Update(invalid, "invalid", 1, time.Time{}) {
			t.Fatalf("Update accepted %d-byte identity", len(invalid))
		}
		if _, ok := r.Member(invalid); ok {
			t.Fatalf("Member accepted %d-byte identity", len(invalid))
		}
	}

	id := mustDecode(t, pubA)
	original := append([]byte(nil), id...)
	if !r.Update(id, "node", 1, time.Time{}) {
		t.Fatal("Update rejected valid identity")
	}
	id[0] ^= 0xff
	member, ok := r.Member(original)
	if !ok {
		t.Fatal("mutating Update input changed the stored key")
	}
	member.Identity[0] ^= 0xff
	again, ok := r.Member(original)
	if !ok || !bytes.Equal(again.Identity[:], original) {
		t.Fatal("mutating Member result changed registry-owned state")
	}

	r.ForEach(func(snapshot cluster.Member) {
		snapshot.Identity[0] ^= 0xff
	})
	again, ok = r.Member(original)
	if !ok || !bytes.Equal(again.Identity[:], original) {
		t.Fatal("mutating ForEach snapshot changed registry-owned state")
	}
}

func TestRegistry_ForEachIteratesAll(t *testing.T) {
	r := cluster.New()
	if err := r.Load([]string{pubA + " a", pubB + " b"}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	count := 0
	names := map[string]bool{}
	r.ForEach(func(m cluster.Member) {
		count++
		names[m.Name] = true
	})
	if count != 2 {
		t.Fatalf("ForEach visited %d; want 2", count)
	}
	if !names["a"] || !names["b"] {
		t.Fatalf("missing names: %v", names)
	}
}

func TestRegistry_ForEachDeterministicOrder(t *testing.T) {
	r := cluster.New()
	for _, fill := range []byte{0x03, 0x01, 0x02} {
		if !r.Update(identityBytes(fill), "", 0, time.Time{}) {
			t.Fatalf("Update(%x) failed", fill)
		}
	}
	var got []byte
	r.ForEach(func(member cluster.Member) {
		got = append(got, member.Identity[1])
	})
	if !bytes.Equal(got, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("ForEach order = %x; want 010203", got)
	}
}

func TestRegistry_ForEachAllowsReentrantAccess(t *testing.T) {
	r := cluster.New()
	for _, fill := range []byte{0x01, 0x02} {
		if !r.Update(identityBytes(fill), "", 0, time.Time{}) {
			t.Fatalf("Update(%x) failed", fill)
		}
	}

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	type walkResult struct {
		visited   []cluster.Identity
		reentrant bool
	}
	walkDone := make(chan walkResult, 1)
	go func() {
		var visited []cluster.Identity
		reentrant := true
		r.ForEach(func(member cluster.Member) {
			if len(visited) == 0 {
				close(callbackStarted)
				<-releaseCallback
			}
			if _, ok := r.Member(member.Identity[:]); !ok {
				reentrant = false
			}
			if r.Size() == 0 {
				reentrant = false
			}
			if !r.Update(member.Identity[:], "updated", 1, member.ReportTime.Add(time.Second)) {
				reentrant = false
			}
			visited = append(visited, member.Identity)
		})
		walkDone <- walkResult{visited: visited, reentrant: reentrant}
	}()
	<-callbackStarted

	writerDone := make(chan bool, 1)
	go func() {
		writerDone <- r.Update(identityBytes(0x03), "concurrent", 0, time.Time{})
	}()
	select {
	case updated := <-writerDone:
		if !updated {
			t.Fatal("concurrent writer did not update the registry")
		}
	case <-time.After(2 * time.Second):
		close(releaseCallback)
		t.Fatal("concurrent writer blocked while ForEach callback was running")
	}
	close(releaseCallback)

	var result walkResult
	select {
	case result = <-walkDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ForEach callback deadlocked on nested registry access")
	}
	if !result.reentrant {
		t.Fatal("ForEach callback could not access the registry")
	}
	if len(result.visited) != 2 {
		t.Fatalf("current snapshot visited %d members; want 2", len(result.visited))
	}
	if r.Size() != 3 {
		t.Fatalf("registry Size = %d after concurrent update; want 3", r.Size())
	}
}

func TestRegistry_ConcurrentSnapshotsDoNotAlias(t *testing.T) {
	r := cluster.New()
	id := mustDecode(t, pubA)
	if !r.Update(id, "node", 0, time.Time{}) {
		t.Fatal("initial Update failed")
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 1; i <= 1_000; i++ {
			r.Update(id, "", uint32(i), time.Unix(int64(i), 0))
		}
	}()
	go func() {
		defer wg.Done()
		for range 1_000 {
			member, ok := r.Member(id)
			if ok {
				member.Identity[0] ^= 0xff
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 1_000 {
			r.ForEach(func(member cluster.Member) {
				member.Identity[0] ^= 0xff
			})
		}
	}()
	wg.Wait()
	member, ok := r.Member(id)
	if !ok || !bytes.Equal(member.Identity[:], id) {
		t.Fatal("concurrent snapshot mutation corrupted registry identity")
	}
}

func makeNodePub(t *testing.T, salt byte) string {
	t.Helper()
	raw := identityBytes(salt)
	enc, err := addresscodec.EncodeNodePublicKey(raw)
	if err != nil {
		t.Fatalf("EncodeNodePublicKey: %v", err)
	}
	return enc
}

// TestRegistry_LoadConfigParity mirrors rippled's
// cluster_test.cpp::testConfigLoad (lines 191-258).
func TestRegistry_LoadConfigParity(t *testing.T) {
	pubs := make([]string, 8)
	for i := range pubs {
		pubs[i] = makeNodePub(t, byte(0x10+i))
	}

	t.Run("empty config", func(t *testing.T) {
		r := cluster.New()
		if err := r.Load(nil); err != nil {
			t.Fatalf("Load(nil): %v", err)
		}
		if r.Size() != 0 {
			t.Fatalf("Size = %d; want 0", r.Size())
		}
	})

	t.Run("valid table", func(t *testing.T) {
		r := cluster.New()
		entries := []string{
			pubs[0],                                       // (a) no comment
			pubs[1] + "    ",                              // (b) trailing whitespace only
			pubs[2] + " Comment",                          // (c) basic comment
			pubs[3] + " Multi Word Comment",               // (d) multi-word
			pubs[4] + "  Leading Whitespace",              // (e) extra leading ws
			pubs[5] + " Trailing Whitespace  ",            // (f) trailing ws after comment
			pubs[6] + "  Leading & Trailing Whitespace  ", // (g) both
			pubs[7] + "  Leading,  Trailing  &  Internal  Whitespace  ", // (h) plus internal
		}
		if err := r.Load(entries); err != nil {
			t.Fatalf("Load: %v", err)
		}
		for i, p := range pubs {
			id, err := addresscodec.DecodeNodePublicKey(p)
			if err != nil {
				t.Fatalf("DecodeNodePublicKey[%d]: %v", i, err)
			}
			if _, ok := r.Member(id); !ok {
				t.Fatalf("entry %d not present in registry", i)
			}
		}
	})

	t.Run("invalid pubkey rejected", func(t *testing.T) {
		r := cluster.New()
		if err := r.Load([]string{"NotAPublicKey"}); err == nil {
			t.Fatal("expected error for invalid base58 pubkey")
		}
	})

	t.Run("trailing bang without whitespace rejected", func(t *testing.T) {
		r := cluster.New()
		if err := r.Load([]string{pubs[0] + "!"}); err == nil {
			t.Fatal("expected error: '!' immediately after pubkey is not a valid comment separator")
		}
	})

	t.Run("trailing bang with comment rejected", func(t *testing.T) {
		r := cluster.New()
		if err := r.Load([]string{pubs[0] + "!  Comment"}); err == nil {
			t.Fatal("expected error: '!' immediately after pubkey is not a valid comment separator")
		}
	})

	t.Run("malformed first entry stops subsequent entries", func(t *testing.T) {
		r := cluster.New()
		err := r.Load([]string{
			pubs[0] + "XXX",
			pubs[1],
		})
		if err == nil {
			t.Fatal("expected error from malformed first entry")
		}
		for i, p := range pubs[:2] {
			id, _ := addresscodec.DecodeNodePublicKey(p)
			if _, ok := r.Member(id); ok {
				t.Fatalf("entry %d unexpectedly present after Load failed", i)
			}
		}
	})

	t.Run("valid entries before an error are retained", func(t *testing.T) {
		r := cluster.New()
		err := r.Load([]string{
			pubs[0],
			pubs[1] + "XXX",
			pubs[2],
		})
		if err == nil {
			t.Fatal("expected error from malformed second entry")
		}
		first, _ := addresscodec.DecodeNodePublicKey(pubs[0])
		if _, ok := r.Member(first); !ok {
			t.Fatal("valid entry before load error was not retained")
		}
		for _, pub := range pubs[1:3] {
			id, _ := addresscodec.DecodeNodePublicKey(pub)
			if _, ok := r.Member(id); ok {
				t.Fatalf("entry %q unexpectedly present after load error", pub)
			}
		}
	})
}

// TestRegistry_LoadAcceptsVerticalTabWhitespace pins the POSIX-class
// regex: \v is whitespace under [[:space:]] but not under Go's \s.
func TestRegistry_LoadAcceptsVerticalTabWhitespace(t *testing.T) {
	r := cluster.New()
	if err := r.Load([]string{pubA + "\v" + "name-after-vtab"}); err != nil {
		t.Fatalf("Load: %v (regex must accept \\v as whitespace, matching rippled [[:space:]])", err)
	}
	id, _ := addresscodec.DecodeNodePublicKey(pubA)
	m, ok := r.Member(id)
	if !ok {
		t.Fatal("expected pubA in registry")
	}
	if m.Name != "name-after-vtab" {
		t.Fatalf("name = %q; want %q", m.Name, "name-after-vtab")
	}
}
