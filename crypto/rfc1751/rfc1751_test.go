package rfc1751

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding hex %q: %v", s, err)
	}
	return b
}

func containsWord(phrase, word string) bool {
	return slices.Contains(strings.Fields(phrase), word)
}

// Canonical RFC 1751 Appendix B test vectors. The key bytes encode directly
// to the listed words via KeyToEnglish, and decode back via EnglishToKey.
func TestKeyToEnglishCanonicalVectors(t *testing.T) {
	vectors := []struct {
		keyHex string
		words  string
	}{
		{"CCAC2AED591056BE4F90FD441C534766", "RASH BUSH MILK LOOK BAD BRIM AVID GAFF BAIT ROT POD LOVE"},
		{"EFF81F9BFBC65350920CDD7416DE8009", "TROD MUTE TAIL WARM CHAR KONG HAAG CITY BORE O TEAL AWL"},
	}

	for _, v := range vectors {
		key := mustHex(t, v.keyHex)

		got, err := KeyToEnglish(key)
		if err != nil {
			t.Fatalf("KeyToEnglish(%s): unexpected error: %v", v.keyHex, err)
		}
		if got != v.words {
			t.Errorf("KeyToEnglish(%s) = %q, want %q", v.keyHex, got, v.words)
		}

		decoded, err := EnglishToKey(v.words)
		if err != nil {
			t.Fatalf("EnglishToKey(%q): unexpected error: %v", v.words, err)
		}
		if !bytes.Equal(decoded, key) {
			t.Errorf("EnglishToKey(%q) = %X, want %s", v.words, decoded, v.keyHex)
		}
	}
}

// Cross-implementation vectors lifted verbatim from rippled's
// KeyGeneration_test.cpp. master_seed_hex is the 16-byte seed entropy;
// master_key is what rippled's seedAs1751 (and go-xrpl's SeedToEnglish, used by
// wallet_propose / validation_create) must emit for it.
func TestSeedToEnglishRippledVectors(t *testing.T) {
	vectors := []struct {
		seedHex   string
		masterKey string
	}{
		{"BE6A670A19B209E112146D0A7ED2AAD7", "SCAT BERN ISLE FOR ROIL BUS SOAK AQUA FREE FOR DRAM BRIG"},
		{"74BA8389B44F98CF41E795CD91F9C93F", "TED AVON CAVE HOUR BRAG JEFF RIFT NEAL TOLD FAT SEW SAN"},
	}

	for _, v := range vectors {
		seed := mustHex(t, v.seedHex)

		got, err := SeedToEnglish(seed)
		if err != nil {
			t.Fatalf("SeedToEnglish(%s): unexpected error: %v", v.seedHex, err)
		}
		if got != v.masterKey {
			t.Errorf("SeedToEnglish(%s) = %q, want %q", v.seedHex, got, v.masterKey)
		}

		decoded, err := EnglishToSeed(v.masterKey)
		if err != nil {
			t.Fatalf("EnglishToSeed(%q): unexpected error: %v", v.masterKey, err)
		}
		if !bytes.Equal(decoded, seed) {
			t.Errorf("EnglishToSeed(%q) = %X, want %s", v.masterKey, decoded, v.seedHex)
		}
	}
}

func TestKeyToEnglishRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for range 2000 {
		key := make([]byte, 16)
		rng.Read(key)

		words, err := KeyToEnglish(key)
		if err != nil {
			t.Fatalf("KeyToEnglish(%X): unexpected error: %v", key, err)
		}

		decoded, err := EnglishToKey(words)
		if err != nil {
			t.Fatalf("EnglishToKey(%q): unexpected error: %v", words, err)
		}
		if !bytes.Equal(decoded, key) {
			t.Fatalf("round-trip mismatch: KeyToEnglish(%X) = %q, EnglishToKey -> %X", key, words, decoded)
		}
	}
}

func TestSeedToEnglishRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for range 2000 {
		seed := make([]byte, 16)
		rng.Read(seed)

		words, err := SeedToEnglish(seed)
		if err != nil {
			t.Fatalf("SeedToEnglish(%X): unexpected error: %v", seed, err)
		}

		decoded, err := EnglishToSeed(words)
		if err != nil {
			t.Fatalf("EnglishToSeed(%q): unexpected error: %v", words, err)
		}
		if !bytes.Equal(decoded, seed) {
			t.Fatalf("round-trip mismatch: SeedToEnglish(%X) = %q, EnglishToSeed -> %X", seed, words, decoded)
		}
	}
}

// SeedToEnglish is KeyToEnglish over the byte-reversed seed (rippled's
// seedAs1751 std::reverse_copy in Seed.cpp). Verify that relationship.
func TestSeedToEnglishReversesKey(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for range 200 {
		seed := make([]byte, 16)
		rng.Read(seed)

		reversed := make([]byte, 16)
		for j := range 16 {
			reversed[j] = seed[15-j]
		}

		viaSeed, err := SeedToEnglish(seed)
		if err != nil {
			t.Fatalf("SeedToEnglish: %v", err)
		}
		viaKey, err := KeyToEnglish(reversed)
		if err != nil {
			t.Fatalf("KeyToEnglish: %v", err)
		}
		if viaSeed != viaKey {
			t.Errorf("SeedToEnglish(%X) = %q, want KeyToEnglish(reverse) = %q", seed, viaSeed, viaKey)
		}
	}
}

func TestKeyToEnglishWrongLength(t *testing.T) {
	for _, n := range []int{0, 8, 15, 17, 32} {
		if _, err := KeyToEnglish(make([]byte, n)); err == nil {
			t.Errorf("KeyToEnglish with %d-byte key: expected error, got nil", n)
		}
	}
}

