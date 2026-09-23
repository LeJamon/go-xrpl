//go:build cgo

package shim

import (
	"bytes"
	"encoding/hex"
	"sync"
	"testing"
)

const (
	testMsg       = "Hello World"
	testPubHex    = "02950F4710101A25073BF37086D73FBBD00C7A6B0F91097D8F0BC6D268C400D56E"
	testSigDERHex = "3045022100E1617F1A3C85B5BC8FA6224F893FE9068BEA8F8D075EE144F6F9D255C829761802206FD9B361CDE83A0C3D5654232F1D7CFB1A614E9A8F9B1A861564029065516E64"
	testSecretHex = "B167A9F3B9E60A4F93695713682C102438620AA1785C3AE635F53E5B6261071A"
	testOrderHex  = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141"
)

func sha512HalfStr(s string) [32]byte {
	return sha512HalfBytes([]byte(s))
}

func TestSecretKeyValid(t *testing.T) {
	valid := mustHex(t, testSecretHex)
	if !SecretKeyValid(valid) {
		t.Fatal("expected valid secret scalar")
	}
	cases := [][]byte{
		nil,
		make([]byte, 31),
		make([]byte, 33),
		make([]byte, 32),
		mustHex(t, testOrderHex),
	}
	for i, secret := range cases {
		if SecretKeyValid(secret) {
			t.Fatalf("case %d: accepted invalid secret %X", i, secret)
		}
	}
}

