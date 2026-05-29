package nftoken

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func mustHexID(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	if len(b) != 32 {
		t.Fatalf("hex %q decodes to %d bytes, want 32", s, len(b))
	}
	var id [32]byte
	copy(id[:], b)
	return id
}

func mustHexIssuer(t *testing.T, s string) [20]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	if len(b) != 20 {
		t.Fatalf("hex %q decodes to %d bytes, want 20", s, len(b))
	}
	var acct [20]byte
	copy(acct[:], b)
	return acct
}

// TestGenerateNFTokenID_KnownVectors pins the NFTokenID composition against
// known-good vectors. The first is the NFTokenID rippled mints in its
// NFTokenAllFeatures conformance suite; the suite's JSON lives in rippled's
// external test vectors, but the signed burn blob carrying this exact ID is
// vendored in-repo at internal/testing/nft/conformance_debug_test.go. The
// second is the all-zero case where the scrambled taxon reduces to the cipher
// constant c = 2459 (0x099B).
// Reference: rippled nft.h cipheredTaxon / NFTokenMint.cpp createNFTokenID.
func TestGenerateNFTokenID_KnownVectors(t *testing.T) {
	tests := []struct {
		name        string
		issuer      string
		taxon       uint32
		sequence    uint32
		flags       uint16
		transferFee uint16
		want        string
	}{
		{
			// From the NFTokenAllFeatures conformance suite: seq=1, taxon=0, scrambled = cipheredTaxon(1,0) = 0x16E5DA9C.
			name:     "conformance_fixture",
			issuer:   "B5F762798A53D543A014CAF8B297CFF8F2F937E8",
			taxon:    0,
			sequence: 1,
			want:     "00000000B5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001",
		},
		{
			// seq=0, taxon=0 → scrambled = c = 2459 = 0x0000099B.
			name:     "zero_seq_zero_taxon",
			issuer:   "0000000000000000000000000000000000000000",
			taxon:    0,
			sequence: 0,
			want:     "0000000000000000000000000000000000000000000000000000099B00000000",
		},
		{
			// Non-zero flags + transfer fee occupy the first four bytes.
			name:        "flags_and_fee",
			issuer:      "B5F762798A53D543A014CAF8B297CFF8F2F937E8",
			taxon:       0,
			sequence:    1,
			flags:       nftFlagBurnable | nftFlagOnlyXRP | nftFlagTransferable, // 0x000B
			transferFee: 314,                                                    // 0x013A
			want:        "000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issuer := mustHexIssuer(t, tc.issuer)
			got := generateNFTokenID(issuer, tc.taxon, tc.sequence, tc.flags, tc.transferFee)
			want := mustHexID(t, tc.want)
			if got != want {
				t.Errorf("generateNFTokenID = %X, want %X", got, want)
			}
			// Exported wrapper must agree with the package-internal generator.
			if GenerateNFTokenID(issuer, tc.taxon, tc.sequence, tc.flags, tc.transferFee) != got {
				t.Errorf("GenerateNFTokenID disagrees with generateNFTokenID")
			}
		})
	}
}

// TestGenerateNFTokenID_FieldLayout verifies each field lands at its documented
// byte offset: Flags[0:2] | TransferFee[2:4] | Issuer[4:24] | scrambledTaxon[24:28] | Sequence[28:32].
func TestGenerateNFTokenID_FieldLayout(t *testing.T) {
	issuer := mustHexIssuer(t, "0102030405060708090A0B0C0D0E0F1011121314")
	const (
		taxon       = uint32(0xDEADBEEF)
		sequence    = uint32(0x01020304)
		flags       = uint16(0x000B)
		transferFee = uint16(0x1388) // 5000 = 5%
	)
	id := generateNFTokenID(issuer, taxon, sequence, flags, transferFee)

	if got := getNFTFlagsFromID(id); got != flags {
		t.Errorf("flags = %#04x, want %#04x", got, flags)
	}
	if got := getNFTTransferFee(id); got != transferFee {
		t.Errorf("transferFee = %d, want %d", got, transferFee)
	}
	if got := getNFTIssuer(id); got != issuer {
		t.Errorf("issuer = %X, want %X", got, issuer)
	}
	// The plaintext taxon is recovered by reversing the cipher over bytes [24:28].
	scrambled := uint32(id[24])<<24 | uint32(id[25])<<16 | uint32(id[26])<<8 | uint32(id[27])
	if got := cipheredTaxon(sequence, scrambled); got != taxon {
		t.Errorf("recovered taxon = %#08x, want %#08x", got, taxon)
	}
	// Sequence occupies the final four bytes, big-endian.
	gotSeq := uint32(id[28])<<24 | uint32(id[29])<<16 | uint32(id[30])<<8 | uint32(id[31])
	if gotSeq != sequence {
		t.Errorf("sequence = %#08x, want %#08x", gotSeq, sequence)
	}
}