func TestSeedToEnglishWrongLength(t *testing.T) {
	for _, n := range []int{0, 8, 15, 17, 32} {
		if _, err := SeedToEnglish(make([]byte, n)); err == nil {
			t.Errorf("SeedToEnglish with %d-byte seed: expected error, got nil", n)
		}
	}
}

func TestEnglishToKeyMalformed(t *testing.T) {
	const valid = "RASH BUSH MILK LOOK BAD BRIM AVID GAFF BAIT ROT POD LOVE"

	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too few words", "RASH BUSH MILK LOOK BAD BRIM"},
		{"too many words", valid + " EXTRA"},
		{"word longer than 4 chars", "RASHH BUSH MILK LOOK BAD BRIM AVID GAFF BAIT ROT POD LOVE"},
		{"word not in dictionary", "ZZZZ BUSH MILK LOOK BAD BRIM AVID GAFF BAIT ROT POD LOVE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := EnglishToKey(tt.input); err == nil {
				t.Errorf("EnglishToKey(%q): expected error, got nil", tt.input)
			}
		})
	}
}

func TestEnglishToKeyParityError(t *testing.T) {
	const valid = "RASH BUSH MILK LOOK BAD BRIM AVID GAFF BAIT ROT POD LOVE"

	if _, err := EnglishToKey(valid); err != nil {
		t.Fatalf("baseline EnglishToKey failed: %v", err)
	}

	// Substitute the first word with another dictionary word so all six
	// words still resolve but the stored parity no longer matches, exercising
	// the etob parity check (error code -2, matching rippled RFC1751.cpp:446).
	words := strings.Fields(valid)
	found := false
	for _, repl := range dictionary {
		if repl == words[0] {
			continue
		}
		candidate := repl + " " + strings.Join(words[1:], " ")
		if _, err := EnglishToKey(candidate); err != nil && strings.Contains(err.Error(), "error code -2") {
			found = true
			break
		}
	}
	if !found {
		t.Error("could not construct a parity-error input from dictionary substitutions")
	}
}

func TestStandardNormalization(t *testing.T) {
	const canonical = "RASH BUSH MILK LOOK BAD BRIM AVID GAFF BAIT ROT POD LOVE"
	want, err := EnglishToKey(canonical)
	if err != nil {
		t.Fatalf("EnglishToKey(canonical): %v", err)
	}

	// standard() uppercases input, so a lowercased phrase must decode identically.
	got, err := EnglishToKey(strings.ToLower(canonical))
	if err != nil {
		t.Fatalf("EnglishToKey(lowercase): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("lowercase decoded to %X, want %X", got, want)
	}

	// standard() also maps the digit-letter confusions 1->L, 0->O, 5->S
	// (rippled RFC1751.cpp:371-376). A phrase with those digits substituted
	// for the corresponding letters must decode identically.
	const mangled = "RA5H BU5H MI1K 100K BAD BRIM AVID GAFF BAIT R0T P0D 10VE"
	got, err = EnglishToKey(mangled)
	if err != nil {
		t.Fatalf("EnglishToKey(digit-mangled): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("digit-mangled decoded to %X, want %X", got, want)
	}
}

// TestEveryDictionaryWordDecodable proves the etob length-bounded binary search
// reaches every dictionary index, including 570 ("YOU", the last 3-letter word).
// rippled caps the short-word range at 570 exclusive (RFC1751.cpp:433), so its
// etob can encode but never decode "YOU"; go-xrpl deliberately diverges with an
// upper bound of 571 (rfc1751.go etob) so the recovery string for any seed that
// maps a word to index 570 round-trips. RFC1751 is RPC/wallet-recovery only, so
// this non-consensus divergence cannot fork the ledger.
func TestEveryDictionaryWordDecodable(t *testing.T) {
	if dictionary[570] != "YOU" {
		t.Fatalf("dictionary[570] = %q, want %q (boundary assumption broken)", dictionary[570], "YOU")
	}
	if got := wsrch("YOU", 0, 571); got != 570 {
		t.Errorf("wsrch(YOU, 0, 571) = %d, want 570 (index 570 must be decodable)", got)
	}

	for i, w := range dictionary {
		lo, hi := 571, 2048
		if len(w) < 4 {
			lo, hi = 0, 571
		}
		if got := wsrch(standard(w), lo, hi); got != i {
			t.Errorf("wsrch(%q) = %d, want %d (index should be decodable)", w, got, i)
		}
	}
}

// TestIndex570RoundTrips is the concrete regression for the index-570 decode
// bug: a 16-byte key whose 7th word encodes to "YOU" must decode back to the
// same key. Under rippled's bounds EnglishToKey(words) would fail here.
func TestIndex570RoundTrips(t *testing.T) {
	const keyHex = "BB27F5BC497BD654474DCB862AB4DD68"
	key := mustHex(t, keyHex)

	words, err := KeyToEnglish(key)
	if err != nil {
		t.Fatalf("KeyToEnglish(%s): %v", keyHex, err)
	}
	if !containsWord(words, "YOU") {
		t.Fatalf("KeyToEnglish(%s) = %q, expected it to contain \"YOU\"", keyHex, words)
	}

	decoded, err := EnglishToKey(words)
	if err != nil {
		t.Fatalf("EnglishToKey(%q): unexpected error: %v", words, err)
	}
	if !bytes.Equal(decoded, key) {
		t.Errorf("round-trip mismatch: %q decoded to %X, want %s", words, decoded, keyHex)
	}
}