func TestSecretKeyReduce(t *testing.T) {
	cases := []struct {
		name string
		in   string
		out  string
		ok   bool
	}{
		{"zero", "0000000000000000000000000000000000000000000000000000000000000000", "", false},
		{"one", "0000000000000000000000000000000000000000000000000000000000000001", "0000000000000000000000000000000000000000000000000000000000000001", true},
		{"order minus one", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140", true},
		{"order", testOrderHex, "", false},
		{"order plus one", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364142", "0000000000000000000000000000000000000000000000000000000000000001", true},
		{"all ff", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", "000000000000000000000000000000014551231950B75FC4402DA1732FC9BEBE", true},
		{"two to the 255", "8000000000000000000000000000000000000000000000000000000000000000", "8000000000000000000000000000000000000000000000000000000000000000", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := mustHex(t, tc.in)
			got, ok := SecretKeyReduce(candidate)
			if ok != tc.ok {
				t.Fatalf("SecretKeyReduce ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				if got != nil {
					t.Fatalf("SecretKeyReduce returned output for invalid candidate: %X", got)
				}
				return
			}
			want := mustHex(t, tc.out)
			if !bytes.Equal(got, want) {
				t.Fatalf("SecretKeyReduce = %X, want %X", got, want)
			}
			candidate[0] ^= 0xff
			if !bytes.Equal(got, want) {
				t.Fatal("reduced key aliases candidate input")
			}
		})
	}
	if got, ok := SecretKeyReduce(make([]byte, 31)); ok || got != nil {
		t.Fatalf("accepted short candidate: %X, %v", got, ok)
	}
}

func TestPublicKeyCreateAndParse(t *testing.T) {
	secret := mustHex(t, testSecretHex)
	public, ok := PublicKeyCreate(secret)
	if !ok {
		t.Fatal("PublicKeyCreate rejected valid secret")
	}
	expected := mustHex(t, testPubHex)
	if got := hex.EncodeToString(public); !bytes.Equal(public, mustHex(t, testPubHex)) {
		t.Fatalf("public key = %s, want %s", got, testPubHex)
	}

	uncompressed, ok := ParsePublicKey(public, false)
	if !ok || len(uncompressed) != 65 || uncompressed[0] != 0x04 {
		t.Fatalf("ParsePublicKey(uncompressed) = %X, %v", uncompressed, ok)
	}
	reencoded, ok := ParsePublicKey(uncompressed, true)
	if !ok || !bytes.Equal(reencoded, public) {
		t.Fatalf("ParsePublicKey(compressed) = %X, %v, want %X", reencoded, ok, public)
	}

	// libsecp256k1 also accepts hybrid encodings when the parity byte agrees
	// with the encoded Y coordinate. A mismatched parity byte must be rejected.
	hybrid := append([]byte{0x06}, uncompressed[1:]...)
	if got, ok := ParsePublicKey(hybrid, false); !ok || !bytes.Equal(got, uncompressed) {
		t.Fatalf("ParsePublicKey(valid hybrid, uncompressed) = %X, %v, want %X", got, ok, uncompressed)
	}
	if got, ok := ParsePublicKey(hybrid, true); !ok || !bytes.Equal(got, public) {
		t.Fatalf("ParsePublicKey(valid hybrid, compressed) = %X, %v, want %X", got, ok, public)
	}
	wrongHybridParity := append([]byte(nil), hybrid...)
	wrongHybridParity[0] = 0x07
	if got, ok := ParsePublicKey(wrongHybridParity, true); ok || got != nil {
		t.Fatalf("accepted hybrid key with wrong parity: %X, %v", got, ok)
	}

	secret[0] ^= 0xff
	if got := hex.EncodeToString(public); !bytes.Equal(public, expected) {
		t.Fatalf("public key changed after source mutation: %s", got)
	}
	uncompressed[1] ^= 0xff
	if got, ok := ParsePublicKey(public, true); !ok || !bytes.Equal(got, mustHex(t, testPubHex)) {
		t.Fatalf("public key parse changed after output mutation: %X, %v", got, ok)
	}

	for _, malformed := range [][]byte{
		nil, []byte{0x02}, make([]byte, 32), append([]byte{0x04}, make([]byte, 64)...),
	} {
		if got, ok := ParsePublicKey(malformed, true); ok || got != nil {
			t.Fatalf("accepted malformed public key %X: %X, %v", malformed, got, ok)
		}
	}
}

func TestSignDigestDeterministic(t *testing.T) {
	secret := mustHex(t, testSecretHex)
	digest := sha512HalfStr(testMsg)
	want := mustHex(t, testSigDERHex)

	first, ok := SignDigest(digest[:], secret)
	if !ok {
		t.Fatal("SignDigest rejected valid input")
	}
	second, ok := SignDigest(digest[:], secret)
	if !ok || !bytes.Equal(first, second) {
		t.Fatalf("SignDigest is not deterministic: %X and %X", first, second)
	}
	if !bytes.Equal(first, want) {
		t.Fatalf("signature = %X, want %X", first, want)
	}
	if !VerifyDigest(digest[:], mustHex(t, testPubHex), first) {
		t.Fatal("VerifyDigest rejected native signature")
	}

	for _, bad := range [][]byte{nil, make([]byte, 31), make([]byte, 33)} {
		if sig, ok := SignDigest(bad, secret); ok || sig != nil {
			t.Fatalf("accepted digest length %d: %X, %v", len(bad), sig, ok)
		}
	}
	if sig, ok := SignDigest(digest[:], make([]byte, 32)); ok || sig != nil {
		t.Fatalf("accepted zero secret: %X, %v", sig, ok)
	}
}

func TestSecretKeyTweakAddAndPublicKeyTweakAdd(t *testing.T) {
	one := make([]byte, 32)
	one[31] = 1
	two := make([]byte, 32)
	two[31] = 2
	three := make([]byte, 32)
	three[31] = 3
	orderMinusOne := mustHex(t, "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140")

	tweaked, ok := SecretKeyTweakAdd(one, two)
	if !ok || !bytes.Equal(tweaked, three) {
		t.Fatalf("secret tweak add = %X, %v, want %X, true", tweaked, ok, three)
	}
	zero := make([]byte, 32)
	same, ok := SecretKeyTweakAdd(one, zero)
	if !ok || !bytes.Equal(same, one) {
		t.Fatalf("zero secret tweak = %X, %v, want %X, true", same, ok, one)
	}
	if result, ok := SecretKeyTweakAdd(one, orderMinusOne); ok || result != nil {
		t.Fatalf("accepted tweak producing zero key: %X, %v", result, ok)
	}
	if result, ok := SecretKeyTweakAdd(one, mustHex(t, testOrderHex)); ok || result != nil {
		t.Fatalf("accepted out-of-range tweak: %X, %v", result, ok)
	}

	pubOne, ok := PublicKeyCreate(one)
	if !ok {
		t.Fatal("PublicKeyCreate(one) failed")
	}
	pubThree, ok := PublicKeyCreate(three)
	if !ok {
		t.Fatal("PublicKeyCreate(three) failed")
	}
	pubTweak, ok := PublicKeyTweakAdd(pubOne, two)
	if !ok || !bytes.Equal(pubTweak, pubThree) {
		t.Fatalf("public tweak add = %X, %v, want %X, true", pubTweak, ok, pubThree)
	}
	pubSame, ok := PublicKeyTweakAdd(pubOne, zero)
	if !ok || !bytes.Equal(pubSame, pubOne) {
		t.Fatalf("zero public tweak = %X, %v, want %X, true", pubSame, ok, pubOne)
	}
	if result, ok := PublicKeyTweakAdd(pubOne, mustHex(t, testOrderHex)); ok || result != nil {
		t.Fatalf("accepted out-of-range public tweak: %X, %v", result, ok)
	}
}

func TestSecretKeyTweakAddBoundaries(t *testing.T) {
	one := make([]byte, 32)
	one[31] = 1
	two := make([]byte, 32)
	two[31] = 2
	three := make([]byte, 32)
	three[31] = 3
	zero := make([]byte, 32)
	order := mustHex(t, testOrderHex)

	tests := []struct {
		name   string
		secret []byte
		tweak  []byte
		ok     bool
	}{
		{"short secret", one[:31], two, false},
		{"long secret", append(append([]byte(nil), one...), 0), two, false},
		{"nil secret", nil, two, false},
		{"zero secret", zero, two, false},
		{"out of range secret", order, two, false},
		{"short tweak", one, two[:31], false},
		{"long tweak", one, append(append([]byte(nil), two...), 0), false},
		{"nil tweak", one, nil, false},
		{"out of range tweak", one, order, false},
		{"valid zero tweak", one, zero, true},
		{"valid addition", one, two, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			secret := append([]byte(nil), tc.secret...)
			tweak := append([]byte(nil), tc.tweak...)
			secretBefore := append([]byte(nil), secret...)
			tweakBefore := append([]byte(nil), tweak...)
			got, ok := SecretKeyTweakAdd(secret, tweak)
			if ok != tc.ok {
				t.Fatalf("SecretKeyTweakAdd ok = %v, want %v; result %X", ok, tc.ok, got)
			}
			if !bytes.Equal(secret, secretBefore) || !bytes.Equal(tweak, tweakBefore) {
				t.Fatalf("tweak add mutated inputs: secret %X (want %X), tweak %X (want %X)", secret, secretBefore, tweak, tweakBefore)
			}
			if !tc.ok {
				if got != nil {
					t.Fatalf("failed tweak returned output: %X", got)
				}
				return
			}
			want := three
			if bytes.Equal(tc.tweak, zero) {
				want = one
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("SecretKeyTweakAdd = %X, want %X", got, want)
			}
			got[0] ^= 0xff
			if !bytes.Equal(secret, secretBefore) || !bytes.Equal(tweak, tweakBefore) {
				t.Fatal("successful tweak output aliases an input")
			}
		})
	}
}

func TestPublicKeyTweakAddBoundaries(t *testing.T) {
	one := make([]byte, 32)
	one[31] = 1
	two := make([]byte, 32)
	two[31] = 2
	three := make([]byte, 32)
	three[31] = 3
	zero := make([]byte, 32)
	orderMinusOne := mustHex(t, "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140")
	order := mustHex(t, testOrderHex)
	pubOne, ok := PublicKeyCreate(one)
	if !ok {
		t.Fatal("PublicKeyCreate(one) failed")
	}
	pubThree, ok := PublicKeyCreate(three)
	if !ok {
		t.Fatal("PublicKeyCreate(three) failed")
	}
	pubOneUncompressed, ok := ParsePublicKey(pubOne, false)
	if !ok {
		t.Fatal("ParsePublicKey(pubOne, false) failed")
	}

	tests := []struct {
		name  string
		pub   []byte
		tweak []byte
		ok    bool
	}{
		{"nil public key", nil, two, false},
		{"short public key", pubOne[:32], two, false},
		{"long public key", append(append([]byte(nil), pubOne...), 0), two, false},
		{"malformed public key", append([]byte{0x02}, make([]byte, 32)...), two, false},
		{"short tweak", pubOne, two[:31], false},
		{"long tweak", pubOne, append(append([]byte(nil), two...), 0), false},
		{"nil tweak", pubOne, nil, false},
		{"out of range tweak", pubOne, order, false},
		{"point at infinity", pubOne, orderMinusOne, false},
		{"valid zero tweak", pubOne, zero, true},
		{"valid compressed addition", pubOne, two, true},
		{"valid uncompressed addition", pubOneUncompressed, two, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pub := append([]byte(nil), tc.pub...)
			tweak := append([]byte(nil), tc.tweak...)
			pubBefore := append([]byte(nil), pub...)
			tweakBefore := append([]byte(nil), tweak...)
			got, ok := PublicKeyTweakAdd(pub, tweak)
			if ok != tc.ok {
				t.Fatalf("PublicKeyTweakAdd ok = %v, want %v; result %X", ok, tc.ok, got)
			}
			if !bytes.Equal(pub, pubBefore) || !bytes.Equal(tweak, tweakBefore) {
				t.Fatalf("public tweak mutated inputs: pub %X (want %X), tweak %X (want %X)", pub, pubBefore, tweak, tweakBefore)
			}
			if !tc.ok {
				if got != nil {
					t.Fatalf("failed public tweak returned output: %X", got)
				}
				return
			}
			want := pubOne
			if !bytes.Equal(tc.tweak, zero) {
				want = pubThree
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("PublicKeyTweakAdd = %X, want %X", got, want)
			}
			got[0] ^= 0xff
			if !bytes.Equal(pub, pubBefore) || !bytes.Equal(tweak, tweakBefore) {
				t.Fatal("successful public tweak output aliases an input")
			}
		})
	}
}

