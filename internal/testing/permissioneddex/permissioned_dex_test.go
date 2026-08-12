// Package permissioneddex contains integration tests for PermissionedDEX behavior.
// Reference: rippled/src/test/app/PermissionedDEX_test.cpp
package permissioneddex

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	ammBuilder "github.com/LeJamon/go-xrpl/internal/testing/amm"
	cred "github.com/LeJamon/go-xrpl/internal/testing/credential"
	offerBuilder "github.com/LeJamon/go-xrpl/internal/testing/offer"
	paymentBuilder "github.com/LeJamon/go-xrpl/internal/testing/payment"
	pd "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	trustsetBuilder "github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
)

// usdPath returns a path through the USD order book (equivalent to rippled's path(~USD)).
func usdPath(gw *jtx.Account) [][]payment.PathStep {
	return [][]payment.PathStep{{{Currency: "USD", Issuer: gw.Address}}}
}

// xrpUsdEurPath returns a path through XRP→USD→EUR books.
func xrpUsdEurPath(gw *jtx.Account) [][]payment.PathStep {
	return [][]payment.PathStep{{
		{Currency: "USD", Issuer: gw.Address},
		{Currency: "EUR", Issuer: gw.Address},
	}}
}

// badDomain is a nonexistent domain ID (hex).
const badDomain = "F10D0CC9A0F9A3CBF585B80BE09A186483668FDBDD39AA7E3370F3649CE134E5"

// parseDomainID parses a hex domain ID into a [32]byte.
func parseDomainID(hexStr string) [32]byte {
	b, _ := hex.DecodeString(hexStr)
	var id [32]byte
	copy(id[:], b)
	return id
}

func requireDirectoryMembership(t *testing.T, env *jtx.TestEnv, dir keylet.Keylet, item [32]byte, want bool) {
	t.Helper()
	found := false
	err := state.DirForEach(env.Ledger(), dir, func(key [32]byte) error {
		if key == item {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("iterate directory %x: %v", dir.Key, err)
	}
	if found != want {
		t.Fatalf("directory %x membership for %x: got %t, want %t", dir.Key, item, found, want)
	}
}

// TestPermissionedDEX_ZeroDomainID verifies that a zero DomainID is rejected as
// temMALFORMED in OfferCreate and Payment once fixCleanup3_2_0 is enabled. A zero
// DomainID can never name a PermissionedDomain ledger entry.
// Reference: rippled PermissionedDEX_test testOfferCreate / testPayment zero-domain arms.
func TestPermissionedDEX_ZeroDomainID(t *testing.T) {
	var zeroDomain [32]byte
	zeroHex := hex.EncodeToString(zeroDomain[:])

	t.Run("OfferCreate_fixEnabled_temMALFORMED", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(zeroDomain).Build(),
		)
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// With the fix disabled the zero-DomainID preflight check is absent, so the
	// offer passes preflight (rippled skips its own disabled arm because a
	// zero-key keylet read can crash an assert-enabled build; goXRPL's read is
	// safe and simply finds no domain). We only assert the check did not fire.
	t.Run("OfferCreate_fixDisabled_passesPreflight", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixCleanup3_2_0")
		dex := SetupPermissionedDEX(t, env)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(zeroDomain).Build(),
		)
		if result.Code == "temMALFORMED" {
			t.Errorf("expected zero-DomainID to pass preflight with fixCleanup3_2_0 disabled, got temMALFORMED")
		}
	})

	t.Run("Payment_fixEnabled_temMALFORMED", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)
		result := env.Submit(
			paymentBuilder.PayIssued(dex.Bob, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(zeroHex).Build(),
		)
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	t.Run("Payment_fixDisabled_passesPreflight", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixCleanup3_2_0")
		dex := SetupPermissionedDEX(t, env)
		result := env.Submit(
			paymentBuilder.PayIssued(dex.Bob, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(zeroHex).Build(),
		)
		if result.Code == "temMALFORMED" {
			t.Errorf("expected zero-DomainID to pass preflight with fixCleanup3_2_0 disabled, got temMALFORMED")
		}
	})
}

// TestPermissionedDEX_CancelRegularOfferWithDomainCreate exercises the
// ValidPermissionedDEX deletion fix: a domain OfferCreate that cancels the
// submitter's own regular (non-domain) offer via OfferSequence succeeds once
// fixCleanup3_2_0 is enabled, but pre-amendment the invariant flags the deleted
// regular offer and the transaction fails with tecINVARIANT_FAILED.
// Reference: rippled PermissionedDEX_test testCancelRegularOfferWithDomainCreate (#7118).
func TestPermissionedDEX_CancelRegularOfferWithDomainCreate(t *testing.T) {
	run := func(t *testing.T, fixOn bool) {
		env := jtx.NewTestEnv(t)
		if !fixOn {
			env.DisableFeature("fixCleanup3_2_0")
		}
		dex := SetupPermissionedDEX(t, env)

		// bob places a regular (non-domain) offer.
		regularSeq := env.Seq(dex.Bob)
		jtx.RequireTxSuccess(t, env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).Build()))
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, regularSeq)

		// bob places a domain offer that cancels the regular one via OfferSequence.
		domainSeq := env.Seq(dex.Bob)
		res := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(20_000_000), dex.USD(20)).
				DomainID(dex.DomainID).OfferSequence(regularSeq).Build())
		env.Close()

		if fixOn {
			jtx.RequireTxSuccess(t, res)
			offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, regularSeq)
			offerBuilder.RequireOfferInLedger(t, env, dex.Bob, domainSeq)
		} else {
			jtx.RequireTxClaimed(t, res, "tecINVARIANT_FAILED")
			offerBuilder.RequireOfferInLedger(t, env, dex.Bob, regularSeq)
			offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, domainSeq)
		}
	}

	t.Run("fixOn", func(t *testing.T) { run(t, true) })
	t.Run("fixOff", func(t *testing.T) { run(t, false) })
}

