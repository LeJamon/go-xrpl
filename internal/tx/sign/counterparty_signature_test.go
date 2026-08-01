package sign

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

// cpKeypair derives a deterministic ed25519 keypair + classic address from a
// single seed byte, so counterparty-signature tests can build real signatures.
func cpKeypair(t *testing.T, seed byte) (priv, pub, addr string) {
	t.Helper()
	entropy := make([]byte, addresscodec.FamilySeedLength)
	for i := range entropy {
		entropy[i] = seed
	}
	priv, pub, err := ed25519.Algorithm{}.DeriveKeypair(entropy, false)
	if err != nil {
		t.Fatalf("derive keypair: %v", err)
	}
	addr, err = addresscodec.EncodeClassicAddressFromPublicKeyHex(pub)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}
	return priv, pub, addr
}

// primarySignedTx builds an AccountSet signed by its own account (the primary),
// so a counterparty signature can be layered on top. The top-level SigningPubKey
// is set before signing, so it is part of the data both the primary and the
// counterparty cover.
func primarySignedTx(t *testing.T) txcore.Transaction {
	t.Helper()
	pPriv, pPub, pAddr := cpKeypair(t, 0x11)
	transaction := txcore.NewBaseTx(txcore.TypeAccountSet, pAddr)
	seq := uint32(1)
	transaction.Common.Sequence = &seq
	transaction.Common.Fee = "10"
	transaction.Common.SigningPubKey = pPub
	sig, err := SignTransaction(transaction, pPriv)
	if err != nil {
		t.Fatalf("sign primary: %v", err)
	}
	transaction.Common.TxnSignature = sig
	return transaction
}

func TestTicketedTransactionSigningIncludesZeroSequence(t *testing.T) {
	priv, pub, addr := cpKeypair(t, 0x77)
	transaction := txcore.NewBaseTx(txcore.TypeAccountSet, addr)
	ticketSequence := uint32(7)
	transaction.Common.TicketSequence = &ticketSequence
	transaction.Common.Fee = "10"
	transaction.Common.SigningPubKey = pub

	payload, err := getSigningPayload(transaction)
	if err != nil {
		t.Fatalf("get signing payload: %v", err)
	}
	expectedMap, err := transaction.Flatten()
	if err != nil {
		t.Fatalf("flatten expected transaction: %v", err)
	}
	expectedMap["Sequence"] = uint32(0)
	expected, err := binarycodec.EncodeForSigning(expectedMap)
	if err != nil {
		t.Fatalf("encode expected signing payload: %v", err)
	}
	if payload != expected {
		t.Fatal("ticketed signing payload omitted required Sequence: 0")
	}

	signature, err := SignTransaction(transaction, priv)
	if err != nil {
		t.Fatalf("sign ticketed transaction: %v", err)
	}
	transaction.Common.TxnSignature = signature
	if err := VerifySignature(transaction, false); err != nil {
		t.Fatalf("verify ticketed transaction: %v", err)
	}
}

// sortSigners orders signers by binary AccountID, as a well-formed multi-sign
// array must be.
func sortSigners(signers []txcore.SignerWrapper) {
	sort.Slice(signers, func(i, j int) bool {
		a, _ := state.DecodeAccountID(signers[i].Signer.Account)
		b, _ := state.DecodeAccountID(signers[j].Signer.Account)
		return bytes.Compare(a[:], b[:]) < 0
	})
}

// TestCounterpartySignature_ExcludedFromSigningData pins the wire-level property
// that the nested CounterpartySignature is not covered by the top-level
// signature: the signing payload is byte-identical whether or not it is present.
func TestCounterpartySignature_ExcludedFromSigningData(t *testing.T) {
	transaction := primarySignedTx(t)

	without, err := getSigningPayload(transaction)
	if err != nil {
		t.Fatalf("payload without counterparty: %v", err)
	}

	transaction.GetCommon().CounterpartySignature = &txcore.CounterpartySignature{
		SigningPubKey: "ED0000000000000000000000000000000000000000000000000000000000000001",
		TxnSignature:  "DEADBEEF",
	}
	with, err := getSigningPayload(transaction)
	if err != nil {
		t.Fatalf("payload with counterparty: %v", err)
	}

	if with != without {
		t.Fatalf("CounterpartySignature must be excluded from signing data:\n with=%s\n without=%s", with, without)
	}
}