// TestCipheredTaxon_Vectors pins the LCG output for known (sequence, taxon) pairs.
func TestCipheredTaxon_Vectors(t *testing.T) {
	tests := []struct {
		seq, taxon, want uint32
	}{
		{0, 0, 2459},       // c = 2459
		{1, 0, 0x16E5DA9C}, // m + c = 384160001 + 2459
		{0, 0xFFFFFFFF, ^uint32(2459)},
	}
	for _, tc := range tests {
		if got := cipheredTaxon(tc.seq, tc.taxon); got != tc.want {
			t.Errorf("cipheredTaxon(%d, %#x) = %#x, want %#x", tc.seq, tc.taxon, got, tc.want)
		}
		// The exported wrapper must match.
		if got := CipheredTaxon(tc.seq, tc.taxon); got != tc.want {
			t.Errorf("CipheredTaxon(%d, %#x) = %#x, want %#x", tc.seq, tc.taxon, got, tc.want)
		}
	}
}

// TestCipheredTaxon_Involution verifies the cipher is its own inverse — applying
// it twice with the same sequence recovers the original taxon (it is an XOR).
// Reference: rippled nft.h getTaxon comment.
func TestCipheredTaxon_Involution(t *testing.T) {
	seqs := []uint32{0, 1, 2, 100, 0x7FFFFFFF, 0xFFFFFFFF}
	taxons := []uint32{0, 1, 42, 0xDEADBEEF, 0xFFFFFFFF}
	for _, seq := range seqs {
		for _, taxon := range taxons {
			scrambled := cipheredTaxon(seq, taxon)
			if got := cipheredTaxon(seq, scrambled); got != taxon {
				t.Errorf("cipheredTaxon(%d, cipheredTaxon(%d, %#x)) = %#x, want %#x",
					seq, seq, taxon, got, taxon)
			}
		}
	}
}

// TestGetNFTokenFlags_HexParse checks the string-based flag extractor (first
// four hex chars) and its agreement with the byte-based extractor.
func TestGetNFTokenFlags_HexParse(t *testing.T) {
	tests := []struct {
		id   string
		want uint16
	}{
		{"000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001", 0x000B},
		{"0008000000000000000000000000000000000000000000000000099B00000000", 0x0008},
		{"abcd", 0xABCD}, // lowercase
		{"00", 0},        // too short → 0
		{"", 0},
	}
	for _, tc := range tests {
		if got := getNFTokenFlags(tc.id); got != tc.want {
			t.Errorf("getNFTokenFlags(%q) = %#04x, want %#04x", tc.id, got, tc.want)
		}
	}

	// String and byte extractors must agree for a full ID.
	full := "000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001"
	if getNFTokenFlags(full) != getNFTFlagsFromID(mustHexID(t, full)) {
		t.Errorf("string and byte flag extractors disagree")
	}
}