// TestPermissionedDEX_OfferCreate tests OfferCreate with domain IDs.
// Reference: rippled PermissionedDEX_test::testOfferCreate
func TestPermissionedDEX_OfferCreate(t *testing.T) {
	// preflight - temDISABLED without PermissionedDEX amendment
	t.Run("temDISABLED", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("PermissionedDEX")
		dex := SetupPermissionedDEX(t, env)

		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxFail(t, result, "temDISABLED")
		env.Close()

		// Re-enable and it should work
		env.EnableFeature("PermissionedDEX")
		env.Close()

		bobSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// preclaim - non-domain account cannot create domain offer
	t.Run("NonDomainAccount", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		devin := jtx.NewAccount("devin")
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(devin, dex.USD(1000))
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, devin, dex.USD(100)).Build())
		env.Close()

		// devin not in domain
		result := env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()

		// domainOwner issues credential for devin
		result = env.Submit(cred.CredentialCreateHex(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// devin still can't create offer - hasn't accepted
		result = env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()

		// devin accepts credential
		result = env.Submit(cred.CredentialAcceptHex(devin, dex.DomainOwner, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// now devin can create domain offer
		result = env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// preclaim - expired credential cannot create domain offer
	t.Run("ExpiredCredential", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		devin := jtx.NewAccount("devin")
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(devin, dex.USD(1000))
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, devin, dex.USD(100)).Build())
		env.Close()

		// Issue credential with 20s expiry
		expiration := env.NowRipple() + 20
		result := env.Submit(
			cred.CredentialCreateHex(dex.DomainOwner, devin, dex.CredType).
				Expiration(expiration).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(cred.CredentialAcceptHex(devin, dex.DomainOwner, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// devin can create offer while cred is valid
		result = env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Advance time 25+ seconds to expire the credential
		env.AdvanceTime(25 * time.Second)
		env.Close()

		// devin can no longer create offer
		result = env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()
	})

	// preclaim - cannot create offer in non-existent domain
	t.Run("NonExistentDomain", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)
		_ = dex

		badDomainID := parseDomainID(badDomain)

		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(badDomainID).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()
	})

	// apply - offer can be created even if issuer is not in domain
	t.Run("IssuerNotInDomain", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// remove gw from domain
		result := env.Submit(
			cred.CredentialDeleteHex(dex.DomainOwner, dex.GW, dex.DomainOwner, dex.CredType).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob can still create domain offer even though USD issuer (gw) is not in domain
		bobSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// apply - offer can be created even if takerpays issuer is not in domain
	t.Run("TakerPaysIssuerNotInDomain", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// remove gw from domain
		result := env.Submit(
			cred.CredentialDeleteHex(dex.DomainOwner, dex.GW, dex.DomainOwner, dex.CredType).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob can still create domain offer even though USD issuer (gw) is takerpays
		bobSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, dex.USD(10), jtx.XRPTxAmount(10_000_000)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// apply - two domain offers cross with each other
	t.Run("TwoDomainOffersCross", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// bob creates a domain offer (XRP→USD)
		bobSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)

		// carol creates a regular (non-domain) offer - should NOT cross bob's domain offer
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Carol, dex.USD(10), jtx.XRPTxAmount(10_000_000)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		// Bob's domain offer should still exist (not crossed by carol's regular offer)
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)

		// alice creates a domain offer (USD→XRP) - should cross with bob's domain offer
		aliceSeq := env.Seq(dex.Alice)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Alice, dex.USD(10), jtx.XRPTxAmount(10_000_000)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Both offers should be consumed
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Alice, aliceSeq)
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// apply - create lots of domain offers and cancel them
	t.Run("LotsOfDomainOffers", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		var offerSeqs []uint32
		for i := 0; i <= 100; i++ {
			bobSeq := env.Seq(dex.Bob)
			offerSeqs = append(offerSeqs, bobSeq)
			result := env.Submit(
				offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
					DomainID(dex.DomainID).Build(),
			)
			jtx.RequireTxSuccess(t, result)
			env.Close()
			offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
		}

		for _, seq := range offerSeqs {
			result := env.Submit(offerBuilder.OfferCancel(dex.Bob, seq).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, seq)
		}
	})
}

// TestPermissionedDEX_Payment tests Payment with domain IDs.
// Reference: rippled PermissionedDEX_test::testPayment
func TestPermissionedDEX_Payment(t *testing.T) {
	// preflight - temDISABLED without PermissionedDEX amendment
	t.Run("temDISABLED", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("PermissionedDEX")
		dex := SetupPermissionedDEX(t, env)

		result := env.Submit(
			paymentBuilder.PayIssued(dex.Bob, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxFail(t, result, "temDISABLED")
		env.Close()

		// Re-enable
		env.EnableFeature("PermissionedDEX")
		env.Close()

		// Create a domain offer
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now domain payment works
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Bob, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// preclaim - non-existent domain returns tecNO_PERMISSION
	t.Run("NonExistentDomain", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		result := env.Submit(
			paymentBuilder.PayIssued(dex.Bob, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(badDomain).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()
	})

	// preclaim - non-domain destination fails
	t.Run("NonDomainDestination", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		devin := jtx.NewAccount("devin")
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(devin, dex.USD(1000))
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, devin, dex.USD(100)).Build())
		env.Close()

		// devin is not in the domain
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, devin, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()

		// Issue credential for devin
		result = env.Submit(cred.CredentialCreateHex(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Still fails - not accepted
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, devin, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()

		// devin accepts credential
		result = env.Submit(cred.CredentialAcceptHex(devin, dex.DomainOwner, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now payment succeeds
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, devin, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// preclaim - non-domain sender fails
	t.Run("NonDomainSender", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		devin := jtx.NewAccount("devin")
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(devin, dex.USD(1000))
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, devin, dex.USD(100)).Build())
		env.Close()

		// devin not in domain
		result = env.Submit(
			paymentBuilder.PayIssued(devin, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()

		// Issue credential for devin
		result = env.Submit(cred.CredentialCreateHex(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Still fails - not accepted
		result = env.Submit(
			paymentBuilder.PayIssued(devin, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
		env.Close()

		result = env.Submit(cred.CredentialAcceptHex(devin, dex.DomainOwner, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Now devin can send domain payment
		result = env.Submit(
			paymentBuilder.PayIssued(devin, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// apply - domain owner can always send and receive domain payment
	t.Run("DomainOwnerCanAlwaysSendReceive", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// create bob's domain offer
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// domain owner can be destination
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.DomainOwner, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob creates another offer
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// domain owner can send
		result = env.Submit(
			paymentBuilder.PayIssued(dex.DomainOwner, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// TestPermissionedDEX_BookStep tests domain payment consuming offers via book steps.
// Reference: rippled PermissionedDEX_test::testBookStep
func TestPermissionedDEX_BookStep(t *testing.T) {
	// Domain payment cannot consume regular offers
	t.Run("DomainPaymentCannotConsumeRegularOffer", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// Create a regular (non-domain) offer
		regularSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, regularSeq)

		// Domain payment cannot consume regular offer
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
		env.Close()

		// Create a domain offer
		domainSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, domainSeq)

		// Domain payment now consumes the domain offer
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Domain offer consumed, regular offer untouched
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, domainSeq)
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, regularSeq)
	})

	// Domain payment consuming two offers in path
	t.Run("TwoOffersInPath", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		EUR := func(amount float64) tx.Amount { return jtx.IssuedCurrency(dex.GW, "EUR", amount) }

		// Set up EUR trust lines and fund bob
		for _, acc := range []*jtx.Account{dex.Alice, dex.Bob, dex.Carol} {
			result := env.Submit(trustsetBuilder.TrustLine(acc, "EUR", dex.GW, "1000").Build())
			jtx.RequireTxSuccess(t, result)
		}
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, dex.Bob, EUR(100)).Build())
		env.Close()

		// bob creates XRP/USD domain offer
		usdOfferSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// payment fails - no EUR offer yet
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, EUR(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(xrpUsdEurPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
		env.Close()

		// bob creates regular USD/EUR offer - domain payment can't use it
		regularOfferSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, dex.USD(10), EUR(10)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Still fails - regular offer can't be consumed in domain payment
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, EUR(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(xrpUsdEurPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
		env.Close()

		// bob creates domain USD/EUR offer
		eurOfferSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, dex.USD(10), EUR(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Consume half of both domain offers
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, EUR(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(xrpUsdEurPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Offers are partially consumed
		if offerBuilder.GetOffer(env, dex.Bob, usdOfferSeq) == nil {
			t.Error("USD offer should still exist (partially consumed)")
		}
		if offerBuilder.GetOffer(env, dex.Bob, eurOfferSeq) == nil {
			t.Error("EUR offer should still exist (partially consumed)")
		}

		// Consume remaining (use same explicit path)
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, EUR(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(xrpUsdEurPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Both domain offers fully consumed
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, usdOfferSeq)
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, eurOfferSeq)
		// Regular offer untouched
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, regularOfferSeq)
	})

	// Domain payment cannot consume offer from another domain
	t.Run("CannotConsumeOfferFromAnotherDomain", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		badDomainOwner := jtx.NewAccount("badDomainOwner")
		devin := jtx.NewAccount("devin")
		env.FundAmount(badDomainOwner, uint64(jtx.XRP(1000)))
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(devin, dex.USD(1000))
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, devin, dex.USD(100)).Build())
		env.Close()

		const badCredType = "6261644372656400000000000000" // hex-encoded
		// Create a second domain
		badDomainSeq := env.Seq(badDomainOwner)
		result := env.Submit(
			pd.DomainSet(badDomainOwner).Credential(badDomainOwner, badCredType).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		badDomainID := keylet.PermissionedDomain(badDomainOwner.ID, badDomainSeq).Key

		// devin gets credential for bad domain
		result = env.Submit(cred.CredentialCreateHex(badDomainOwner, devin, badCredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(cred.CredentialAcceptHex(devin, badDomainOwner, badCredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// devin creates an offer in the bad domain
		devinSeq := env.Seq(devin)
		result = env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(badDomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Domain payment from dex can't consume devin's offer in bad domain
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
		env.Close()

		// bob creates offer in the correct domain
		bobSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Domain payment succeeds consuming bob's offer
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, bobSeq)
		offerBuilder.RequireOfferInLedger(t, env, devin, devinSeq)
	})

	// Offer becomes unfunded when owner's credential expires
	t.Run("OfferUnfundedOnCredentialExpiry", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		devin := jtx.NewAccount("devin")
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()
		env.Trust(devin, dex.USD(1000))
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, devin, dex.USD(100)).Build())
		env.Close()

		// Issue credential with 20-second expiry.
		// CredentialCreate and CredentialAccept are submitted in the same ledger
		// (no Close between them), matching rippled's testBookStep behavior where
		// both are applied before env.close() so only 2 closes happen before the
		// first payment instead of 3 (which would exceed the 20s expiration).
		expiration := env.NowRipple() + 20
		result := env.Submit(
			cred.CredentialCreateHex(dex.DomainOwner, devin, dex.CredType).
				Expiration(expiration).Build(),
		)
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(cred.CredentialAcceptHex(devin, dex.DomainOwner, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create domain offer while credential is still valid
		devinSeq := env.Seq(devin)
		result = env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Offer can be consumed while credential is valid
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, devin, devinSeq)

		// Advance time past expiry
		env.AdvanceTime(25 * time.Second)
		env.Close()

		// Offer is now unfunded (credential expired)
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
		env.Close()
		// Offer still exists (just unfunded, not removed)
		offerBuilder.RequireOfferInLedger(t, env, devin, devinSeq)
	})

	// Offer becomes unfunded when owner's credential is removed
	t.Run("OfferUnfundedOnCredentialRemoval", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// Create bob's domain offer
		bobSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Offer can be consumed while credential exists
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)

		// Remove bob's credential
		result = env.Submit(
			cred.CredentialDeleteHex(dex.DomainOwner, dex.Bob, dex.DomainOwner, dex.CredType).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Bob's offer is now unfunded
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// Sanity check: devin, who is part of the domain but doesn't have a
	// trustline with USD issuer, can successfully make a payment using offer
	t.Run("MemberWithoutTrustlineCanPayViaOffer", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// fund devin but don't create a USD trustline with gateway
		devin := jtx.NewAccount("devin")
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()

		// domain owner issues credential for devin
		result = env.Submit(cred.CredentialCreateHex(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(cred.CredentialAcceptHex(devin, dex.DomainOwner, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// successful payment because offer is consumed
		result = env.Submit(
			paymentBuilder.PayIssued(devin, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// TestPermissionedDEX_Rippling tests non-domain accounts can be part of rippling
// in a domain payment.
// Reference: rippled PermissionedDEX_test::testRippling
func TestPermissionedDEX_Rippling(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)

	// alice is EUR issuer for bob; bob is EUR issuer for carol
	// bob trusts alice's EUR
	result := env.Submit(trustsetBuilder.TrustLine(dex.Bob, "EUR", dex.Alice, "100").Build())
	jtx.RequireTxSuccess(t, result)
	// carol trusts bob's EUR
	result = env.Submit(trustsetBuilder.TrustLine(dex.Carol, "EUR", dex.Bob, "100").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Remove bob from domain
	result = env.Submit(
		cred.CredentialDeleteHex(dex.DomainOwner, dex.Bob, dex.DomainOwner, dex.CredType).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice can still ripple through bob even though he's not in the domain
	// path: alice's EUR → bob's EUR trustline → carol
	// In rippled, paths(EURA) triggers the pathfinder which discovers Bob as the
	// intermediate hop. Since go-xrpl doesn't have a pathfinder yet, we manually
	// specify the equivalent resolved path {Account: Bob}.
	// TODO: replace with pathfinder-based path resolution once pathfinding is implemented.
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, jtx.IssuedCurrency(dex.Bob, "EUR", 10)).
			Paths([][]payment.PathStep{{{Account: dex.Bob.Address}}}).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// carol sets NoRipple on bob's EUR trust line with limit 0
	// Reference: rippled trust(carol, bob["EUR"](0), bob, tfSetNoRipple)
	// The limit 0 combined with NoRipple prevents further rippling
	result = env.Submit(
		trustsetBuilder.TrustLine(dex.Carol, "EUR", dex.Bob, "0").NoRipple().Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Payment no longer works because carol has NoRipple set with limit 0
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, jtx.IssuedCurrency(dex.Bob, "EUR", 5)).
			Paths([][]payment.PathStep{{{Account: dex.Bob.Address}}}).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_DRY")
	env.Close()
}

// TestPermissionedDEX_OfferTokenIssuerInDomain verifies token issuers are not
// required to be in the domain.
// Reference: rippled PermissionedDEX_test::testOfferTokenIssuerInDomain
func TestPermissionedDEX_OfferTokenIssuerInDomain(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)

	// bob creates XRP/USD offer (takergets=USD)
	offer1Seq := env.Seq(dex.Bob)
	result := env.Submit(
		offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
			DomainID(dex.DomainID).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob creates USD/XRP offer (takerpays=USD)
	offer2Seq := env.Seq(dex.Bob)
	result = env.Submit(
		offerBuilder.OfferCreate(dex.Bob, dex.USD(10), jtx.XRPTxAmount(10_000_000)).
			DomainID(dex.DomainID).Passive().Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, offer1Seq)
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, offer2Seq)

	// Remove gateway from domain
	result = env.Submit(
		cred.CredentialDeleteHex(dex.DomainOwner, dex.GW, dex.DomainOwner, dex.CredType).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// XRP/USD offer is consumed even though issuer is not in domain
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(10)).
			SendMax(jtx.XRPTxAmount(10_000_000)).
			Paths(usdPath(dex.GW)).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, offer1Seq)

	// USD/XRP offer is consumed even though issuer is not in domain
	result = env.Submit(
		paymentBuilder.Pay(dex.Alice, dex.Carol, 10_000_000).
			SendMax(dex.USD(10)).
			Paths([][]payment.PathStep{{{Currency: "XRP", Type: int(payment.PathTypeCurrency)}}}).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, offer2Seq)
}

// TestPermissionedDEX_RemoveUnfundedOffer tests that an unfunded offer is implicitly
// removed by a successful payment.
// Reference: rippled PermissionedDEX_test::testRemoveUnfundedOffer
func TestPermissionedDEX_RemoveUnfundedOffer(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)

	// alice and bob both create domain offers
	aliceSeq := env.Seq(dex.Alice)
	result := env.Submit(
		offerBuilder.OfferCreate(dex.Alice, jtx.XRPTxAmount(100_000_000), dex.USD(100)).
			DomainID(dex.DomainID).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	bobSeq := env.Seq(dex.Bob)
	result = env.Submit(
		offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(20_000_000), dex.USD(20)).
			DomainID(dex.DomainID).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	offerBuilder.RequireOfferInLedger(t, env, dex.Alice, aliceSeq)
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)

	// Remove alice from domain - her offer becomes unfunded
	result = env.Submit(
		cred.CredentialDeleteHex(dex.DomainOwner, dex.Alice, dex.DomainOwner, dex.CredType).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// A successful payment to carol should consume bob's offer and implicitly remove alice's unfunded offer
	result = env.Submit(
		paymentBuilder.PayIssued(dex.GW, dex.Carol, dex.USD(10)).
			SendMax(jtx.XRPTxAmount(10_000_000)).
			Paths(usdPath(dex.GW)).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Bob's offer is partially consumed
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	// Alice's unfunded offer is implicitly removed
	offerBuilder.RequireNoOfferInLedger(t, env, dex.Alice, aliceSeq)
}

// TestPermissionedDEX_AmmNotUsed tests that domain payments cannot consume AMM liquidity.
// Reference: rippled PermissionedDEX_test::testAmmNotUsed
func TestPermissionedDEX_AmmNotUsed(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)

	// Create AMM with alice: XRP(10) / USD(50)
	ammCreateTx := ammBuilder.AMMCreate(dex.Alice, jtx.XRPTxAmount(10_000_000), dex.USD(50)).Build()
	result := env.Submit(ammCreateTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// a domain payment isn't able to consume AMM
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Bob, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
	env.Close()

	// a non domain payment can use AMM
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Bob, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// USD amount in AMM is changed (from 50 to 45)
	jtx.RequireIOUBalance(t, env, dex.Carol, dex.GW, "USD", 105)
}

// TestPermissionedDEX_AmmQualityNotLeaked verifies that an AMM which cannot be
// crossed from a permissioned domain book does not influence path ranking once
// fixCleanup3_3_0 is enabled. The disabled case preserves the legacy ranking.
// Reference: rippled PermissionedDEX_test::testAmmQualityNotLeaked.
func TestPermissionedDEX_AmmQualityNotLeaked(t *testing.T) {
	for _, test := range []struct {
		name       string
		fixEnabled bool
		wantUSD    float64
		wantDirect bool
	}{
		{name: "amendment_off", wantUSD: 10, wantDirect: false},
		{name: "amendment_on", fixEnabled: true, wantUSD: 20, wantDirect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			dex := SetupPermissionedDEX(t, env)
			if test.fixEnabled {
				env.EnableFeature("fixCleanup3_3_0")
			} else {
				env.DisableFeature("fixCleanup3_3_0")
			}
			env.Close()

			EUR := func(amount float64) tx.Amount {
				return jtx.IssuedCurrency(dex.GW, "EUR", amount)
			}
			result := env.Submit(trustsetBuilder.TrustLine(dex.Bob, "EUR", dex.GW, "1000").Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			result = env.Submit(paymentBuilder.PayIssued(dex.GW, dex.Bob, EUR(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// The AMM makes the direct XRP/USD book look better than it is for
			// domain payments, while the two-book path returns twice as much USD.
			result = env.Submit(paymentBuilder.PayIssued(dex.GW, dex.Alice, dex.USD(500)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			result = env.Submit(ammBuilder.AMMCreate(dex.Alice, jtx.XRPTxAmount(10_000_000), dex.USD(500)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			ammData, err := env.Ledger().Read(coreAmm.ComputeAMMKeylet(
				tx.Asset{Currency: "XRP"},
				tx.Asset{Currency: "USD", Issuer: dex.GW.Address},
			))
			if err != nil || ammData == nil {
				t.Fatalf("read AMM entry: %v", err)
			}
			pool, err := coreAmm.ParseAMMData(ammData)
			if err != nil {
				t.Fatalf("parse AMM entry: %v", err)
			}
			ammAccount := &jtx.Account{ID: pool.Account}
			xrpBefore := env.Balance(ammAccount)
			usdBefore := env.BalanceIOU(ammAccount, "USD", dex.GW)
			if xrpBefore != uint64(jtx.XRP(10)) || usdBefore != 500 {
				t.Fatalf("AMM balances before payment = XRP %d/USD %v, want XRP %d/USD 500", xrpBefore, usdBefore, jtx.XRP(10))
			}

			directSeq := env.Seq(dex.Bob)
			result = env.Submit(
				offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
					DomainID(dex.DomainID).Build(),
			)
			jtx.RequireTxSuccess(t, result)
			env.Close()

			xrpEURSeq := env.Seq(dex.Bob)
			result = env.Submit(
				offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), EUR(20)).
					DomainID(dex.DomainID).Build(),
			)
			jtx.RequireTxSuccess(t, result)
			env.Close()

			eurUSDSeq := env.Seq(dex.Bob)
			result = env.Submit(
				offerBuilder.OfferCreate(dex.Bob, EUR(20), dex.USD(20)).
					DomainID(dex.DomainID).Build(),
			)
			jtx.RequireTxSuccess(t, result)
			env.Close()

			carolBalanceBefore := env.IOUBalance(dex.Carol, dex.GW, "USD").Float64()
			result = env.Submit(
				paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(100)).
					SendMax(jtx.XRPTxAmount(10_000_000)).
					Paths([][]payment.PathStep{
						{{Currency: "USD", Issuer: dex.GW.Address}},
						{{Currency: "EUR", Issuer: dex.GW.Address}, {Currency: "USD", Issuer: dex.GW.Address}},
					}).
					PartialPayment().
					NoDirectRipple().
					DomainID(dex.DomainIDHex).Build(),
			)
			jtx.RequireTxSuccess(t, result)
			env.Close()

			carolBalanceAfter := env.IOUBalance(dex.Carol, dex.GW, "USD").Float64()
			if got := carolBalanceAfter - carolBalanceBefore; got != test.wantUSD {
				t.Fatalf("delivered USD = %v, want %v", got, test.wantUSD)
			}
			if got := env.Balance(ammAccount); got != xrpBefore {
				t.Fatalf("AMM XRP balance after payment = %d, want %d", got, xrpBefore)
			}
			if got := env.BalanceIOU(ammAccount, "USD", dex.GW); got != usdBefore {
				t.Fatalf("AMM USD balance after payment = %v, want %v", got, usdBefore)
			}
			if test.wantDirect {
				offerBuilder.RequireOfferInLedger(t, env, dex.Bob, directSeq)
				offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, xrpEURSeq)
				offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, eurUSDSeq)
			} else {
				offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, directSeq)
				offerBuilder.RequireOfferInLedger(t, env, dex.Bob, xrpEURSeq)
				offerBuilder.RequireOfferInLedger(t, env, dex.Bob, eurUSDSeq)
			}
		})
	}
}

// TestPermissionedDEX_AutoBridge tests that domain offers can be auto-bridged.
// Reference: rippled PermissionedDEX_test::testAutoBridge
func TestPermissionedDEX_AutoBridge(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)

	EUR := func(amount float64) tx.Amount { return jtx.IssuedCurrency(dex.GW, "EUR", amount) }

	for _, acc := range []*jtx.Account{dex.Alice, dex.Bob, dex.Carol} {
		result := env.Submit(trustsetBuilder.TrustLine(acc, "EUR", dex.GW, "10000").Build())
		jtx.RequireTxSuccess(t, result)
	}
	env.Close()

	env.Submit(paymentBuilder.PayIssued(dex.GW, dex.Carol, EUR(1)).Build())
	env.Close()

	// alice creates XRP/USD domain offer, bob creates EUR/XRP domain offer
	aliceSeq := env.Seq(dex.Alice)
	result := env.Submit(
		offerBuilder.OfferCreate(dex.Alice, jtx.XRPTxAmount(100_000_000), dex.USD(1)).
			DomainID(dex.DomainID).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	bobSeq := env.Seq(dex.Bob)
	result = env.Submit(
		offerBuilder.OfferCreate(dex.Bob, EUR(1), jtx.XRPTxAmount(100_000_000)).
			DomainID(dex.DomainID).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// carol creates a USD/EUR domain offer - auto-bridge should cross all three
	carolSeq := env.Seq(dex.Carol)
	result = env.Submit(
		offerBuilder.OfferCreate(dex.Carol, dex.USD(1), EUR(1)).
			DomainID(dex.DomainID).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// All three offers should be consumed through auto-bridging
	offerBuilder.RequireNoOfferInLedger(t, env, dex.Alice, aliceSeq)
	offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, bobSeq)
	offerBuilder.RequireNoOfferInLedger(t, env, dex.Carol, carolSeq)
}

// TestPermissionedDEX_HybridOfferCreate tests hybrid offer creation.
// Reference: rippled PermissionedDEX_test::testHybridOfferCreate
func TestPermissionedDEX_HybridOfferCreate(t *testing.T) {
	// Flag validation: temDISABLED and temINVALID_FLAG
	t.Run("FlagValidation", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("PermissionedDEX")
		dex := SetupPermissionedDEX(t, env)

		// Without PermissionedDEX, tfHybrid + domain → temDISABLED
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxFail(t, result, "temDISABLED")
		env.Close()

		// tfHybrid without domain → temINVALID_FLAG (even without amendment)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				Hybrid().Build(),
		)
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
		env.Close()

		// Enable PermissionedDEX
		env.EnableFeature("PermissionedDEX")
		env.Close()

		// tfHybrid without domain still fails
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				Hybrid().Build(),
		)
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
		env.Close()

		// tfHybrid with domain succeeds
		bobSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// Domain offer crosses with hybrid
	t.Run("DomainOfferCrossesHybrid", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// bob creates hybrid offer
		bobSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)

		// alice creates a domain offer - should cross bob's hybrid
		aliceSeq := env.Seq(dex.Alice)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Alice, dex.USD(10), jtx.XRPTxAmount(10_000_000)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireNoOfferInLedger(t, env, dex.Alice, aliceSeq)
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// Open offer crosses with hybrid
	t.Run("OpenOfferCrossesHybrid", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// bob creates hybrid offer
		bobSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
		hybridOffer := offerBuilder.GetOffer(env, dex.Bob, bobSeq)
		if hybridOffer == nil {
			t.Fatal("hybrid offer missing after creation")
		}
		offerKey := keylet.Offer(dex.Bob.ID, bobSeq).Key
		ownerDir := keylet.OwnerDir(dex.Bob.ID)
		primaryDir := keylet.Keylet{Key: hybridOffer.BookDirectory}
		additionalDir := keylet.Keylet{Key: hybridOffer.AdditionalBookDirectory}
		for _, dir := range []keylet.Keylet{ownerDir, primaryDir, additionalDir} {
			requireDirectoryMembership(t, env, dir, offerKey, true)
		}
		ownerCount := env.OwnerCount(dex.Bob)

		// alice creates regular (non-domain) offer - should cross bob's hybrid (in open book)
		aliceSeq := env.Seq(dex.Alice)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Alice, dex.USD(10), jtx.XRPTxAmount(10_000_000)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireNoOfferInLedger(t, env, dex.Alice, aliceSeq)
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, bobSeq)
		for _, dir := range []keylet.Keylet{ownerDir, primaryDir, additionalDir} {
			requireDirectoryMembership(t, env, dir, offerKey, false)
		}
		jtx.RequireOwnerCount(t, env, dex.Bob, ownerCount-1)
	})

	// Hybrid offer crosses with domain offer by default (looks at domain book)
	t.Run("HybridCrossesDomainOfferFirst", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// bob creates domain offer
		bobSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)

		// alice creates a hybrid offer - crosses bob's domain offer
		aliceSeq := env.Seq(dex.Alice)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Alice, dex.USD(10), jtx.XRPTxAmount(10_000_000)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireNoOfferInLedger(t, env, dex.Alice, aliceSeq)
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, bobSeq)
	})

	// Hybrid offer does NOT auto-cross with open offers (only looks at domain book by default)
	t.Run("HybridDoesNotCrossOpenOffer", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// bob creates regular offer
		bobSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)

		// alice creates a hybrid offer - does NOT cross bob's regular offer (no open book crossing)
		aliceSeq := env.Seq(dex.Alice)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Alice, dex.USD(10), jtx.XRPTxAmount(10_000_000)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice's hybrid offer exists (wasn't crossed)
		offerBuilder.RequireOfferInLedger(t, env, dex.Alice, aliceSeq)
		// bob's regular offer also still exists
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	})
}

// TestPermissionedDEX_HybridBookStep tests hybrid offers in payment book steps.
// Reference: rippled PermissionedDEX_test::testHybridBookStep
func TestPermissionedDEX_HybridBookStep(t *testing.T) {
	// Both domain and regular payments can consume hybrid offer
	t.Run("BothDomainAndOpenPaymentCanConsumeHybrid", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		hybridSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Domain payment consumes half the hybrid offer
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, hybridSeq)

		// Regular payment consumes remaining (hybrid is in open book too)
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Hybrid offer fully consumed
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, hybridSeq)
	})

	// Someone from another domain can't cross hybrid if they specified wrong domainID
	t.Run("WrongDomainCannotCrossHybrid", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		badDomainOwner := jtx.NewAccount("badDomainOwner")
		devin := jtx.NewAccount("devin")
		env.FundAmount(badDomainOwner, uint64(jtx.XRP(1000)))
		env.FundAmount(devin, uint64(jtx.XRP(1000)))
		env.Close()

		const badCredType = "6261644372656400000000000000" // hex("badCred")
		// Create a second domain
		badDomainSeq := env.Seq(badDomainOwner)
		result := env.Submit(
			pd.DomainSet(badDomainOwner).Credential(badDomainOwner, badCredType).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		badDomainIDKey := keylet.PermissionedDomain(badDomainOwner.ID, badDomainSeq).Key

		result = env.Submit(cred.CredentialCreateHex(badDomainOwner, devin, badCredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(cred.CredentialAcceptHex(devin, badDomainOwner, badCredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob creates hybrid offer in the correct domain
		hybridSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// other domain can't consume the hybrid offer
		badDomainIDHex := hex.EncodeToString(badDomainIDKey[:])
		result = env.Submit(
			paymentBuilder.PayIssued(devin, badDomainOwner, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(badDomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_DRY")
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, hybridSeq)

		// correct domain can consume the hybrid offer partially
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, hybridSeq)

		// regular payment consumes remaining
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(usdPath(dex.GW)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, hybridSeq)
	})

	// Domain payment with two offers including a hybrid
	t.Run("TwoOffersWithHybrid", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		EUR := func(amount float64) tx.Amount { return jtx.IssuedCurrency(dex.GW, "EUR", amount) }

		for _, acc := range []*jtx.Account{dex.Alice, dex.Bob, dex.Carol} {
			result := env.Submit(trustsetBuilder.TrustLine(acc, "EUR", dex.GW, "1000").Build())
			jtx.RequireTxSuccess(t, result)
		}
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, dex.Bob, EUR(100)).Build())
		env.Close()

		// bob creates XRP/USD domain offer
		usdSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Payment fails - no EUR offer
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, EUR(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(xrpUsdEurPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
		env.Close()

		// bob creates hybrid USD/EUR offer
		eurSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, dex.USD(10), EUR(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Consume half via domain payment
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, EUR(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(xrpUsdEurPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, usdSeq)
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, eurSeq)
	})

	// Regular payment uses regular offer + hybrid offer
	t.Run("RegularPaymentUsesRegularAndHybrid", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		EUR := func(amount float64) tx.Amount { return jtx.IssuedCurrency(dex.GW, "EUR", amount) }

		for _, acc := range []*jtx.Account{dex.Alice, dex.Bob, dex.Carol} {
			result := env.Submit(trustsetBuilder.TrustLine(acc, "EUR", dex.GW, "1000").Build())
			jtx.RequireTxSuccess(t, result)
		}
		env.Close()
		env.Submit(paymentBuilder.PayIssued(dex.GW, dex.Bob, EUR(100)).Build())
		env.Close()

		// bob creates regular XRP/USD offer
		usdSeq := env.Seq(dex.Bob)
		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// bob creates hybrid USD/EUR offer
		eurSeq := env.Seq(dex.Bob)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, dex.USD(10), EUR(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Regular payment uses both offers (regular USD offer + hybrid EUR offer)
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, dex.Carol, EUR(5)).
				SendMax(jtx.XRPTxAmount(5_000_000)).
				Paths(xrpUsdEurPath(dex.GW)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, usdSeq)
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, eurSeq)
	})
}

// TestPermissionedDEX_HybridInvalidOffer tests that a hybrid offer becomes
// unfunded when its owner leaves the domain.
// Reference: rippled PermissionedDEX_test::testHybridInvalidOffer
func TestPermissionedDEX_HybridInvalidOffer(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)
	env.DisableFeature("fixCleanup3_3_0")
	env.Close()

	// bob creates a hybrid offer
	hybridSeq := env.Seq(dex.Bob)
	result := env.Submit(
		offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(50_000_000), dex.USD(50)).
			DomainID(dex.DomainID).Hybrid().Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Remove bob from domain
	result = env.Submit(
		cred.CredentialDeleteHex(dex.DomainOwner, dex.Bob, dex.DomainOwner, dex.CredType).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Bob's hybrid offer can't be consumed in domain payment
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
	env.Close()
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, hybridSeq)

	// Bob's hybrid offer can't be consumed in regular payment either
	// (in open book but invalid since bob left domain)
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
	env.Close()
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, hybridSeq)

	// bob creates a new regular offer
	regularSeq := env.Seq(dex.Bob)
	result = env.Submit(
		offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, regularSeq)

	// Normal payment consumes regular offer and removes the unfunded hybrid
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Hybrid offer removed, regular offer partially consumed
	offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, hybridSeq)
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, regularSeq)
}

// TestPermissionedDEX_HybridInvalidOfferFixCleanup330 verifies that losing a
// domain credential removes hybrid liquidity only from domain-book walks. The
// open-book directory, metadata, and owner entry survive both a failed domain
// payment (rollback) and subsequent open-book crossings.
// Reference: rippled PermissionedDEX_test::testHybridInvalidOffer with
// fixCleanup3_3_0 enabled.
func TestPermissionedDEX_HybridInvalidOfferFixCleanup330(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)
	env.EnableFeature("fixCleanup3_3_0")
	env.Close()

	hybridSeq := env.Seq(dex.Bob)
	result := env.Submit(
		offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(50_000_000), dex.USD(50)).
			DomainID(dex.DomainID).Hybrid().Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	hybrid := offerBuilder.GetOffer(env, dex.Bob, hybridSeq)
	if hybrid == nil {
		t.Fatal("hybrid offer missing after creation")
	}
	offerKey := keylet.Offer(dex.Bob.ID, hybridSeq).Key
	domainDir := keylet.Keylet{Key: hybrid.BookDirectory}
	openDir := keylet.Keylet{Key: hybrid.AdditionalBookDirectory}
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)

	// Remove bob's domain credential, then verify a domain payment fails and
	// rolls back the attempted domain-book removal.
	result = env.Submit(
		cred.CredentialDeleteHex(dex.DomainOwner, dex.Bob, dex.DomainOwner, dex.CredType).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	ownerCountAfterCredential := env.OwnerCount(dex.Bob)
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
	env.Close()
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, hybridSeq)
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)
	jtx.RequireOwnerCount(t, env, dex.Bob, ownerCountAfterCredential)

	// The amendment allows the same invalid hybrid offer to remain usable from
	// the open book, where it is partially consumed rather than evicted.
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	hybrid = offerBuilder.GetOffer(env, dex.Bob, hybridSeq)
	if hybrid == nil || hybrid.TakerPays.Drops() != 45_000_000 || hybrid.TakerGets.Float64() != 45 {
		t.Fatalf("hybrid offer after open crossing = %+v, want 45 XRP/45 USD", hybrid)
	}
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)
	jtx.RequireOwnerCount(t, env, dex.Bob, ownerCountAfterCredential)

	// A newer regular offer shares the open directory. FIFO crossing must keep
	// the older hybrid offer first and leave both directory links intact.
	regularSeq := env.Seq(dex.Bob)
	result = env.Submit(
		offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	regular := offerBuilder.GetOffer(env, dex.Bob, regularSeq)
	if regular == nil {
		t.Fatal("regular offer missing after creation")
	}
	regularKey := keylet.Offer(dex.Bob.ID, regularSeq).Key
	requireDirectoryMembership(t, env, openDir, regularKey, true)

	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	hybrid = offerBuilder.GetOffer(env, dex.Bob, hybridSeq)
	regular = offerBuilder.GetOffer(env, dex.Bob, regularSeq)
	if hybrid == nil || hybrid.TakerPays.Drops() != 40_000_000 || hybrid.TakerGets.Float64() != 40 {
		t.Fatalf("hybrid offer after second open crossing = %+v, want 40 XRP/40 USD", hybrid)
	}
	if regular == nil || regular.TakerPays.Drops() != 10_000_000 || regular.TakerGets.Float64() != 10 {
		t.Fatalf("regular offer after second open crossing = %+v, want 10 XRP/10 USD", regular)
	}
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, regularKey, true)
	jtx.RequireOwnerCount(t, env, dex.Bob, ownerCountAfterCredential+1)
}

// TestPermissionedDEX_HybridOpenBookAfterCredentialExpiry verifies that an
// expired credential invalidates a hybrid offer for domain payments without
// removing its open-book liquidity. A failed domain payment must roll back the
// attempted domain-book removal, leaving the offer available for later open
// payments.
// Reference: rippled PermissionedDEX_test::testHybridOpenBookAfterCredentialExpiry.
func TestPermissionedDEX_HybridOpenBookAfterCredentialExpiry(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)
	env.EnableFeature("fixCleanup3_3_0")
	env.Close()

	devin := jtx.NewAccount("devin")
	env.FundAmount(devin, uint64(jtx.XRP(1000)))
	env.Close()
	env.Trust(devin, dex.USD(1000))
	env.Close()
	result := env.Submit(paymentBuilder.PayIssued(dex.GW, devin, dex.USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Create and accept an expiring credential in one open ledger, matching the
	// rippled fixture and leaving enough time for the hybrid setup/payment.
	expiration := env.NowRipple() + 100
	result = env.Submit(
		cred.CredentialCreateHex(dex.DomainOwner, devin, dex.CredType).
			Expiration(expiration).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(cred.CredentialAcceptHex(devin, dex.DomainOwner, dex.CredType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	hybridSeq := env.Seq(devin)
	result = env.Submit(
		offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
			DomainID(dex.DomainID).Hybrid().Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	hybrid := offerBuilder.GetOffer(env, devin, hybridSeq)
	if hybrid == nil {
		t.Fatal("hybrid offer missing after creation")
	}
	offerKey := keylet.Offer(devin.ID, hybridSeq).Key
	domainDir := keylet.Keylet{Key: hybrid.BookDirectory}
	openDir := keylet.Keylet{Key: hybrid.AdditionalBookDirectory}
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)
	ownerCount := env.OwnerCount(devin)

	// The open book can cross the hybrid offer while its credential is valid.
	carolBalance := env.BalanceIOU(dex.Carol, "USD", dex.GW)
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	if got := env.BalanceIOU(dex.Carol, "USD", dex.GW) - carolBalance; got != 5 {
		t.Fatalf("USD delivered before expiry = %v, want 5", got)
	}
	hybrid = offerBuilder.GetOffer(env, devin, hybridSeq)
	if hybrid == nil || hybrid.TakerPays.Drops() != 5_000_000 || hybrid.TakerGets.Compare(dex.USD(5)) != 0 {
		t.Fatalf("hybrid offer before expiry = %+v, want 5 XRP/5 USD", hybrid)
	}
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)
	jtx.RequireOwnerCount(t, env, devin, ownerCount)

	// Expire the credential and confirm new domain offers are rejected.
	env.AdvanceTime(100 * time.Second)
	env.Close()
	result = env.Submit(
		offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(1_000_000), dex.USD(1)).
			DomainID(dex.DomainID).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
	env.Close()
	offerBuilder.RequireOfferInLedger(t, env, devin, hybridSeq)

	// The expired hybrid remains usable from the open book.
	carolBalance = env.BalanceIOU(dex.Carol, "USD", dex.GW)
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(2)).
			SendMax(jtx.XRPTxAmount(2_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	if got := env.BalanceIOU(dex.Carol, "USD", dex.GW) - carolBalance; got != 2 {
		t.Fatalf("USD delivered after expiry = %v, want 2", got)
	}
	hybrid = offerBuilder.GetOffer(env, devin, hybridSeq)
	if hybrid == nil || hybrid.TakerPays.Drops() != 3_000_000 || hybrid.TakerGets.Compare(dex.USD(3)) != 0 {
		t.Fatalf("hybrid offer after expired open crossing = %+v, want 3 XRP/3 USD", hybrid)
	}
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)
	jtx.RequireOwnerCount(t, env, devin, ownerCount)

	// A domain payment now fails while the sandboxed domain removal is rolled
	// back, preserving the offer and both directory links.
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(1)).
			SendMax(jtx.XRPTxAmount(1_000_000)).
			Paths(usdPath(dex.GW)).
			DomainID(dex.DomainIDHex).Build(),
	)
	jtx.RequireTxClaimed(t, result, "tecPATH_PARTIAL")
	env.Close()
	hybrid = offerBuilder.GetOffer(env, devin, hybridSeq)
	if hybrid == nil || hybrid.TakerPays.Drops() != 3_000_000 || hybrid.TakerGets.Compare(dex.USD(3)) != 0 {
		t.Fatalf("hybrid offer after failed domain payment = %+v, want 3 XRP/3 USD", hybrid)
	}
	requireDirectoryMembership(t, env, domainDir, offerKey, true)
	requireDirectoryMembership(t, env, openDir, offerKey, true)
	jtx.RequireOwnerCount(t, env, devin, ownerCount)

	// The remaining offer can be fully consumed from the open book.
	carolBalance = env.BalanceIOU(dex.Carol, "USD", dex.GW)
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(3)).
			SendMax(jtx.XRPTxAmount(3_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	if got := env.BalanceIOU(dex.Carol, "USD", dex.GW) - carolBalance; got != 3 {
		t.Fatalf("USD delivered on final open crossing = %v, want 3", got)
	}
	offerBuilder.RequireNoOfferInLedger(t, env, devin, hybridSeq)
	requireDirectoryMembership(t, env, domainDir, offerKey, false)
	requireDirectoryMembership(t, env, openDir, offerKey, false)
	jtx.RequireOwnerCount(t, env, devin, ownerCount-1)
}

// TestPermissionedDEX_HybridOfferDirectories tests that hybrid offers appear in
// both domain and open book directories.
// Reference: rippled PermissionedDEX_test::testHybridOfferDirectories
func TestPermissionedDEX_HybridOfferDirectories(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)

	const dirCount = 100
	offerSeqs := make([]uint32, 0, dirCount)
	offerDirs := make(map[uint32][]keylet.Keylet, dirCount)

	for range dirCount {
		bobSeq := env.Seq(dex.Bob)
		offerSeqs = append(offerSeqs, bobSeq)

		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
		offer := offerBuilder.GetOffer(env, dex.Bob, bobSeq)
		if offer == nil {
			t.Fatalf("hybrid offer %d missing after creation", bobSeq)
		}
		offerKey := keylet.Offer(dex.Bob.ID, bobSeq).Key
		offerDirs[bobSeq] = []keylet.Keylet{
			keylet.OwnerDir(dex.Bob.ID),
			{Key: offer.BookDirectory},
			{Key: offer.AdditionalBookDirectory},
		}
		for _, dir := range offerDirs[bobSeq] {
			requireDirectoryMembership(t, env, dir, offerKey, true)
		}
	}

	// Cancel all hybrid offers - they should be removed from both directories
	for _, seq := range offerSeqs {
		ownerCount := env.OwnerCount(dex.Bob)
		result := env.Submit(offerBuilder.OfferCancel(dex.Bob, seq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, seq)
		offerKey := keylet.Offer(dex.Bob.ID, seq).Key
		for _, dir := range offerDirs[seq] {
			requireDirectoryMembership(t, env, dir, offerKey, false)
		}
		jtx.RequireOwnerCount(t, env, dex.Bob, ownerCount-1)
	}
}
