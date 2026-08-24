package engine

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
)

var cacheMutationRegisterAccount sync.Once

func registerCacheMutationAccount() {
	cacheMutationRegisterAccount.Do(account.Register)
}

func cacheMutationKeypair(t *testing.T, seed string) (privateKey, publicKey, account string) {
	t.Helper()
	seedDigest := sha512half.Sum([]byte(seed))
	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair(seedDigest[:crypto.FamilySeedSize], false)
	if err != nil {
		t.Fatalf("derive keypair: %v", err)
	}
	account, err = addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if err != nil {
		t.Fatalf("derive account: %v", err)
	}
	return privateKey, publicKey, account
}

func cacheMutationBaseTx(t *testing.T) *txcore.BaseTx {
	t.Helper()
	privateKey, publicKey, account := cacheMutationKeypair(t, "cache-mutation-primary")
	txn := txcore.NewBaseTx(txcore.TypeAccountSet, account)
	txn.Fee = "10"
	txn.SetSequence(1)
	txn.SigningPubKey = publicKey
	var err error
	txn.TxnSignature, err = sign.SignTransaction(txn, privateKey)
	if err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return txn
}

func TestPrewarmSignatureVerdictRejectsSignatureObjectMutation(t *testing.T) {
	tests := []struct {
		name   string
		makeTx func(*testing.T) txcore.Transaction
		mutate func(txcore.Transaction)
	}{
		{
			name: "outer",
			makeTx: func(t *testing.T) txcore.Transaction {
				return cacheMutationBaseTx(t)
			},
			mutate: func(txn txcore.Transaction) {
				txn.GetCommon().TxnSignature = "00"
			},
		},
		{
			name: "counterparty",
			makeTx: func(t *testing.T) txcore.Transaction {
				txn := cacheMutationBaseTx(t)
				privateKey, publicKey, _ := cacheMutationKeypair(t, "cache-mutation-counterparty")
				counterparty, err := sign.SignCounterparty(txn, publicKey, privateKey)
				if err != nil {
					t.Fatalf("sign counterparty: %v", err)
				}
				txn.CounterpartySignature = counterparty
				return txn
			},
			mutate: func(txn txcore.Transaction) {
				txn.GetCommon().CounterpartySignature.TxnSignature = "00"
			},
		},
		{
			name: "sponsor",
			makeTx: func(t *testing.T) txcore.Transaction {
				primaryPrivate, primaryPublic, primaryAccount := cacheMutationKeypair(t, "cache-mutation-sponsored-primary")
				sponsorPrivate, sponsorPublic, sponsorAccount := cacheMutationKeypair(t, "cache-mutation-sponsor")
				txn := txcore.NewBaseTx(txcore.TypeAccountSet, primaryAccount)
				txn.Fee = "20"
				txn.SetSequence(1)
				txn.SigningPubKey = primaryPublic
				txn.Sponsor = sponsorAccount
				flags := txcore.SpfSponsorFee
				txn.SponsorFlags = &flags
				var err error
				txn.TxnSignature, err = sign.SignTransaction(txn, primaryPrivate)
				if err != nil {
					t.Fatalf("sign transaction: %v", err)
				}
				txn.SponsorSignature, err = sign.SignSponsor(txn, sponsorPublic, sponsorPrivate)
				if err != nil {
					t.Fatalf("sign sponsor: %v", err)
				}
				return txn
			},
			mutate: func(txn txcore.Transaction) {
				txn.GetCommon().SponsorSignature.TxnSignature = "00"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sigcache.Reset()
			txn := test.makeTx(t)
			if err := PrewarmSignature(txn); err != nil {
				t.Fatalf("prewarm valid transaction: %v", err)
			}
			verifiedID, err := txcore.ComputeCurrentTransactionHash(txn)
			if err != nil {
				t.Fatalf("compute verified transaction ID: %v", err)
			}
			test.mutate(txn)
			mutatedID, err := txcore.ComputeCurrentTransactionHash(txn)
			if err != nil {
				t.Fatalf("compute mutated transaction ID: %v", err)
			}
			if verifiedID == mutatedID {
				t.Fatal("signature mutation did not change transaction identity")
			}
			if err := PrewarmSignature(txn); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("mutated signature error = %v, want ErrInvalidSignature", err)
			}
			if sigcache.Verified(mutatedID) {
				t.Fatal("invalid mutated transaction populated signature cache")
			}
		})
	}
}

func TestSetRawBytesInvalidatesCachedVerdicts(t *testing.T) {
	txn := cacheMutationBaseTx(t)
	if err := PrewarmSignature(txn); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	rules := amendment.AllSupportedRules()
	txID, err := txcore.ComputeCurrentTransactionHash(txn)
	if err != nil {
		t.Fatalf("compute transaction ID: %v", err)
	}
	txn.GetCommon().MarkPreflightVerified(rules, txID)
	txn.SetRawBytes([]byte{0x01})
	if txn.GetCommon().SignatureVerified() {
		t.Fatal("SetRawBytes retained signature verdict")
	}
	if txn.GetCommon().PreflightVerified(rules) {
		t.Fatal("SetRawBytes retained preflight verdict")
	}
}

