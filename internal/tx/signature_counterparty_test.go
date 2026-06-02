package tx

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// keypair holds a prefixed private key, public key and classic address derived
// deterministically from a name.
type keypair struct {
	priv string
	pub  string
	addr string
}

// deriveKey produces a deterministic ed25519 or secp256k1 keypair for a name,
// mirroring the seed derivation used by the test environment (account.go).
func deriveKey(t *testing.T, name, keyType string) keypair {
	t.Helper()
	hash := sha512.Sum512([]byte(name))
	seed := hash[:16]

	var priv, pub string
	var err error
	switch keyType {
	case "ed25519":
		priv, pub, err = ed25519.ED25519().DeriveKeypair(seed, false)
	case "secp256k1":
		priv, pub, err = secp256k1.SECP256K1().DeriveKeypair(seed, false)
	default:
		t.Fatalf("unsupported key type %q", keyType)
	}
	if err != nil {
		t.Fatalf("DeriveKeypair(%s, %s): %v", name, keyType, err)
	}
	addr, err := DeriveAddressFromPublicKey(pub)
	if err != nil {
		t.Fatalf("DeriveAddressFromPublicKey(%s): %v", name, err)
	}
	return keypair{priv: priv, pub: pub, addr: addr}
}

// newSignedTx builds a minimal single-signed AccountSet signed by primary.
func newSignedTx(t *testing.T, primary keypair) *BaseTx {
	t.Helper()
	seq := uint32(1)
	tx := &BaseTx{Common: Common{
		Account:         primary.addr,
		TransactionType: "AccountSet",
		Fee:             "10",
		Sequence:        &seq,
		SigningPubKey:   primary.pub,
	}}
	sig, err := SignTransaction(tx, primary.priv)
	if err != nil {
		t.Fatalf("SignTransaction: %v", err)
	}
	tx.Common.TxnSignature = sig
	if err := VerifySignature(tx); err != nil {
		t.Fatalf("primary signature should be valid: %v", err)
	}
	return tx
}

// flipLastNibble corrupts a hex string while keeping it valid hex, guaranteeing
// the decoded value changes.
func flipLastNibble(s string) string {
	if s == "" {
		return "FF"
	}
	b := []byte(s)
	if b[len(b)-1] == '0' {
		b[len(b)-1] = '1'
	} else {
		b[len(b)-1] = '0'
	}
	return string(b)
}

func TestCounterpartySignature_AbsentUnchanged(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))

	if err := VerifyCounterpartySignature(tx, amendment.AllSupportedRules()); err != nil {
		t.Fatalf("absent counterparty signature should pass, got %v", err)
	}
	e := &Engine{config: EngineConfig{SkipSignatureVerification: false, Rules: amendment.AllSupportedRules()}}
	if r := e.verifySignatures(tx); r != TesSUCCESS {
		t.Fatalf("verifySignatures = %v, want TesSUCCESS", r)
	}
}

func TestCounterpartySignature_ValidSingle(t *testing.T) {
	for _, kt := range []string{"ed25519", "secp256k1"} {
		t.Run(kt, func(t *testing.T) {
			tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
			cp := deriveKey(t, "counterparty", kt)
			if err := SignCounterparty(tx, cp.pub, cp.priv); err != nil {
				t.Fatalf("SignCounterparty: %v", err)
			}
			if err := VerifyCounterpartySignature(tx, amendment.AllSupportedRules()); err != nil {
				t.Fatalf("valid counterparty signature should pass, got %v", err)
			}
			e := &Engine{config: EngineConfig{SkipSignatureVerification: false, Rules: amendment.AllSupportedRules()}}
			if r := e.verifySignatures(tx); r != TesSUCCESS {
				t.Fatalf("verifySignatures = %v, want TesSUCCESS", r)
			}
		})
	}
}

