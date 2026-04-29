package cluster_test

import (
	"testing"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/cluster"
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

func TestRegistry_NilSafe(t *testing.T) {
	var r *cluster.Registry
	if _, ok := r.Member([]byte{0x01}); ok {
		t.Fatalf("nil registry should never report membership")
	}
	if r.Size() != 0 {
		t.Fatalf("nil Size = %d; want 0", r.Size())
	}
	r.ForEach(func(cluster.Member) { t.Fatal("ForEach on nil should be no-op") })
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

func makeNodePub(t *testing.T, salt byte) string {
	t.Helper()
	raw := make([]byte, 33)
	for i := range raw {
		raw[i] = salt
	}
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

	t.Run("malformed entry aborts load and rejects subsequent entries", func(t *testing.T) {
		// cluster_test.cpp:248-258 — a bad entry must prevent every
		// other entry, including subsequent ones, from being inserted.
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
