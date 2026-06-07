package host

// These tests port the edge-case error-code assertions from rippled's
// src/test/app/HostFuncImpl_test.cpp (ripple/smart-escrow), so the host
// functions are verified against rippled's own conformance bar rather than only
// a reading of the reference. Each case names the rippled testcase it mirrors.

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

func payment(t *testing.T) []byte {
	return encodeTx(t, map[string]any{
		"TransactionType": "Payment",
		"Account":         testAddr,
		"Destination":     testAddr,
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        uint32(1),
	})
}

// rippled getTxField: kSfInvalid (-1) and kSfGeneric (0) resolve to a field that
// is never present, so they report FieldNotFound, not InvalidField.
func TestConformance_SentinelFieldCodes(t *testing.T) {
	e := New(&mockView{tx: payment(t)})
	for _, code := range []int32{-1, 0} {
		if _, herr := e.GetTxField(code); herr != wasm.HfFieldNotFound {
			t.Errorf("GetTxField(%d) herr = %d, want HfFieldNotFound", code, herr)
		}
		if _, herr := e.GetTxArrayLen(code); herr != wasm.HfNoArray {
			t.Errorf("GetTxArrayLen(%d) herr = %d, want HfNoArray", code, herr)
		}
	}
}

// rippled getAnyFieldData STI_ISSUE: an MPT-holding Issue field returns the bare
// 24-byte mptID, not the wire form.
func TestConformance_MPTIssueField(t *testing.T) {
	_, issuer, err := addresscodec.DecodeClassicAddressToAccountID(testAddr)
	if err != nil {
		t.Fatal(err)
	}
	// mpt_issuance_id = sequence(big-endian, 4) + issuer(20).
	mptID := append([]byte{0x00, 0x00, 0x00, 0x01}, issuer...)
	obj := encodeTx(t, map[string]any{
		"Asset": map[string]any{"mpt_issuance_id": strings.ToUpper(hex.EncodeToString(mptID))},
	})
	e := New(&mockView{tx: obj})

	got, herr := e.GetTxField(fieldCode(t, "Asset"))
	if herr != wasm.HfSuccess {
		t.Fatalf("GetTxField(Asset) herr = %d", herr)
	}
	if !bytes.Equal(got, mptID) {
		t.Errorf("MPT Issue = %x, want 24-byte mptID %x", got, mptID)
	}
}

// rippled getTxArrayLen checks the field's static type before presence: an
// array-length query on a known non-array field is NoArray even when absent.
func TestConformance_ArrayLenAbsentNonArray(t *testing.T) {
	e := New(&mockView{tx: payment(t)})
	if _, herr := e.GetTxArrayLen(fieldCode(t, "DestinationTag")); herr != wasm.HfNoArray {
		t.Errorf("GetTxArrayLen(absent UInt32) herr = %d, want HfNoArray", herr)
	}
}

// rippled cacheLedgerObj / getLedgerObjField: slot index < 1 or > 256 is
// SlotOutRange, and a full cache is SlotsFull.
func TestConformance_CacheSlotRanges(t *testing.T) {
	var idx [32]byte
	idx[0], idx[31] = 0xAB, 0xCD
	obj := payment(t)
	newEnv := func() *Env { return New(&mockView{sles: map[[32]byte][]byte{idx: obj}}) }

	e := newEnv()
	if _, herr := e.CacheLedgerObj(idx[:], -1); herr != wasm.HfSlotOutRange {
		t.Errorf("CacheLedgerObj(idx, -1) herr = %d, want HfSlotOutRange", herr)
	}
	if _, herr := e.CacheLedgerObj(idx[:], 257); herr != wasm.HfSlotOutRange {
		t.Errorf("CacheLedgerObj(idx, 257) herr = %d, want HfSlotOutRange", herr)
	}
	if _, herr := e.GetLedgerObjField(0, fieldCode(t, "Account")); herr != wasm.HfSlotOutRange {
		t.Errorf("GetLedgerObjField(0) herr = %d, want HfSlotOutRange", herr)
	}
	if _, herr := e.GetLedgerObjField(257, fieldCode(t, "Account")); herr != wasm.HfSlotOutRange {
		t.Errorf("GetLedgerObjField(257) herr = %d, want HfSlotOutRange", herr)
	}

	// Fill all 256 slots, then the next auto-allocation is SlotsFull.
	full := newEnv()
	for i := 0; i < maxCache; i++ {
		if slot, herr := full.CacheLedgerObj(idx[:], 0); herr != wasm.HfSuccess || slot != int32(i+1) {
			t.Fatalf("fill slot %d = %d, %d", i, slot, herr)
		}
	}
	if _, herr := full.CacheLedgerObj(idx[:], 0); herr != wasm.HfSlotsFull {
		t.Errorf("full cache herr = %d, want HfSlotsFull", herr)
	}
}

// rippled checkSignature: an unparseable public key is InvalidParams, while a
// parseable key with a bad signature is a non-error 0 result.
func TestConformance_CheckSig(t *testing.T) {
	e := New(nil)
	msg := []byte("message")
	badSig := make([]byte, 64)

	// 32-byte (too short) key: PublicKeyType is unknown -> InvalidParams.
	if v, herr := e.CheckSignature(msg, badSig, make([]byte, 32)); herr != wasm.HfInvalidParams || v != 0 {
		t.Errorf("bad pubkey = (%d, %d), want (0, HfInvalidParams)", v, herr)
	}
	// Well-formed secp256k1 key (0x02 prefix) with a bogus signature: 0, success.
	secpKey, err := hex.DecodeString("0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020")
	if err != nil {
		t.Fatal(err)
	}
	if v, herr := e.CheckSignature(msg, badSig, secpKey); herr != wasm.HfSuccess || v != 0 {
		t.Errorf("invalid sig = (%d, %d), want (0, HfSuccess)", v, herr)
	}
}

// rippled getNFT: a missing token is LedgerObjNotFound, a token with no URI is
// FieldNotFound, and zero account/id are rejected before lookup.
func TestConformance_GetNFT(t *testing.T) {
	owner := acct20(7)
	noURIOwner := acct20(8)
	uri := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	v := &mockView{nftURIs: map[[20]byte][]byte{
		owner:      uri,
		noURIOwner: {}, // present but no URI
	}}
	e := New(v)
	var id [32]byte
	id[0] = 0x01

	if got, herr := e.GetNFT(owner[:], id[:]); herr != wasm.HfSuccess || !bytes.Equal(got, uri) {
		t.Errorf("found+uri = %x (herr %d), want %x", got, herr, uri)
	}
	if _, herr := e.GetNFT(noURIOwner[:], id[:]); herr != wasm.HfFieldNotFound {
		t.Errorf("found, no URI herr = %d, want HfFieldNotFound", herr)
	}
	absent := acct20(9)
	if _, herr := e.GetNFT(absent[:], id[:]); herr != wasm.HfLedgerObjNotFound {
		t.Errorf("missing token herr = %d, want HfLedgerObjNotFound", herr)
	}
	if _, herr := e.GetNFT(make([]byte, 20), id[:]); herr != wasm.HfInvalidAccount {
		t.Errorf("zero account herr = %d, want HfInvalidAccount", herr)
	}
	if _, herr := e.GetNFT(owner[:], make([]byte, 32)); herr != wasm.HfInvalidParams {
		t.Errorf("zero nftID herr = %d, want HfInvalidParams", herr)
	}
	if _, herr := e.GetNFT([]byte{1, 2, 3}, id[:]); herr != wasm.HfInvalidParams {
		t.Errorf("short account herr = %d, want HfInvalidParams", herr)
	}
}