// TestGetNFTPageKey verifies that the page key keeps only the low 96 bits
// (bytes 20-31) and zeroes the high 160 bits.
func TestGetNFTPageKey(t *testing.T) {
	id := mustHexID(t, "000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001")
	key := getNFTPageKey(id)

	for i := 0; i < 20; i++ {
		if key[i] != 0 {
			t.Errorf("page key byte %d = %#02x, want 0 (high 160 bits must be zero)", i, key[i])
		}
	}
	for i := 20; i < 32; i++ {
		if key[i] != id[i] {
			t.Errorf("page key byte %d = %#02x, want %#02x (low 96 bits preserved)", i, key[i], id[i])
		}
	}

	// All-ones low bits, arbitrary high bits → only low 96 bits survive.
	allOnes := mustHexID(t, "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	k2 := getNFTPageKey(allOnes)
	for i := 0; i < 20; i++ {
		if k2[i] != 0 {
			t.Errorf("byte %d = %#02x, want 0", i, k2[i])
		}
	}
	for i := 20; i < 32; i++ {
		if k2[i] != 0xFF {
			t.Errorf("byte %d = %#02x, want 0xFF", i, k2[i])
		}
	}
}

// TestCompareNFTokenID checks rippled's sort order: low 96 bits first, then the
// full 256-bit ID as tiebreaker.
// Reference: rippled NFTokenUtils.cpp compareTokens.
func TestCompareNFTokenID(t *testing.T) {
	// a and b share low 96 bits; they differ only in the high bytes, so the
	// full-ID tiebreaker decides (a < b).
	a := mustHexID(t, "0000000000000000000000000000000000000000000000000000000000000001")
	b := mustHexID(t, "0100000000000000000000000000000000000000000000000000000000000001")
	if got := compareNFTokenID(a, b); got >= 0 {
		t.Errorf("compareNFTokenID(a,b) = %d, want < 0 (tiebreak on full ID)", got)
	}
	if got := compareNFTokenID(b, a); got <= 0 {
		t.Errorf("compareNFTokenID(b,a) = %d, want > 0", got)
	}
	if got := compareNFTokenID(a, a); got != 0 {
		t.Errorf("compareNFTokenID(a,a) = %d, want 0", got)
	}

	// c has larger low 96 bits than a; low-96 comparison dominates even though
	// c's high bytes are smaller.
	c := mustHexID(t, "0000000000000000000000000000000000000000000000000000000000000002")
	if got := compareNFTokenID(a, c); got >= 0 {
		t.Errorf("compareNFTokenID(a,c) = %d, want < 0 (low 96 bits dominate)", got)
	}
}

// TestInsertNFTokenSorted checks that insertion maintains compareNFTokenID order
// regardless of insertion sequence.
func TestInsertNFTokenSorted(t *testing.T) {
	ids := []string{
		"0000000000000000000000000000000000000000000000000000000000000003",
		"0000000000000000000000000000000000000000000000000000000000000001",
		"0000000000000000000000000000000000000000000000000000000000000004",
		"0000000000000000000000000000000000000000000000000000000000000002",
	}
	var tokens []state.NFTokenData
	for _, s := range ids {
		tokens = insertNFTokenSorted(tokens, state.NFTokenData{NFTokenID: mustHexID(t, s)})
	}

	if len(tokens) != len(ids) {
		t.Fatalf("got %d tokens, want %d", len(tokens), len(ids))
	}
	for i := 1; i < len(tokens); i++ {
		if compareNFTokenID(tokens[i-1].NFTokenID, tokens[i].NFTokenID) >= 0 {
			t.Errorf("tokens not sorted at index %d: %X >= %X",
				i, tokens[i-1].NFTokenID, tokens[i].NFTokenID)
		}
	}
}

// TestUint256Next checks 256-bit increment with carry propagation.
func TestUint256Next(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no_carry",
			in:   "0000000000000000000000000000000000000000000000000000000000000001",
			want: "0000000000000000000000000000000000000000000000000000000000000002",
		},
		{
			name: "carry_low_byte",
			in:   "00000000000000000000000000000000000000000000000000000000000000FF",
			want: "0000000000000000000000000000000000000000000000000000000000000100",
		},
		{
			name: "carry_multi_byte",
			in:   "000000000000000000000000000000000000000000000000000000000000FFFF",
			want: "0000000000000000000000000000000000000000000000000000000000010000",
		},
		{
			name: "wrap_around",
			in:   "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
			want: "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := uint256Next(mustHexID(t, tc.in)); got != mustHexID(t, tc.want) {
				t.Errorf("uint256Next(%s) = %X, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestNFTransferFeeXRP exercises the round-half-even (banker's rounding) used
// for the issuer's XRP transfer-fee cut.
// Reference: rippled Rate2.cpp / Number.cpp operator rep().
func TestNFTransferFeeXRP(t *testing.T) {
	tests := []struct {
		name   string
		amount uint64
		fee    uint16
		want   uint64
	}{
		{"zero_fee", 1_000_000, 0, 0},
		{"exact_half", 1_000_000, 50000, 500_000}, // 1e6 * 0.5, no remainder
		{"five_percent", 1_000_000, 5000, 50_000},
		{"round_down_below_half", 2, 20000, 0}, // 40000/100000 = 0.4 → 0
		{"round_up_above_half", 2, 30000, 1},   // 60000/100000 = 0.6 → 1
		{"tie_to_even_down", 1, 50000, 0},      // 50000/100000 = 0.5 → ties to even (0)
		{"tie_to_even_up", 3, 50000, 2},        // 150000/100000 = 1.5 → ties to even (2)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nftTransferFeeXRP(tc.amount, tc.fee); got != tc.want {
				t.Errorf("nftTransferFeeXRP(%d, %d) = %d, want %d", tc.amount, tc.fee, got, tc.want)
			}
		})
	}
}
