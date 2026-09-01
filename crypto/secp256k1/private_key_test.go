package secp256k1

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
)

const secp256k1OrderHex = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141"

func decodePrivateScalar(t *testing.T, value string) []byte {
	t.Helper()
	scalar, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode private scalar: %v", err)
	}
	return scalar
}

func TestSigningRejectsInvalidPrivateScalars(t *testing.T) {
	t.Parallel()

	message := []byte("private scalar bounds")
	digest := sha512half.Sum(message)
	invalid := []struct {
		name   string
		scalar string
	}{
		{name: "zero", scalar: "0000000000000000000000000000000000000000000000000000000000000000"},
		{name: "order", scalar: secp256k1OrderHex},
		{name: "order plus one", scalar: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364142"},
		{name: "all ff", scalar: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"},
	}

	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			scalar := decodePrivateScalar(t, test.scalar)
			if _, err := (Algorithm{}).DerivePublicKeyFromSecret(scalar); !errors.Is(err, ErrInvalidPrivateKey) {
				t.Fatalf("DerivePublicKeyFromSecret got error %v, want %v", err, ErrInvalidPrivateKey)
			}
			type signer struct {
				name string
				sign func() error
			}
			signers := []signer{
				{name: "SignBytes", sign: func() error {
					_, err := Algorithm{}.SignBytes(message, scalar)
					return err
				}},
				{name: "SignDigestBytes", sign: func() error {
					_, err := SignDigestBytes(digest[:], scalar)
					return err
				}},
			}
			for _, encoded := range []struct {
				name string
				key  string
			}{
				{name: "bare", key: test.scalar},
				{name: "prefixed", key: "00" + test.scalar},
			} {
				signers = append(signers,
					signer{name: "Sign/" + encoded.name, sign: func() error {
						_, err := Algorithm{}.Sign(string(message), encoded.key)
						return err
					}},
					signer{name: "SignDigest/" + encoded.name, sign: func() error {
						_, err := Algorithm{}.SignDigest(digest, encoded.key)
						return err
					}},
				)
			}

			for _, signer := range signers {
				t.Run(signer.name, func(t *testing.T) {
					if err := signer.sign(); !errors.Is(err, ErrInvalidPrivateKey) {
						t.Fatalf("got error %v, want %v", err, ErrInvalidPrivateKey)
					}
				})
			}
		})
	}
}

func TestSigningAcceptsPrivateScalarBounds(t *testing.T) {
	t.Parallel()

	message := []byte("private scalar bounds")
	digest := sha512half.Sum(message)
	valid := []struct {
		name   string
		scalar string
	}{
		{name: "one", scalar: "0000000000000000000000000000000000000000000000000000000000000001"},
		{name: "order minus one", scalar: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364140"},
	}

	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			algo := Algorithm{}
			scalar := decodePrivateScalar(t, test.scalar)
			publicKey, err := algo.DerivePublicKeyFromSecret(scalar)
			if err != nil {
				t.Fatalf("derive public key: %v", err)
			}

			signature, err := algo.SignBytes(message, scalar)
			if err != nil {
				t.Fatalf("SignBytes: %v", err)
			}
			if !algo.ValidateBytes(message, publicKey, signature) {
				t.Fatal("ValidateBytes rejected SignBytes signature")
			}

			signature, err = SignDigestBytes(digest[:], scalar)
			if err != nil {
				t.Fatalf("SignDigestBytes: %v", err)
			}
			if !VerifyDigestBytes(digest[:], publicKey, signature) {
				t.Fatal("VerifyDigestBytes rejected SignDigestBytes signature")
			}

			for _, encoded := range []struct {
				name string
				key  string
			}{
				{name: "bare", key: test.scalar},
				{name: "prefixed", key: "00" + test.scalar},
			} {
				t.Run(encoded.name, func(t *testing.T) {
					signatureHex, err := algo.Sign(string(message), encoded.key)
					if err != nil {
						t.Fatalf("Sign: %v", err)
					}
					if !algo.Validate(string(message), hex.EncodeToString(publicKey), signatureHex) {
						t.Fatal("Validate rejected Sign signature")
					}

					signature, err := algo.SignDigest(digest, encoded.key)
					if err != nil {
						t.Fatalf("SignDigest: %v", err)
					}
					if !VerifyDigestBytes(digest[:], publicKey, signature) {
						t.Fatal("VerifyDigestBytes rejected SignDigest signature")
					}
				})
			}
		})
	}
}
