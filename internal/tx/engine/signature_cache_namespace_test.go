package engine

import (
	"fmt"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func cleanupCacheRules(enabled bool) *amendment.Rules {
	if !enabled {
		return amendment.NewRules(nil)
	}
	return amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
}

func roleCacheTransaction(t *testing.T, role binarycodec.SigningRole, rules *amendment.Rules) *txcore.BaseTx {
	t.Helper()
	txn := cacheMutationBaseTx(t)
	privateKey, publicKey, _ := cacheMutationKeypair(t, "signature-cache-role")

	switch role {
	case binarycodec.CounterpartyRole:
		signature, err := sign.SignCounterpartyWithRules(txn, publicKey, privateKey, rules)
		if err != nil {
			t.Fatalf("sign counterparty: %v", err)
		}
		txn.CounterpartySignature = signature
	case binarycodec.SponsorRole:
		signature, err := sign.SignSponsorWithRules(txn, publicKey, privateKey, rules)
		if err != nil {
			t.Fatalf("sign sponsor: %v", err)
		}
		txn.SponsorSignature = signature
	default:
		t.Fatalf("unsupported role %d", role)
	}
	return txn
}

func corruptRoleCacheSignature(txn *txcore.BaseTx, role binarycodec.SigningRole) {
	corruptRoleCacheSignatureTo(txn, role, "00")
}

func corruptRoleCacheSignatureTo(txn *txcore.BaseTx, role binarycodec.SigningRole, signature string) {
	if role == binarycodec.CounterpartyRole {
		txn.CounterpartySignature.TxnSignature = signature
		return
	}
	txn.SponsorSignature.TxnSignature = signature
}

func roleCacheID(t *testing.T, txn txcore.Transaction) [32]byte {
	t.Helper()
	id, err := txcore.ComputeCurrentTransactionHash(txn)
	if err != nil {
		t.Fatalf("compute transaction ID: %v", err)
	}
	return id
}

func TestRoleSignatureCacheNamespaceTransition(t *testing.T) {
	for _, role := range []binarycodec.SigningRole{
		binarycodec.CounterpartyRole,
		binarycodec.SponsorRole,
	} {
		t.Run(roleName(role), func(t *testing.T) {
			sigcache.Reset()
			oldRules := cleanupCacheRules(false)
			newRules := cleanupCacheRules(true)

			oldTxn := roleCacheTransaction(t, role, oldRules)
			if err := PrewarmSignatureWithRules(oldTxn, oldRules); err != nil {
				t.Fatalf("prewarm pre-cleanup role signature: %v", err)
			}
			oldID := roleCacheID(t, oldTxn)
			if !sigcache.VerifiedWithRules(oldID, true) {
				t.Fatal("pre-cleanup role signature was not cached in legacy namespace")
			}
			if sigcache.VerifiedWithRules(oldID, false) {
				t.Fatal("pre-cleanup role signature leaked into normal namespace")
			}
			if got := verifyingEngine(oldRules).verifySignatures(oldTxn); got != ter.TesSUCCESS {
				t.Fatalf("pre-cleanup role signature under old rules = %s, want tesSUCCESS", got)
			}
			if got := verifyingEngine(newRules).verifySignatures(oldTxn); got != ter.TemINVALID {
				t.Fatalf("pre-cleanup role signature under cleanup rules = %s, want temINVALID", got)
			}
			if got := verifyingEngine(oldRules).verifySignatures(oldTxn); got != ter.TesSUCCESS {
				t.Fatalf("pre-cleanup role signature after transition back = %s, want tesSUCCESS", got)
			}

			newTxn := roleCacheTransaction(t, role, newRules)
			if err := PrewarmSignatureWithRules(newTxn, newRules); err != nil {
				t.Fatalf("prewarm cleanup role signature: %v", err)
			}
			newID := roleCacheID(t, newTxn)
			if !sigcache.VerifiedWithRules(newID, false) {
				t.Fatal("cleanup role signature was not cached in normal namespace")
			}
			if sigcache.VerifiedWithRules(newID, true) {
				t.Fatal("cleanup role signature leaked into legacy namespace")
			}
			if got := verifyingEngine(newRules).verifySignatures(newTxn); got != ter.TesSUCCESS {
				t.Fatalf("cleanup role signature under cleanup rules = %s, want tesSUCCESS", got)
			}
			if got := verifyingEngine(oldRules).verifySignatures(newTxn); got != ter.TemINVALID {
				t.Fatalf("cleanup role signature under pre-cleanup rules = %s, want temINVALID", got)
			}
		})
	}
}

func TestRoleSignatureProcessCacheNamespaceMissesAcrossTransition(t *testing.T) {
	for _, role := range []binarycodec.SigningRole{
		binarycodec.CounterpartyRole,
		binarycodec.SponsorRole,
	} {
		t.Run(roleName(role), func(t *testing.T) {
			sigcache.Reset()
			oldRules := cleanupCacheRules(false)
			newRules := cleanupCacheRules(true)

			oldBad := roleCacheTransaction(t, role, oldRules)
			corruptRoleCacheSignature(oldBad, role)
			oldID := roleCacheID(t, oldBad)
			// Seed the simulated prior positive verdict so the test can exercise
			// the cache hit with a deliberately bad current signature.
			sigcache.MarkVerifiedWithRules(oldID, true)

			freshOldBad := roleCacheTransaction(t, role, oldRules)
			corruptRoleCacheSignature(freshOldBad, role)
			if got := verifyingEngine(oldRules).verifySignatures(freshOldBad); got != ter.TesSUCCESS {
				t.Fatalf("legacy process-cache hit = %s, want tesSUCCESS", got)
			}
			if freshOldBad.GetCommon().SignatureVerifiedWithRules(oldID, oldRules) {
				t.Fatal("process-cache hit must not mutate the fresh object's verdict")
			}

			freshOldUnderNew := roleCacheTransaction(t, role, oldRules)
			corruptRoleCacheSignature(freshOldUnderNew, role)
			if got := verifyingEngine(newRules).verifySignatures(freshOldUnderNew); got != ter.TemINVALID {
				t.Fatalf("legacy entry under cleanup rules = %s, want temINVALID", got)
			}

			newBad := roleCacheTransaction(t, role, newRules)
			corruptRoleCacheSignatureTo(newBad, role, "01")
			newID := roleCacheID(t, newBad)
			// As above, this models a prior verification of the exact blob.
			sigcache.MarkVerifiedWithRules(newID, false)

			freshNewBad := roleCacheTransaction(t, role, newRules)
			corruptRoleCacheSignatureTo(freshNewBad, role, "01")
			if got := verifyingEngine(newRules).verifySignatures(freshNewBad); got != ter.TesSUCCESS {
				t.Fatalf("normal process-cache hit = %s, want tesSUCCESS", got)
			}

			freshNewUnderOld := roleCacheTransaction(t, role, newRules)
			corruptRoleCacheSignatureTo(freshNewUnderOld, role, "01")
			if got := verifyingEngine(oldRules).verifySignatures(freshNewUnderOld); got != ter.TemINVALID {
				t.Fatalf("normal entry under pre-cleanup rules = %s, want temINVALID", got)
			}
		})
	}
}

func TestOrdinarySignatureCacheNamespaceIsSharedAcrossRules(t *testing.T) {
	sigcache.Reset()
	oldRules := cleanupCacheRules(false)
	newRules := cleanupCacheRules(true)
	valid := newSignedSingleSignTx(t)
	valid.Common.TxnSignature = "00"
	badID := roleCacheID(t, valid)
	sigcache.MarkVerifiedWithRules(badID, false)

	for name, rules := range map[string]*amendment.Rules{"old": oldRules, "new": newRules} {
		t.Run(name, func(t *testing.T) {
			bad := newSignedSingleSignTx(t)
			bad.Common.TxnSignature = "00"
			if got := verifyingEngine(rules).verifySignatures(bad); got != ter.TesSUCCESS {
				t.Fatalf("ordinary normal-namespace cache hit = %s, want tesSUCCESS", got)
			}
		})
	}
}

func TestSignatureCacheSkippedVerificationDoesNotPopulate(t *testing.T) {
	for name, config := range map[string]txcore.EngineConfig{
		"skip": {
			Rules:                     cleanupCacheRules(false),
			SkipSignatureVerification: true,
		},
		"dry-run": {
			Rules:      cleanupCacheRules(false),
			ApplyFlags: txcore.TapDRY_RUN,
		},
	} {
		t.Run(name, func(t *testing.T) {
			sigcache.Reset()
			txn := roleCacheTransaction(t, binarycodec.CounterpartyRole, config.Rules)
			id := roleCacheID(t, txn)
			if got := NewEngine(newMockBaseView(), config).verifySignatures(txn); got != ter.TesSUCCESS {
				t.Fatalf("skipped signature verification = %s, want tesSUCCESS", got)
			}
			if sigcache.VerifiedWithRules(id, true) || sigcache.VerifiedWithRules(id, false) {
				t.Fatal("skipped signature verification populated the process cache")
			}
			if txn.GetCommon().SignatureVerifiedWithRules(id, config.Rules) {
				t.Fatal("skipped signature verification populated the object cache")
			}
		})
	}
}

func TestInnerBatchFlagCannotUseSignatureCache(t *testing.T) {
	sigcache.Reset()
	rules := cleanupCacheRules(false)
	txn := newSignedSingleSignTx(t)
	flags := txcore.TfInnerBatchTxn
	txn.Common.Flags = &flags
	id := roleCacheID(t, txn)
	txn.GetCommon().MarkSignatureVerifiedWithRules(id, rules)
	sigcache.MarkVerifiedWithRules(id, false)

	if err := PrewarmSignatureWithRules(txn, rules); err == nil {
		t.Fatal("prewarm must reject an inner-batch transaction before consulting the cache")
	}
	if got := verifyingEngine(rules).verifySignatures(txn); got != ter.TemINVALID {
		t.Fatalf("inner-batch transaction with cached signature = %s, want temINVALID", got)
	}
}

func TestRoleSignatureCacheConcurrentFreshObjects(t *testing.T) {
	sigcache.Reset()
	oldRules := cleanupCacheRules(false)
	newRules := cleanupCacheRules(true)

	const workers = 32
	transactions := make([]*txcore.BaseTx, workers)
	for i := range transactions {
		rules := oldRules
		if i%2 != 0 {
			rules = newRules
		}
		transactions[i] = roleCacheTransaction(t, binarycodec.CounterpartyRole, rules)
	}

	var wg sync.WaitGroup
	for i, transaction := range transactions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			validRules, invalidRules := oldRules, newRules
			if i%2 != 0 {
				validRules, invalidRules = newRules, oldRules
			}
			for range 4 {
				if got := verifyingEngine(invalidRules).verifySignatures(transaction); got != ter.TemINVALID {
					t.Errorf("opposite-era verification = %s, want temINVALID", got)
				}
				if err := PrewarmSignatureWithRules(transaction, validRules); err != nil {
					t.Errorf("matching-era prewarm after rejection: %v", err)
				}
				if got := verifyingEngine(validRules).verifySignatures(transaction); got != ter.TesSUCCESS {
					t.Errorf("matching-era verification after rejection = %s, want tesSUCCESS", got)
				}
			}
		}()
	}
	wg.Wait()
}

