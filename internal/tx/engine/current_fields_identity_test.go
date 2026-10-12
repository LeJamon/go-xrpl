package engine

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type flattenCountingTx struct {
	*txcore.BaseTx
	flattenCalls int
	flattenErr   error
	failFlatten  int
}

func (tx *flattenCountingTx) Flatten() (map[string]any, error) {
	tx.flattenCalls++
	if tx.failFlatten > 0 {
		tx.failFlatten--
		return nil, tx.flattenErr
	}
	return tx.BaseTx.Flatten()
}

func boundCountingTx(t *testing.T) (*flattenCountingTx, [32]byte) {
	t.Helper()
	base := newSignedSingleSignTx(t)
	raw, err := txcore.SerializeTransaction(base)
	if err != nil {
		t.Fatalf("serialize signed transaction: %v", err)
	}
	tx := &flattenCountingTx{BaseTx: base}
	if err := txcore.BindRawBytes(tx, raw); err != nil {
		t.Fatalf("bind signed transaction: %v", err)
	}
	id, err := txcore.ComputeCurrentTransactionHash(tx)
	if err != nil {
		t.Fatalf("compute current transaction ID: %v", err)
	}
	tx.flattenCalls = 0
	return tx, id
}

func markBothSignatureCaches(tx *flattenCountingTx, id [32]byte, rules *amendment.Rules) {
	tx.GetCommon().MarkSignatureVerifiedWithRules(id, rules)
	sigcache.MarkVerifiedWithRules(id, tx.GetCommon().SignatureCacheLegacyRole(rules))
}

func TestVerifySignaturesWireCacheHitsSerializeCurrentFieldsOnce(t *testing.T) {
	rules := amendment.AllSupportedRules()
	for _, test := range []struct {
		name string
		seed func(*flattenCountingTx, [32]byte)
	}{
		{
			name: "local cache",
			seed: func(tx *flattenCountingTx, id [32]byte) {
				tx.GetCommon().MarkSignatureVerifiedWithRules(id, rules)
			},
		},
		{
			name: "global cache",
			seed: func(tx *flattenCountingTx, id [32]byte) {
				sigcache.MarkVerifiedWithRules(id, tx.GetCommon().SignatureCacheLegacyRole(rules))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sigcache.Reset()
			t.Cleanup(sigcache.Reset)
			tx, id := boundCountingTx(t)
			test.seed(tx, id)

			if got := verifyingEngine(rules).verifySignatures(tx); got != ter.TesSUCCESS {
				t.Fatalf("cached wire transaction result = %s, want tesSUCCESS", got)
			}
			if tx.flattenCalls != 1 {
				t.Fatalf("cached wire transaction flattened %d times, want 1", tx.flattenCalls)
			}
		})
	}
}

func TestVerifySignaturesRejectsMutatedOrUnboundWireBeforeCaches(t *testing.T) {
	rules := amendment.AllSupportedRules()

	t.Run("mutated fields", func(t *testing.T) {
		sigcache.Reset()
		t.Cleanup(sigcache.Reset)
		tx, _ := boundCountingTx(t)
		tx.GetCommon().Fee = "11"
		mutatedID, err := txcore.ComputeCurrentTransactionHash(tx)
		if err != nil {
			t.Fatalf("compute mutated transaction ID: %v", err)
		}
		tx.flattenCalls = 0
		markBothSignatureCaches(tx, mutatedID, rules)

		if got := verifyingEngine(rules).verifySignatures(tx); got != ter.TemINVALID {
			t.Fatalf("mutated wire transaction result = %s, want temINVALID", got)
		}
		if tx.flattenCalls != 1 {
			t.Fatalf("mutated wire transaction flattened %d times, want 1", tx.flattenCalls)
		}
	})

	t.Run("missing binding", func(t *testing.T) {
		sigcache.Reset()
		t.Cleanup(sigcache.Reset)
		base := newSignedSingleSignTx(t)
		raw, err := txcore.SerializeTransaction(base)
		if err != nil {
			t.Fatalf("serialize signed transaction: %v", err)
		}
		tx := &flattenCountingTx{BaseTx: base}
		tx.SetRawBytes(raw)
		id, err := txcore.ComputeCurrentTransactionHash(tx)
		if err != nil {
			t.Fatalf("compute current transaction ID: %v", err)
		}
		tx.flattenCalls = 0
		markBothSignatureCaches(tx, id, rules)

		if got := verifyingEngine(rules).verifySignatures(tx); got != ter.TemINVALID {
			t.Fatalf("unbound wire transaction result = %s, want temINVALID", got)
		}
		if tx.flattenCalls != 0 {
			t.Fatalf("unbound wire transaction flattened %d times, want 0", tx.flattenCalls)
		}
	})
}

func TestVerifySignaturesIdentityErrorFallsBackWithoutCaching(t *testing.T) {
	sigcache.Reset()
	t.Cleanup(sigcache.Reset)
	rules := amendment.AllSupportedRules()
	base := newSignedSingleSignTx(t)
	tx := &flattenCountingTx{BaseTx: base}
	id, err := txcore.ComputeCurrentTransactionHash(tx)
	if err != nil {
		t.Fatalf("compute current transaction ID: %v", err)
	}
	tx.flattenCalls = 0
	tx.flattenErr = errors.New("transient flatten failure")
	tx.failFlatten = 1

	if got := verifyingEngine(rules).verifySignatures(tx); got != ter.TesSUCCESS {
		t.Fatalf("identity-error fallback result = %s, want tesSUCCESS", got)
	}
	if tx.flattenCalls != 2 {
		t.Fatalf("identity-error path flattened %d times, want identity plus signature checks", tx.flattenCalls)
	}
	if tx.GetCommon().SignatureVerified() {
		t.Fatal("identity-error path populated the local signature cache")
	}
	if sigcache.Verified(id) {
		t.Fatal("identity-error path populated the global signature cache")
	}
}

func TestVerifySignaturesIdentityErrorSkipsSeededCaches(t *testing.T) {
	sigcache.Reset()
	t.Cleanup(sigcache.Reset)
	rules := amendment.AllSupportedRules()
	tx := &flattenCountingTx{BaseTx: newSignedSingleSignTx(t)}
	var zeroID [32]byte
	tx.GetCommon().MarkSignatureVerifiedWithRules(zeroID, rules)
	sigcache.MarkVerifiedWithRules(zeroID, false)
	tx.GetCommon().TxnSignature = "00"
	tx.flattenErr = errors.New("transient flatten failure")
	tx.failFlatten = 1

	if got := verifyingEngine(rules).verifySignatures(tx); got != ter.TemINVALID {
		t.Fatalf("identity-error cached bad signature result = %s, want temINVALID", got)
	}
	if tx.flattenCalls != 2 {
		t.Fatalf("identity-error cached path flattened %d times, want identity plus signature checks", tx.flattenCalls)
	}
}
