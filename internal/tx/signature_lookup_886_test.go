package tx

import (
	"errors"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// stubSignerLookup is a SignerListLookup whose two methods are driven by
// closures, so a test can inject a real storage error vs. the not-found
// sentinel independently.
type stubSignerLookup struct {
	signerList func(account string) (*state.SignerListInfo, error)
	accInfo    func(account string) (uint32, string, error)
}

func (s *stubSignerLookup) GetSignerList(account string) (*state.SignerListInfo, error) {
	return s.signerList(account)
}

func (s *stubSignerLookup) GetAccountInfo(account string) (uint32, string, error) {
	return s.accInfo(account)
}

// multiSignedTxForLookup builds a single-signer multi-signed transaction whose
// signer's public key derives to its own account (the phantom/master branch of
// VerifyMultiSignature). The signature is intentionally bogus: GetAccountInfo is
// consulted before the signature is verified, so a lookup error short-circuits
// ahead of crypto. EncodeClassicAddressFromPublicKeyHex only hashes the bytes,
// so any valid 33-byte key yields a deterministic address.
func multiSignedTxForLookup(t *testing.T) (Transaction, string) {
	t.Helper()
	const signerPub = "ED0000000000000000000000000000000000000000000000000000000000000001"
	signerAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(signerPub)
	if err != nil {
		t.Fatalf("derive signer address: %v", err)
	}

	tx := NewBaseTx(TypeAccountSet, signerAddr)
	tx.Common.SigningPubKey = "" // multi-signed: empty top-level signing key
	tx.Common.Signers = []SignerWrapper{
		{Signer: Signer{
			Account:       signerAddr,
			SigningPubKey: signerPub,
			TxnSignature:  "00", // bogus; never reached when lookup errors
		}},
	}
	return tx, signerAddr
}

func authorizingLookup(signerAddr string, accInfo func(string) (uint32, string, error)) *stubSignerLookup {
	return &stubSignerLookup{
		signerList: func(string) (*state.SignerListInfo, error) {
			return &state.SignerListInfo{
				SignerQuorum: 1,
				SignerEntries: []state.AccountSignerEntry{
					{Account: signerAddr, SignerWeight: 1},
				},
			}, nil
		},
		accInfo: accInfo,
	}
}

// TestVerifyMultiSignature_StorageErrorIsInternal pins the issue #886 finding 5
// fix: a real storage/parse failure during signer authorization must NOT be
// swallowed into the phantom-allowed branch. It has to surface as the
// internal-error class, never silently accepting the signer.
func TestVerifyMultiSignature_StorageErrorIsInternal(t *testing.T) {
	tx, signerAddr := multiSignedTxForLookup(t)

	storageErr := errors.New("kvstore: disk read failed")
	lookup := authorizingLookup(signerAddr, func(string) (uint32, string, error) {
		return 0, "", storageErr
	})

	err := VerifyMultiSignature(tx, lookup, false)
	if err == nil {
		t.Fatal("expected error on storage failure, got nil (signer was accepted)")
	}

	re, ok := ter.AsResultError(err)
	if !ok || re.Code != ter.TefINTERNAL {
		t.Fatalf("storage error must map to tefINTERNAL, got %v", err)
	}
	if errors.Is(err, ErrBadSignature) || errors.Is(err, ErrMasterDisabled) {
		t.Fatalf("storage error must not be reported as a signature/auth verdict: %v", err)
	}
}

// TestVerifyMultiSignature_NotFoundTakesPhantomPath confirms the genuine
// not-found case still takes the phantom branch (no internal error). Because the
// account is absent, the phantom signer is allowed through to signature
// verification, which then fails on the bogus signature (ErrBadSignature). The
// point is that not-found does NOT short-circuit to tefINTERNAL.
func TestVerifyMultiSignature_NotFoundTakesPhantomPath(t *testing.T) {
	tx, signerAddr := multiSignedTxForLookup(t)

	lookup := authorizingLookup(signerAddr, func(string) (uint32, string, error) {
		return 0, "", ErrAccountNotFound
	})

	err := VerifyMultiSignature(tx, lookup, false)
	if err != ErrBadSignature {
		t.Fatalf("phantom signer with bogus signature should yield ErrBadSignature, got %v", err)
	}
	if re, ok := ter.AsResultError(err); ok && re.Code == ter.TefINTERNAL {
		t.Fatal("not-found must not be reported as tefINTERNAL")
	}
}

// TestVerifyMultiSignature_RegularKeyStorageErrorIsInternal covers the other
// GetAccountInfo call site: the regular-key branch (signer pubkey derives to a
// different account). A storage failure there must also be internal, not a bad
// signature.
func TestVerifyMultiSignature_RegularKeyStorageErrorIsInternal(t *testing.T) {
	const signerPub = "ED0000000000000000000000000000000000000000000000000000000000000003"
	signerPubAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(signerPub)
	if err != nil {
		t.Fatalf("derive pubkey address: %v", err)
	}
	// The signer account is a DIFFERENT, valid address than the one the pubkey
	// derives to, forcing the regular-key branch. Derived from an unrelated key.
	signerAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(
		"ED00000000000000000000000000000000000000000000000000000000000000AA")
	if err != nil {
		t.Fatalf("derive signer address: %v", err)
	}
	if signerAddr == signerPubAddr {
		t.Fatal("test setup: signer account collided with pubkey-derived address")
	}

	tx := NewBaseTx(TypeAccountSet, signerAddr)
	tx.Common.SigningPubKey = ""
	tx.Common.Signers = []SignerWrapper{
		{Signer: Signer{Account: signerAddr, SigningPubKey: signerPub, TxnSignature: "00"}},
	}

	storageErr := errors.New("kvstore: disk read failed")
	lookup := authorizingLookup(signerAddr, func(string) (uint32, string, error) {
		return 0, "", storageErr
	})

	verr := VerifyMultiSignature(tx, lookup, false)
	re, ok := ter.AsResultError(verr)
	if !ok || re.Code != ter.TefINTERNAL {
		t.Fatalf("regular-key storage error must map to tefINTERNAL, got %v", verr)
	}
}