func roleName(role binarycodec.SigningRole) string {
	if role == binarycodec.CounterpartyRole {
		return "counterparty"
	}
	return "sponsor"
}

func TestEngineRoleSignatureFieldPresence(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, role := range []binarycodec.SigningRole{binarycodec.CounterpartyRole, binarycodec.SponsorRole} {
			t.Run(fmt.Sprintf("cleanup=%t/%s", cleanup, roleName(role)), func(t *testing.T) {
				sigcache.Reset()
				rules := cleanupCacheRules(cleanup)
				txn := roleCacheTransaction(t, role, rules)
				if role == binarycodec.CounterpartyRole {
					txn.CounterpartySignature.MarkFieldPresent("Signers")
				} else {
					txn.SponsorSignature.MarkFieldPresent("Signers")
				}
				if got := verifyingEngine(rules).verifySignatures(txn); got != ter.TemINVALID {
					t.Fatalf("single signature with present empty Signers = %s, want temINVALID", got)
				}
			})
		}
	}
}

func TestPreflightSignatureStructureHonorsDisabledChecks(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip=%t", skip), func(t *testing.T) {
			sigcache.Reset()
			txn := cacheMutationBaseTx(t)
			txn.SetPresentFields(map[string]bool{"Signers": true})
			engine := NewEngine(newMockBaseView(), txcore.EngineConfig{
				Rules: amendment.AllSupportedRules(), SkipSignatureVerification: skip,
			})
			want := ter.TemINVALID
			if skip {
				want = ter.TesSUCCESS
			}
			if got := engine.preflight(txn); got != want {
				t.Fatalf("preflight with conflicting signing fields = %s, want %s", got, want)
			}
		})
	}
}
