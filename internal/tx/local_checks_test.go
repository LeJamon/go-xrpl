package tx

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func memoCommon(memos ...Memo) *Common {
	wrapped := make([]MemoWrapper, len(memos))
	for i, m := range memos {
		wrapped[i] = MemoWrapper{Memo: m}
	}
	return &Common{Account: "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS", Memos: wrapped}
}

// TestPassesLocalChecks_MemoSize pins the rippled isMemoOkay boundary: the whole
// Memos array serialized (headers included, per-object end markers, but not the
// sfMemos field header or array end marker) must be <= 1024 bytes. A MemoData of
// 1019 bytes serializes to exactly 1024; 1020 bytes to 1025.
func TestPassesLocalChecks_MemoSize(t *testing.T) {
	tests := []struct {
		name      string
		dataBytes int
		want      ter.Result
	}{
		{"at the 1024-byte boundary", 1019, ter.TesSUCCESS},
		{"one byte over the boundary", 1020, ter.TemMALFORMED},
		{"well under the boundary", 100, ter.TesSUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common := memoCommon(Memo{MemoData: strings.Repeat("AA", tt.dataBytes)})
			if got := PassesLocalChecks(common); got != tt.want {
				t.Fatalf("PassesLocalChecks = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPassesLocalChecks_MemoFields pins the per-field hex and charset rules:
// every field must be valid hex, and the decoded MemoType/MemoFormat bytes must
// be RFC 3986 URL characters — MemoData is unrestricted. There are no per-field
// size caps.
func TestPassesLocalChecks_MemoFields(t *testing.T) {
	tests := []struct {
		name string
		memo Memo
		want ter.Result
	}{
		{"no memos", Memo{}, ter.TesSUCCESS},
		{"valid hex fields", Memo{MemoType: "41", MemoData: "42", MemoFormat: "43"}, ter.TesSUCCESS},
		{"MemoType not hex", Memo{MemoType: "GG"}, ter.TemMALFORMED},
		{"MemoData not hex", Memo{MemoData: "XY"}, ter.TemMALFORMED},
		{"MemoFormat not hex", Memo{MemoFormat: "ZZ"}, ter.TemMALFORMED},
		// 0x01 is a valid byte but not an RFC 3986 URL character.
		{"MemoType non-URL char", Memo{MemoType: "01"}, ter.TemMALFORMED},
		{"MemoFormat non-URL char", Memo{MemoFormat: "01"}, ter.TemMALFORMED},
		// MemoData may hold arbitrary bytes, including non-URL characters.
		{"MemoData non-URL char", Memo{MemoData: "01"}, ter.TesSUCCESS},
		// No per-field cap: a MemoType larger than the retired 256-byte cap is
		// fine as long as the whole array fits in 1024 serialized bytes and it is
		// valid hex of URL characters ('A' = 0x41).
		{"large MemoType within array budget", Memo{MemoType: strings.Repeat("41", 300)}, ter.TesSUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common := memoCommon(tt.memo)
			if got := PassesLocalChecks(common); got != tt.want {
				t.Fatalf("PassesLocalChecks = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPassesLocalChecks_NoMemos confirms a transaction without memos passes.
func TestPassesLocalChecks_NoMemos(t *testing.T) {
	if got := PassesLocalChecks(&Common{Account: "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"}); got != ter.TesSUCCESS {
		t.Fatalf("PassesLocalChecks(no memos) = %v, want TesSUCCESS", got)
	}
}
