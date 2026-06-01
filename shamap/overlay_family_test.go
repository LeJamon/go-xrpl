package shamap

import (
	"context"
	"testing"
)

func mustMemFamily(t *testing.T) *NodeStoreFamily {
	t.Helper()
	fam, err := NewMemoryNodeStoreFamily()
	if err != nil {
		t.Fatalf("NewMemoryNodeStoreFamily: %v", err)
	}
	return fam
}

func TestOverlayFamily_ReadsOverlayThenBase(t *testing.T) {
	ctx := context.Background()
	base := mustMemFamily(t)
	overlay := mustMemFamily(t)
	of := NewOverlayFamily(base, overlay)

	var hBase, hBoth [32]byte
	hBase[0], hBoth[0] = 0x01, 0x02

	// Seed the base only.
	if err := base.StoreBatch(ctx, []FlushEntry{{Hash: hBase, Data: []byte("from-base")}}); err != nil {
		t.Fatalf("base StoreBatch: %v", err)
	}
	// Seed both with different bytes; overlay must win.
	if err := base.StoreBatch(ctx, []FlushEntry{{Hash: hBoth, Data: []byte("base-version")}}); err != nil {
		t.Fatalf("base StoreBatch: %v", err)
	}
	if err := overlay.StoreBatch(ctx, []FlushEntry{{Hash: hBoth, Data: []byte("overlay-version")}}); err != nil {
		t.Fatalf("overlay StoreBatch: %v", err)
	}

	got, err := of.Fetch(ctx, hBase)
	if err != nil || string(got) != "from-base" {
		t.Fatalf("base fallback: got %q err %v", got, err)
	}
	got, err = of.Fetch(ctx, hBoth)
	if err != nil || string(got) != "overlay-version" {
		t.Fatalf("overlay precedence: got %q err %v", got, err)
	}

	var missing [32]byte
	missing[0] = 0xFF
	got, err = of.Fetch(ctx, missing)
	if err != nil || got != nil {
		t.Fatalf("absent node: got %q err %v, want nil/nil", got, err)
	}
}

func TestOverlayFamily_WritesOnlyToOverlay(t *testing.T) {
	ctx := context.Background()
	base := mustMemFamily(t)
	overlay := mustMemFamily(t)
	of := NewOverlayFamily(base, overlay)

	var h [32]byte
	h[0] = 0xAB
	if err := of.StoreBatch(ctx, []FlushEntry{{Hash: h, Data: []byte("written")}}); err != nil {
		t.Fatalf("OverlayFamily StoreBatch: %v", err)
	}

	// The base must remain untouched so it can stay read-only and shared.
	if data, err := base.Fetch(ctx, h); err != nil || data != nil {
		t.Fatalf("base mutated: got %q err %v, want nil/nil", data, err)
	}
	if data, err := overlay.Fetch(ctx, h); err != nil || string(data) != "written" {
		t.Fatalf("overlay missing write: got %q err %v", data, err)
	}
}
