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
