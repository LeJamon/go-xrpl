package protocol

import (
	"strings"
	"testing"
)

func TestHash256HexRoundTrip(t *testing.T) {
	var want [32]byte
	for i := range want {
		want[i] = byte(0xa0 + i)
	}
	encoded := Hash256Hex(want)
	if len(encoded) != 64 || encoded != strings.ToUpper(encoded) {
		t.Fatalf("Hash256Hex = %q, want 64 uppercase characters", encoded)
	}
	got, err := Hash256FromHex(encoded)
	if err != nil {
		t.Fatalf("Hash256FromHex: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %x, want %x", got, want)
	}
	gotLower, err := Hash256FromHex(strings.ToLower(encoded))
	if err != nil || gotLower != want {
		t.Fatalf("lowercase parse = %x, %v", gotLower, err)
	}
}

func TestHash256FromHexRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"abcd", strings.Repeat("z", 64), strings.Repeat("ab", 1_000_000)} {
		if _, err := Hash256FromHex(input); err == nil {
			t.Errorf("Hash256FromHex accepted input of length %d", len(input))
		}
	}
}
