// Package permissioneddex contains integration tests for PermissionedDEX behavior.
// Reference: rippled/src/test/app/PermissionedDEX_test.cpp
package permissioneddex

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	ammBuilder "github.com/LeJamon/goXRPLd/internal/testing/amm"
	cred "github.com/LeJamon/goXRPLd/internal/testing/credential"
	offerBuilder "github.com/LeJamon/goXRPLd/internal/testing/offer"
	paymentBuilder "github.com/LeJamon/goXRPLd/internal/testing/payment"
	pd "github.com/LeJamon/goXRPLd/internal/testing/permissioneddomain"
	trustsetBuilder "github.com/LeJamon/goXRPLd/internal/testing/trustset"
)

// requireResult asserts the transaction result matches the expected code.
// Handles both tem (malformed/rejected) and tec (claimed) result codes.
func requireResult(t *testing.T, result jtx.TxResult, code string) {
	t.Helper()
	if result.Code != code {
		t.Errorf("Expected result code %s, got %s: %s", code, result.Code, result.Message)
	}
}

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

// rippleTimeNow returns the current ledger time as Ripple epoch seconds.
// The Ripple epoch is Jan 1, 2000 00:00:00 UTC (946684800 Unix seconds).
func rippleTimeNow(env *jtx.TestEnv) uint32 {
	const rippleEpoch int64 = 946684800
	return uint32(env.Now().Unix() - rippleEpoch)
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
		requireResult(t, result, "temDISABLED")
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
		requireResult(t, result, "tecNO_PERMISSION")
		env.Close()

		// domainOwner issues credential for devin
		result = env.Submit(cred.CredentialCreate(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// devin still can't create offer - hasn't accepted
		result = env.Submit(
			offerBuilder.OfferCreate(devin, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Build(),
		)
		requireResult(t, result, "tecNO_PERMISSION")
		env.Close()

		// devin accepts credential
		result = env.Submit(cred.CredentialAccept(devin, dex.DomainOwner, dex.CredType).Build())
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
		expiration := rippleTimeNow(env) + 20
		result := env.Submit(
			cred.CredentialCreate(dex.DomainOwner, devin, dex.CredType).
				Expiration(expiration).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(cred.CredentialAccept(devin, dex.DomainOwner, dex.CredType).Build())
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
		requireResult(t, result, "tecNO_PERMISSION")
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
		requireResult(t, result, "tecNO_PERMISSION")
		env.Close()
	})

	// apply - offer can be created even if issuer is not in domain
	t.Run("IssuerNotInDomain", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		dex := SetupPermissionedDEX(t, env)

		// remove gw from domain
		result := env.Submit(
			cred.CredentialDelete(dex.DomainOwner, dex.GW, dex.DomainOwner, dex.CredType).Build(),
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
			cred.CredentialDelete(dex.DomainOwner, dex.GW, dex.DomainOwner, dex.CredType).Build(),
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
		requireResult(t, result, "temDISABLED")
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
		requireResult(t, result, "tecNO_PERMISSION")
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
		requireResult(t, result, "tecNO_PERMISSION")
		env.Close()

		// Issue credential for devin
		result = env.Submit(cred.CredentialCreate(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Still fails - not accepted
		result = env.Submit(
			paymentBuilder.PayIssued(dex.Alice, devin, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		requireResult(t, result, "tecNO_PERMISSION")
		env.Close()

		// devin accepts credential
		result = env.Submit(cred.CredentialAccept(devin, dex.DomainOwner, dex.CredType).Build())
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
		requireResult(t, result, "tecNO_PERMISSION")
		env.Close()

		// Issue credential for devin
		result = env.Submit(cred.CredentialCreate(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Still fails - not accepted
		result = env.Submit(
			paymentBuilder.PayIssued(devin, dex.Alice, dex.USD(10)).
				SendMax(jtx.XRPTxAmount(10_000_000)).
				Paths(usdPath(dex.GW)).
				DomainID(dex.DomainIDHex).Build(),
		)
		requireResult(t, result, "tecNO_PERMISSION")
		env.Close()

		result = env.Submit(cred.CredentialAccept(devin, dex.DomainOwner, dex.CredType).Build())
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
		requireResult(t, result, "tecPATH_PARTIAL")
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
		requireResult(t, result, "tecPATH_PARTIAL")
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
		requireResult(t, result, "tecPATH_PARTIAL")
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
		result = env.Submit(cred.CredentialCreate(badDomainOwner, devin, badCredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(cred.CredentialAccept(devin, badDomainOwner, badCredType).Build())
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
		requireResult(t, result, "tecPATH_PARTIAL")
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
		expiration := rippleTimeNow(env) + 20
		result := env.Submit(
			cred.CredentialCreate(dex.DomainOwner, devin, dex.CredType).
				Expiration(expiration).Build(),
		)
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(cred.CredentialAccept(devin, dex.DomainOwner, dex.CredType).Build())
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
		requireResult(t, result, "tecPATH_PARTIAL")
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
			cred.CredentialDelete(dex.DomainOwner, dex.Bob, dex.DomainOwner, dex.CredType).Build(),
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
		requireResult(t, result, "tecPATH_PARTIAL")
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
		result = env.Submit(cred.CredentialCreate(dex.DomainOwner, devin, dex.CredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(cred.CredentialAccept(devin, dex.DomainOwner, dex.CredType).Build())
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
		cred.CredentialDelete(dex.DomainOwner, dex.Bob, dex.DomainOwner, dex.CredType).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice can still ripple through bob even though he's not in the domain
	// path: alice's EUR → bob's EUR trustline → carol
	// In rippled, paths(EURA) triggers the pathfinder which discovers Bob as the
	// intermediate hop. Since goXRPL doesn't have a pathfinder yet, we manually
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
	requireResult(t, result, "tecPATH_DRY")
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
		cred.CredentialDelete(dex.DomainOwner, dex.GW, dex.DomainOwner, dex.CredType).Build(),
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
		cred.CredentialDelete(dex.DomainOwner, dex.Alice, dex.DomainOwner, dex.CredType).Build(),
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
	requireResult(t, result, "tecPATH_PARTIAL")
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
		requireResult(t, result, "temDISABLED")
		env.Close()

		// tfHybrid without domain → temINVALID_FLAG (even without amendment)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				Hybrid().Build(),
		)
		requireResult(t, result, "temINVALID_FLAG")
		env.Close()

		// Enable PermissionedDEX
		env.EnableFeature("PermissionedDEX")
		env.Close()

		// tfHybrid without domain still fails
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				Hybrid().Build(),
		)
		requireResult(t, result, "temINVALID_FLAG")
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

		// alice creates regular (non-domain) offer - should cross bob's hybrid (in open book)
		aliceSeq := env.Seq(dex.Alice)
		result = env.Submit(
			offerBuilder.OfferCreate(dex.Alice, dex.USD(10), jtx.XRPTxAmount(10_000_000)).Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		offerBuilder.RequireNoOfferInLedger(t, env, dex.Alice, aliceSeq)
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, bobSeq)
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

		result = env.Submit(cred.CredentialCreate(badDomainOwner, devin, badCredType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(cred.CredentialAccept(devin, badDomainOwner, badCredType).Build())
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
		requireResult(t, result, "tecPATH_DRY")
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
		requireResult(t, result, "tecPATH_PARTIAL")
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
		cred.CredentialDelete(dex.DomainOwner, dex.Bob, dex.DomainOwner, dex.CredType).Build(),
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
	requireResult(t, result, "tecPATH_PARTIAL")
	env.Close()
	offerBuilder.RequireOfferInLedger(t, env, dex.Bob, hybridSeq)

	// Bob's hybrid offer can't be consumed in regular payment either
	// (in open book but invalid since bob left domain)
	result = env.Submit(
		paymentBuilder.PayIssued(dex.Alice, dex.Carol, dex.USD(5)).
			SendMax(jtx.XRPTxAmount(5_000_000)).
			Paths(usdPath(dex.GW)).Build(),
	)
	requireResult(t, result, "tecPATH_PARTIAL")
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

// TestPermissionedDEX_HybridOfferDirectories tests that hybrid offers appear in
// both domain and open book directories.
// Reference: rippled PermissionedDEX_test::testHybridOfferDirectories
func TestPermissionedDEX_HybridOfferDirectories(t *testing.T) {
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)

	var offerSeqs []uint32
	const dirCount = 100

	for i := 0; i < dirCount; i++ {
		bobSeq := env.Seq(dex.Bob)
		offerSeqs = append(offerSeqs, bobSeq)

		result := env.Submit(
			offerBuilder.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).
				DomainID(dex.DomainID).Hybrid().Build(),
		)
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireOfferInLedger(t, env, dex.Bob, bobSeq)
	}

	// Cancel all hybrid offers - they should be removed from both directories
	for _, seq := range offerSeqs {
		result := env.Submit(offerBuilder.OfferCancel(dex.Bob, seq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		offerBuilder.RequireNoOfferInLedger(t, env, dex.Bob, seq)
	}
}
