package binarycodec

import (
	"strings"
	"testing"
)

// TestEncode_JSONArraySizeCap verifies rippled's per-array-field cap of 512
// elements (maxSTParsedJSONArraySize) is enforced when encoding JSON to binary,
// across the three JSON-array field kinds. Reference: rippled commit 377b155ddc.
func TestEncode_JSONArraySizeCap(t *testing.T) {
	const issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	hashes := func(n int) []any {
		h := make([]any, n)
		for i := range h {
			h[i] = strings.Repeat("A", 64)
		}
		return h
	}

	t.Run("Vector256 over cap rejected with exact message", func(t *testing.T) {
		_, err := Encode(map[string]any{"Amendments": hashes(513)})
		msg, ok := AsArrayTooLargeError(err)
		if !ok {
			t.Fatalf("expected JSONArrayTooLargeError, got %v", err)
		}
		want := "Field 'Amendments' exceeds allowed JSON array size of 512 elements per field."
		if msg != want {
			t.Fatalf("message mismatch:\n got %q\nwant %q", msg, want)
		}
	})

	t.Run("Vector256 at cap accepted", func(t *testing.T) {
		if _, err := Encode(map[string]any{"Amendments": hashes(512)}); err != nil {
			t.Fatalf("512-element Amendments array must encode: %v", err)
		}
	})

	t.Run("STArray over cap rejected", func(t *testing.T) {
		memos := make([]any, 513)
		for i := range memos {
			memos[i] = map[string]any{"Memo": map[string]any{"MemoData": "AB"}}
		}
		_, err := Encode(map[string]any{"Memos": memos})
		msg, ok := AsArrayTooLargeError(err)
		if !ok {
			t.Fatalf("expected JSONArrayTooLargeError, got %v", err)
		}
		if !strings.Contains(msg, "Field 'Memos'") {
			t.Fatalf("expected Memos in message, got %q", msg)
		}
	})

	t.Run("PathSet outer over cap rejected", func(t *testing.T) {
		paths := make([]any, 513)
		for i := range paths {
			paths[i] = []any{map[string]any{"currency": "USD", "issuer": issuer}}
		}
		_, err := Encode(map[string]any{"Paths": paths})
		if _, ok := AsArrayTooLargeError(err); !ok {
			t.Fatalf("expected JSONArrayTooLargeError for outer Paths, got %v", err)
		}
	})

	t.Run("PathSet inner path over cap rejected with index", func(t *testing.T) {
		inner := make([]any, 513)
		for i := range inner {
			inner[i] = map[string]any{"account": issuer}
		}
		_, err := Encode(map[string]any{"Paths": []any{inner}})
		msg, ok := AsArrayTooLargeError(err)
		if !ok {
			t.Fatalf("expected JSONArrayTooLargeError for inner path, got %v", err)
		}
		if !strings.Contains(msg, "Paths[0]") {
			t.Fatalf("expected inner path index in message, got %q", msg)
		}
	})
}
