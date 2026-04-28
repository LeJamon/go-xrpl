package peertls

import (
	"crypto/sha512"
	"strings"
	"testing"
)

// TestComputeSharedValue_MatchesRippled pins the rippled construction
// (Handshake.cpp:127-175): sha512Half(SHA512(local) XOR SHA512(peer))
// where sha512Half is the first 32 bytes of SHA-512.
func TestComputeSharedValue_MatchesRippled(t *testing.T) {
	local := []byte("local_finished_message_payload_")
	peer := []byte("peer__finished_message_payload_")

	sv, err := computeSharedValue(local, peer)
	if err != nil {
		t.Fatalf("computeSharedValue: %v", err)
	}
	if len(sv) != 32 {
		t.Fatalf("shared value must be 32 bytes (sha512Half), got %d", len(sv))
	}

	// Recompute the rippled construction by hand and compare.
	h1 := sha512.Sum512(local)
	h2 := sha512.Sum512(peer)
	var mixed [sha512.Size]byte
	for i := range mixed {
		mixed[i] = h1[i] ^ h2[i]
	}
	final := sha512.Sum512(mixed[:])
	expect := final[:32]
	for i := range expect {
		if sv[i] != expect[i] {
			t.Fatalf("byte %d: got %x want %x", i, sv[i], expect[i])
		}
	}
}

// TestComputeSharedValue_TooShort pins the
// `sslMinimumFinishedLength = 12` rejection
// (Handshake.cpp:130-136).
func TestComputeSharedValue_TooShort(t *testing.T) {
	cases := []struct {
		name        string
		local, peer []byte
	}{
		{"local short", []byte("short"), []byte("valid_finished_msg_xx")},
		{"peer short", []byte("valid_finished_msg_xx"), []byte("short")},
		{"both short", []byte("a"), []byte("b")},
		{"empty local", []byte{}, []byte("valid_finished_msg_xx")},
		{"empty peer", []byte("valid_finished_msg_xx"), []byte{}},
		{"exactly 11 local", make([]byte, 11), make([]byte, 12)},
		{"exactly 11 peer", make([]byte, 12), make([]byte, 11)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := computeSharedValue(tc.local, tc.peer)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "shorter than 12") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestComputeSharedValue_IdenticalRejected pins the rippled XOR-zero
// rejection (Handshake.cpp:167-172): if local and peer Finished hash
// to the same value, the XOR is zero and we must refuse.
func TestComputeSharedValue_IdenticalRejected(t *testing.T) {
	same := []byte("identical_finished_message_payload")
	_, err := computeSharedValue(same, same)
	if err == nil {
		t.Fatalf("identical Finished must be rejected")
	}
	if !strings.Contains(err.Error(), "identical") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestComputeSharedValue_BoundaryValid pins that the 12-byte minimum
// is inclusive — exactly 12 bytes on each side must succeed.
func TestComputeSharedValue_BoundaryValid(t *testing.T) {
	local := []byte("aaaaaaaaaaaa")  // 12 bytes
	peer := []byte("bbbbbbbbbbbb")   // 12 bytes
	sv, err := computeSharedValue(local, peer)
	if err != nil {
		t.Fatalf("12 bytes must be accepted: %v", err)
	}
	if len(sv) != 32 {
		t.Fatalf("shared value must be 32 bytes, got %d", len(sv))
	}
}