func TestCounterpartySignature_InvalidSingle(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
	cp := deriveKey(t, "counterparty", "ed25519")
	if err := SignCounterparty(tx, cp.pub, cp.priv); err != nil {
		t.Fatalf("SignCounterparty: %v", err)
	}
	// Corrupt the counterparty signature.
	tx.Common.CounterpartySignature.TxnSignature = flipLastNibble(tx.Common.CounterpartySignature.TxnSignature)

	err := VerifyCounterpartySignature(tx, amendment.AllSupportedRules())
	if err == nil {
		t.Fatal("invalid counterparty signature should fail")
	}
	if !strings.HasPrefix(err.Error(), "Counterparty: ") {
		t.Fatalf("error %q must be prefixed with \"Counterparty: \"", err.Error())
	}
	e := &Engine{config: EngineConfig{SkipSignatureVerification: false, Rules: amendment.AllSupportedRules()}}
	if r := e.verifySignatures(tx); r != TemBAD_SIGNATURE {
		t.Fatalf("verifySignatures = %v, want TemBAD_SIGNATURE", r)
	}
}

// TestCounterpartySignature_InvalidTopLevel proves the primary signature is
// checked first: a bad top-level signature is rejected even when the counterparty
// signature is valid, mirroring rippled STTx::checkSign ordering.
func TestCounterpartySignature_InvalidTopLevel(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
	cp := deriveKey(t, "counterparty", "ed25519")
	if err := SignCounterparty(tx, cp.pub, cp.priv); err != nil {
		t.Fatalf("SignCounterparty: %v", err)
	}
	// Corrupt the top-level signature; counterparty remains valid.
	tx.Common.TxnSignature = flipLastNibble(tx.Common.TxnSignature)

	e := &Engine{config: EngineConfig{SkipSignatureVerification: false, Rules: amendment.AllSupportedRules()}}
	if r := e.verifySignatures(tx); r != TemBAD_SIGNATURE {
		t.Fatalf("verifySignatures = %v, want TemBAD_SIGNATURE", r)
	}
}

// signCounterpartyMulti attaches a sorted multi-signed counterparty object.
func signCounterpartyMulti(t *testing.T, tx *BaseTx, signers []keypair) {
	t.Helper()
	wrappers := make([]SignerWrapper, 0, len(signers))
	for _, s := range signers {
		sig, err := SignTransactionForMultiSign(tx, s.addr, s.priv)
		if err != nil {
			t.Fatalf("SignTransactionForMultiSign(%s): %v", s.addr, err)
		}
		wrappers = append(wrappers, SignerWrapper{Signer: Signer{
			Account:       s.addr,
			SigningPubKey: s.pub,
			TxnSignature:  sig,
		}})
	}
	sort.Slice(wrappers, func(i, j int) bool {
		idI, _ := state.DecodeAccountID(wrappers[i].Signer.Account)
		idJ, _ := state.DecodeAccountID(wrappers[j].Signer.Account)
		return bytes.Compare(idI[:], idJ[:]) < 0
	})
	tx.Common.CounterpartySignature = &CounterpartySignature{Signers: wrappers}
}

func TestCounterpartySignature_ValidMulti(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
	signers := []keypair{
		deriveKey(t, "cp-signer-1", "ed25519"),
		deriveKey(t, "cp-signer-2", "secp256k1"),
	}
	signCounterpartyMulti(t, tx, signers)

	if err := VerifyCounterpartySignature(tx, amendment.AllSupportedRules()); err != nil {
		t.Fatalf("valid multi-signed counterparty should pass, got %v", err)
	}
}

func TestCounterpartySignature_InvalidMulti(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
	signers := []keypair{
		deriveKey(t, "cp-signer-1", "ed25519"),
		deriveKey(t, "cp-signer-2", "ed25519"),
	}
	signCounterpartyMulti(t, tx, signers)
	// Corrupt one signer's signature.
	cs := tx.Common.CounterpartySignature
	cs.Signers[0].Signer.TxnSignature = flipLastNibble(cs.Signers[0].Signer.TxnSignature)

	err := VerifyCounterpartySignature(tx, amendment.AllSupportedRules())
	if err == nil {
		t.Fatal("invalid multi-signed counterparty should fail")
	}
	if !strings.HasPrefix(err.Error(), "Counterparty: ") {
		t.Fatalf("error %q must be prefixed with \"Counterparty: \"", err.Error())
	}
}

