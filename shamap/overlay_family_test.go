package shamap

import (
	"context"
	"errors"
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

// stubFamily returns canned Fetch results, for exercising OverlayFamily's
// overlay-error and empty-hit paths that a real nodestore never produces.
type stubFamily struct {
	data []byte
	err  error
}

func (s stubFamily) Fetch(context.Context, [32]byte) ([]byte, error) { return s.data, s.err }
func (s stubFamily) StoreBatch(context.Context, []FlushEntry) error  { return nil }

func TestOverlayFamily_PropagatesOverlayError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("overlay boom")
	of := NewOverlayFamily(mustMemFamily(t), stubFamily{err: sentinel})

	var h [32]byte
	h[0] = 0x01
	if _, err := of.Fetch(ctx, h); !errors.Is(err, sentinel) {
		t.Fatalf("Fetch error = %v, want %v (must not fall through to base)", err, sentinel)
	}
}

func TestOverlayFamily_EmptyOverlayHitFallsThroughToBase(t *testing.T) {
	ctx := context.Background()
	base := mustMemFamily(t)
	var h [32]byte
	h[0] = 0x02
	if err := base.StoreBatch(ctx, []FlushEntry{{Hash: h, Data: []byte("from-base")}}); err != nil {
		t.Fatalf("base StoreBatch: %v", err)
	}
	// A non-nil zero-length overlay value must be treated as absent, not as a
	// hit that shadows the base.
	of := NewOverlayFamily(base, stubFamily{data: []byte{}})

	got, err := of.Fetch(ctx, h)
	if err != nil || string(got) != "from-base" {
		t.Fatalf("empty overlay hit did not fall through: got %q err %v", got, err)
	}
}

func TestNewOverlayFamily_NilArgsPanic(t *testing.T) {
	base := mustMemFamily(t)
	cases := []struct {
		name          string
		base, overlay Family
	}{
		{"nil base", nil, base},
		{"nil overlay", base, nil},
		{"both nil", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic on nil family argument")
				}
			}()
			NewOverlayFamily(tc.base, tc.overlay)
		})
	}
}