// TestCounterpartySignature_WireRoundTrip confirms a transaction carrying a
// single-signed CounterpartySignature survives encode → decode with the nested
// signature fields intact.
func TestCounterpartySignature_WireRoundTrip(t *testing.T) {
	transaction := primarySignedTx(t)
	_, cPub, _ := cpKeypair(t, 0x22)
	cp, err := SignCounterparty(transaction, cPub, mustPrivFor(t, 0x22))
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	transaction.GetCommon().CounterpartySignature = cp

	flat, err := transaction.Flatten()
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	blob, err := binarycodec.Encode(flat)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := binarycodec.Decode(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	obj, ok := decoded["CounterpartySignature"].(map[string]any)
	if !ok {
		t.Fatalf("decoded tx missing CounterpartySignature: %v", decoded)
	}
	if obj["SigningPubKey"] != cPub {
		t.Fatalf("round-trip SigningPubKey = %v, want %s", obj["SigningPubKey"], cPub)
	}
	if obj["TxnSignature"] != cp.TxnSignature {
		t.Fatalf("round-trip TxnSignature = %v, want %s", obj["TxnSignature"], cp.TxnSignature)
	}
}

func mustPrivFor(t *testing.T, seed byte) string {
	t.Helper()
	priv, _, _ := cpKeypair(t, seed)
	return priv
}

// TestCounterpartySignature_SingleSignValid verifies a correctly counterparty-
// signed transaction passes.
func TestCounterpartySignature_SingleSignValid(t *testing.T) {
	transaction := primarySignedTx(t)
	cPriv, cPub, _ := cpKeypair(t, 0x22)
	cp, err := SignCounterparty(transaction, cPub, cPriv)
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	transaction.GetCommon().CounterpartySignature = cp

	if err := VerifyCounterpartySignature(transaction, cp, false); err != nil {
		t.Fatalf("valid counterparty signature rejected: %v", err)
	}
}

// TestCounterpartySignature_SingleSignWrongKey rejects a counterparty signature
// whose SigningPubKey does not match the key that produced it, with the
// "Counterparty: " prefix rippled uses.
func TestCounterpartySignature_SingleSignWrongKey(t *testing.T) {
	transaction := primarySignedTx(t)
	cPriv, _, _ := cpKeypair(t, 0x22)
	_, wrongPub, _ := cpKeypair(t, 0x33)

	sig, err := SignTransaction(transaction, cPriv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	cp := &txcore.CounterpartySignature{SigningPubKey: wrongPub, TxnSignature: sig}

	err = VerifyCounterpartySignature(transaction, cp, false)
	if err == nil {
		t.Fatal("wrong-key counterparty signature was accepted")
	}
	if !strings.HasPrefix(err.Error(), counterpartyPrefix) {
		t.Fatalf("error not prefixed %q: %v", counterpartyPrefix, err)
	}
}

// TestCounterpartySignature_SingleAndMultiRejected rejects an object that
// carries both a single signature and a Signers array (signed two ways).
func TestCounterpartySignature_SingleAndMultiRejected(t *testing.T) {
	transaction := primarySignedTx(t)
	cPriv, cPub, _ := cpKeypair(t, 0x22)
	sig, _ := SignTransaction(transaction, cPriv)
	cp := &txcore.CounterpartySignature{
		SigningPubKey: cPub,
		TxnSignature:  sig,
		Signers: []txcore.SignerWrapper{
			{Signer: txcore.Signer{Account: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}},
		},
	}
	if err := VerifyCounterpartySignature(transaction, cp, false); err == nil {
		t.Fatal("counterparty signed two ways was accepted")
	}
}

// TestCounterpartySignature_MultiSignValid verifies a multi-signed counterparty
// object with two signers over the multi-signing payload.
func TestCounterpartySignature_MultiSignValid(t *testing.T) {
	transaction := primarySignedTx(t)

	s1Priv, s1Pub, s1Addr := cpKeypair(t, 0x44)
	s2Priv, s2Pub, s2Addr := cpKeypair(t, 0x55)
	sig1, _ := SignTransactionForMultiSignTarget(transaction, s1Addr, s1Priv)
	sig2, _ := SignTransactionForMultiSignTarget(transaction, s2Addr, s2Priv)

	signers := []txcore.SignerWrapper{
		{Signer: txcore.Signer{Account: s1Addr, SigningPubKey: s1Pub, TxnSignature: sig1}},
		{Signer: txcore.Signer{Account: s2Addr, SigningPubKey: s2Pub, TxnSignature: sig2}},
	}
	sortSigners(signers)
	cp := &txcore.CounterpartySignature{Signers: signers}

	if err := VerifyCounterpartySignature(transaction, cp, false); err != nil {
		t.Fatalf("valid multi-signed counterparty rejected: %v", err)
	}
}

// TestCounterpartySignature_MultiSignWrongKey rejects a multi-signed
// counterparty object when a nested signature does not match its key.
func TestCounterpartySignature_MultiSignWrongKey(t *testing.T) {
	transaction := primarySignedTx(t)

	s1Priv, _, s1Addr := cpKeypair(t, 0x44)
	_, wrongPub, _ := cpKeypair(t, 0x66)
	sig1, _ := SignTransactionForMultiSignTarget(transaction, s1Addr, s1Priv)

	cp := &txcore.CounterpartySignature{
		Signers: []txcore.SignerWrapper{
			{Signer: txcore.Signer{Account: s1Addr, SigningPubKey: wrongPub, TxnSignature: sig1}},
		},
	}
	err := VerifyCounterpartySignature(transaction, cp, false)
	if err == nil {
		t.Fatal("multi-signed counterparty with wrong key accepted")
	}
	if !strings.HasPrefix(err.Error(), counterpartyPrefix) {
		t.Fatalf("error not prefixed %q: %v", counterpartyPrefix, err)
	}
}

// TestCounterpartySignature_MultiSignUnsorted rejects nested signers that are
// not sorted by AccountID.
func TestCounterpartySignature_MultiSignUnsorted(t *testing.T) {
	transaction := primarySignedTx(t)

	s1Priv, s1Pub, s1Addr := cpKeypair(t, 0x44)
	s2Priv, s2Pub, s2Addr := cpKeypair(t, 0x55)
	sig1, _ := SignTransactionForMultiSignTarget(transaction, s1Addr, s1Priv)
	sig2, _ := SignTransactionForMultiSignTarget(transaction, s2Addr, s2Priv)

	signers := []txcore.SignerWrapper{
		{Signer: txcore.Signer{Account: s1Addr, SigningPubKey: s1Pub, TxnSignature: sig1}},
		{Signer: txcore.Signer{Account: s2Addr, SigningPubKey: s2Pub, TxnSignature: sig2}},
	}
	sortSigners(signers)
	// Force descending order to break the sort invariant.
	signers[0], signers[1] = signers[1], signers[0]
	cp := &txcore.CounterpartySignature{Signers: signers}

	if err := VerifyCounterpartySignature(transaction, cp, false); err == nil {
		t.Fatal("unsorted counterparty signers accepted")
	}
}

// TestCounterpartySignature_MultiSignSelfSignAllowed confirms the counterparty
// may sign for the transaction's own Account, unlike a top-level multi-sign
// (rippled multiSignHelper with an unseated txnAccountID).
func TestCounterpartySignature_MultiSignSelfSignAllowed(t *testing.T) {
	transaction := primarySignedTx(t)
	txAccount := transaction.GetCommon().Account

	// Sign the multi-signing payload for the transaction's own account, using
	// the primary key material (the account signing for itself).
	priv := mustPrivFor(t, 0x11)
	_, pub, _ := cpKeypair(t, 0x11)
	sig, _ := SignTransactionForMultiSignTarget(transaction, txAccount, priv)

	cp := &txcore.CounterpartySignature{
		Signers: []txcore.SignerWrapper{
			{Signer: txcore.Signer{Account: txAccount, SigningPubKey: pub, TxnSignature: sig}},
		},
	}
	if err := VerifyCounterpartySignature(transaction, cp, false); err != nil {
		t.Fatalf("counterparty self-sign should be allowed: %v", err)
	}
}