// TestCounterpartySignature_SignerCountBound enforces rippled multiSignHelper's
// signer-count bound (STTx.cpp:495-497) on a multi-signed counterparty object:
// the array is rejected above maxMultiSigners, which is rules-aware (8 by
// default, 32 with featureExpandedSignerList). Unlike the top-level path there is
// no signer list to implicitly bound the count, so the cap is enforced directly.
func TestCounterpartySignature_SignerCountBound(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
	signers := make([]keypair, 9)
	for i := range signers {
		signers[i] = deriveKey(t, "cp-many-"+string(rune('a'+i)), "ed25519")
	}
	signCounterpartyMulti(t, tx, signers)

	// 9 signers exceed the default cap of 8 (ExpandedSignerList disabled). The
	// signatures are all valid, so a rejection proves the size bound fires first.
	noExpanded := amendment.NewRules(nil)
	err := VerifyCounterpartySignature(tx, noExpanded)
	if err == nil {
		t.Fatal("9-signer counterparty must be rejected when the cap is 8")
	}
	if !strings.HasPrefix(err.Error(), "Counterparty: ") ||
		!strings.Contains(err.Error(), "invalid Signers array size") {
		t.Fatalf("expected wrapped array-size error, got %v", err)
	}

	// The same 9 signers are within the expanded cap of 32.
	withExpanded := amendment.NewRules([][32]byte{amendment.FeatureExpandedSignerList})
	if err := VerifyCounterpartySignature(tx, withExpanded); err != nil {
		t.Fatalf("9-signer counterparty must pass when the cap is 32, got %v", err)
	}
}

// TestCounterpartySignature_BothSign rejects an object that carries both a
// SigningPubKey and Signers, mirroring rippled singleSignHelper.
func TestCounterpartySignature_BothSign(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
	cp := deriveKey(t, "counterparty", "ed25519")
	if err := SignCounterparty(tx, cp.pub, cp.priv); err != nil {
		t.Fatalf("SignCounterparty: %v", err)
	}
	signer := deriveKey(t, "cp-signer-1", "ed25519")
	sig, err := SignTransactionForMultiSign(tx, signer.addr, signer.priv)
	if err != nil {
		t.Fatalf("SignTransactionForMultiSign: %v", err)
	}
	tx.Common.CounterpartySignature.Signers = []SignerWrapper{{Signer: Signer{
		Account:       signer.addr,
		SigningPubKey: signer.pub,
		TxnSignature:  sig,
	}}}

	err = VerifyCounterpartySignature(tx, amendment.AllSupportedRules())
	if err == nil || !strings.Contains(err.Error(), "cannot both single- and multi-sign") {
		t.Fatalf("expected cannot-both-sign error, got %v", err)
	}
}

// TestCounterpartySignature_WireRoundTrip confirms the field survives an
// encode/decode cycle so a node verifies what it received off the wire.
func TestCounterpartySignature_WireRoundTrip(t *testing.T) {
	tx := newSignedTx(t, deriveKey(t, "primary", "ed25519"))
	cp := deriveKey(t, "counterparty", "ed25519")
	if err := SignCounterparty(tx, cp.pub, cp.priv); err != nil {
		t.Fatalf("SignCounterparty: %v", err)
	}

	flat, err := tx.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	encoded, err := binarycodec.Encode(flat)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	blob, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	parsed, err := ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	got := parsed.GetCommon().CounterpartySignature
	if got == nil {
		t.Fatal("CounterpartySignature dropped on round-trip")
	}
	if got.SigningPubKey != cp.pub || got.TxnSignature != tx.Common.CounterpartySignature.TxnSignature {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
	if err := VerifyCounterpartySignature(parsed, amendment.AllSupportedRules()); err != nil {
		t.Fatalf("round-tripped counterparty signature should verify, got %v", err)
	}
}