func TestSetRawBytesRequiresBindingBeforeSignatureVerification(t *testing.T) {
	registerCacheMutationAccount()
	original := cacheMutationBaseTx(t)
	blob, err := txcore.SerializeTransaction(original)
	if err != nil {
		t.Fatalf("serialize signed transaction: %v", err)
	}
	parsed, err := txcore.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("parse signed transaction: %v", err)
	}
	parsed.SetRawBytes(blob)
	if err := PrewarmSignature(parsed); err != nil {
		t.Fatalf("setting identical raw bytes lost field binding: %v", err)
	}

	replacement := append([]byte(nil), blob...)
	replacement[len(replacement)-1] ^= 1
	parsed.SetRawBytes(replacement)
	if err := PrewarmSignature(parsed); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("unbound replacement raw bytes error = %v, want ErrInvalidSignature", err)
	}
}

func TestBindRawBytesRejectsMismatchedFieldsAndOwnsBytes(t *testing.T) {
	registerCacheMutationAccount()
	first := cacheMutationBaseTx(t)
	firstRaw, err := txcore.SerializeTransaction(first)
	if err != nil {
		t.Fatalf("serialize first transaction: %v", err)
	}

	privateKey, publicKey, accountID := cacheMutationKeypair(t, "cache-binding-second")
	second := txcore.NewBaseTx(txcore.TypeAccountSet, accountID)
	second.Fee = "12"
	second.SetSequence(3)
	second.SigningPubKey = publicKey
	second.TxnSignature, err = sign.SignTransaction(second, privateKey)
	if err != nil {
		t.Fatalf("sign second transaction: %v", err)
	}
	secondRaw, err := txcore.SerializeTransaction(second)
	if err != nil {
		t.Fatalf("serialize second transaction: %v", err)
	}

	if err := txcore.BindRawBytes(second, firstRaw); err == nil {
		t.Fatal("mismatched raw transaction was bound")
	}
	if len(second.GetRawBytes()) != 0 {
		t.Fatal("failed binding changed stored raw bytes")
	}

	input := append([]byte(nil), secondRaw...)
	if err := txcore.BindRawBytes(second, input); err != nil {
		t.Fatalf("bind matching raw transaction: %v", err)
	}
	input[0] ^= 1
	if got := second.GetRawBytes(); !bytes.Equal(got, secondRaw) {
		t.Fatal("mutating BindRawBytes input changed stored raw bytes")
	}
	returned := second.GetRawBytes()
	returned[0] ^= 1
	if got := second.GetRawBytes(); !bytes.Equal(got, secondRaw) {
		t.Fatal("mutating GetRawBytes result changed stored raw bytes")
	}
	if err := PrewarmSignature(second); err != nil {
		t.Fatalf("matching bound transaction did not verify: %v", err)
	}
}

func TestParsedSignatureVerificationUsesCurrentSignedFields(t *testing.T) {
	registerCacheMutationAccount()
	_, _, replacementAccount := cacheMutationKeypair(t, "cache-mutation-replacement")
	_, _, delegateAccount := cacheMutationKeypair(t, "cache-mutation-delegate")
	sequence := uint32(2)
	ticket := uint32(7)
	zero := uint32(0)
	tests := []struct {
		name   string
		mutate func(txcore.Transaction)
	}{
		{name: "fee", mutate: func(txn txcore.Transaction) { txn.GetCommon().Fee = "11" }},
		{name: "sequence", mutate: func(txn txcore.Transaction) { txn.GetCommon().Sequence = &sequence }},
		{name: "ticket", mutate: func(txn txcore.Transaction) {
			txn.GetCommon().Sequence = &zero
			txn.GetCommon().TicketSequence = &ticket
		}},
		{name: "account", mutate: func(txn txcore.Transaction) { txn.GetCommon().Account = replacementAccount }},
		{name: "delegate", mutate: func(txn txcore.Transaction) { txn.GetCommon().Delegate = delegateAccount }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sigcache.Reset()
			original := cacheMutationBaseTx(t)
			blob, err := txcore.SerializeTransaction(original)
			if err != nil {
				t.Fatalf("serialize signed transaction: %v", err)
			}
			parsed, err := txcore.ParseFromBinary(blob)
			if err != nil {
				t.Fatalf("parse signed transaction: %v", err)
			}
			if err := PrewarmSignature(parsed); err != nil {
				t.Fatalf("unchanged parsed transaction did not verify: %v", err)
			}
			test.mutate(parsed)
			if err := txcore.BindRawBytes(parsed, blob); err != nil {
				t.Fatalf("rebind identical raw transaction: %v", err)
			}
			if err := PrewarmSignature(parsed); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("mutated signed field error = %v, want ErrInvalidSignature", err)
			}
		})
	}
}