func TestConcurrentSignAndVerify(t *testing.T) {
	secret := mustHex(t, testSecretHex)
	pub := mustHex(t, testPubHex)
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := range 64 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			digest := sha512HalfStr(testMsg)
			digest[0] ^= byte(i)
			sig, ok := SignDigest(digest[:], secret)
			if !ok {
				errs <- "sign failed"
				return
			}
			if !VerifyDigest(digest[:], pub, sig) {
				errs <- "verify failed"
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestVerifyDigest_Valid(t *testing.T) {
	pub := mustHex(t, testPubHex)
	sig := mustHex(t, testSigDERHex)
	digest := sha512HalfStr(testMsg)
	if !VerifyDigest(digest[:], pub, sig) {
		t.Fatal("expected libsecp256k1 to accept the known-good signature")
	}
}

func TestVerifyDigest_RejectsTamperedDigest(t *testing.T) {
	pub := mustHex(t, testPubHex)
	sig := mustHex(t, testSigDERHex)
	digest := sha512HalfStr(testMsg)
	digest[0] ^= 0x01
	if VerifyDigest(digest[:], pub, sig) {
		t.Fatal("expected libsecp256k1 to reject a tampered digest")
	}
}

func TestVerifyDigest_RejectsMalformedSig(t *testing.T) {
	pub := mustHex(t, testPubHex)
	digest := sha512HalfStr(testMsg)
	if VerifyDigest(digest[:], pub, []byte{0x30, 0x00}) {
		t.Fatal("expected libsecp256k1 to reject malformed DER")
	}
}

func TestVerifyDigest_RejectsNoncanonicalPublicKeys(t *testing.T) {
	pub := mustHex(t, testPubHex)
	sig := mustHex(t, testSigDERHex)
	digest := sha512HalfStr(testMsg)
	uncompressed, ok := ParsePublicKey(pub, false)
	if !ok {
		t.Fatal("parse public key")
	}

	invalidPrefix := append([]byte(nil), pub...)
	invalidPrefix[0] = 0x04
	invalidPoint := append([]byte{0x02}, make([]byte, 32)...)
	for i := 1; i < len(invalidPoint); i++ {
		invalidPoint[i] = 0xFF
	}
	keys := []struct {
		name string
		key  []byte
	}{
		{"uncompressed", uncompressed},
		{"invalid prefix", invalidPrefix},
		{"invalid length", pub[:len(pub)-1]},
		{"invalid point", invalidPoint},
	}
	for _, key := range keys {
		t.Run(key.name, func(t *testing.T) {
			if VerifyDigest(digest[:], key.key, sig) {
				t.Fatalf("accepted invalid public key %X", key.key)
			}
		})
	}
}

func TestVerifyDigest_RejectsBadInputs(t *testing.T) {
	if VerifyDigest(nil, []byte{1}, []byte{1}) {
		t.Fatal("nil hash should be rejected")
	}
	hash := make([]byte, 32)
	if VerifyDigest(hash, nil, []byte{1}) {
		t.Fatal("nil pubkey should be rejected")
	}
	if VerifyDigest(hash, []byte{1}, nil) {
		t.Fatal("nil sig should be rejected")
	}
	if VerifyDigest(make([]byte, 31), []byte{1}, []byte{1}) {
		t.Fatal("short hash should be rejected")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return b
}
