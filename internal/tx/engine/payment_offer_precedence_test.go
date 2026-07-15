package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// zeroAccountAddr is the classic address of the all-zero AccountID
// (rippled xrpAccount()/noAccount()), which is wire-valid and signable.
const zeroAccountAddr = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// mptIDA / mptIDB are two distinct 24-byte MPT issuance IDs (48 hex chars).
const (
	mptIDA = "00000000000000000000000000000000000000000000000A"
	mptIDB = "00000000000000000000000000000000000000000000000B"
)

func preflightPayment(dst string, amount txcore.Amount) *payment.Payment {
	p := payment.NewPayment(precedenceSourceAddr, dst, amount)
	p.Fee = "10"
	p.Sequence = u32(5)
	return p
}

func preflightOfferCreate(gets, pays txcore.Amount) *offer.OfferCreate {
	o := offer.NewOfferCreate(precedenceSourceAddr, gets, pays)
	o.Fee = "10"
	o.Sequence = u32(5)
	return o
}

// --- Payment ---

// P1: a non-MPT Payment's flags mask (tfPaymentMask) rejects any undefined flag
// bit at preflight0 — an XRP→XRP payment carrying flag 0x1 is temINVALID_FLAG.
func TestPaymentPrecedence_UndefinedFlagRejected(t *testing.T) {
	e := preflightEngine(allRules())
	p := preflightPayment(precedenceGenesisAddr, txcore.NewXRPAmount(1_000_000))
	p.SetFlags(0x00000001)
	if got := e.preflight(p); got != ter.TemINVALID_FLAG {
		t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
	}
}

// P2: temDST_NEEDED fires for the zero AccountID destination, not just an absent
// Destination field.
func TestPaymentPrecedence_ZeroDestinationNeeded(t *testing.T) {
	e := preflightEngine(allRules())
	p := preflightPayment(zeroAccountAddr, txcore.NewIssuedAmountFromFloat64(10, "USD", precedenceGenesisAddr))
	if got := e.preflight(p); got != ter.TemDST_NEEDED {
		t.Fatalf("preflight = %v, want TemDST_NEEDED", got)
	}
}

// P3: equalTokens compares the currency only (issuer ignored), so a self-payment
// of USD/issuerB with SendMax USD/issuerA and no paths is temREDUNDANT.
func TestPaymentPrecedence_SelfPaymentDifferentIssuerRedundant(t *testing.T) {
	e := preflightEngine(allRules())
	p := payment.NewPayment(precedenceSourceAddr, precedenceSourceAddr,
		txcore.NewIssuedAmountFromFloat64(10, "USD", precedenceGenesisAddr))
	p.Fee = "10"
	p.Sequence = u32(5)
	sendMax := txcore.NewIssuedAmountFromFloat64(11, "USD", precedenceSourceAddr)
	p.SendMax = &sendMax
	if got := e.preflight(p); got != ter.TemREDUNDANT {
		t.Fatalf("preflight = %v, want TemREDUNDANT", got)
	}
}

// P4: path-element shape validation no longer runs in preflight, so an otherwise
// well-formed IOU payment carrying a malformed (account+currency) path element
// passes preflight — the divergent code is decided later at preclaim/apply.
func TestPaymentPrecedence_BadPathElementPassesPreflight(t *testing.T) {
	e := preflightEngine(allRules())
	p := preflightPayment(precedenceGenesisAddr, txcore.NewIssuedAmountFromFloat64(10, "USD", precedenceGenesisAddr))
	p.Paths = [][]payment.PathStep{{{Account: precedenceSourceAddr, Currency: "USD"}}}
	if got := e.preflight(p); got != ter.TesSUCCESS {
		t.Fatalf("preflight = %v, want TesSUCCESS (path shape checked at apply)", got)
	}
}

// P5: DeliverMin must hold the same asset as Amount by full asset identity — an
// MPT DeliverMin of a different issuance than the MPT Amount is temBAD_AMOUNT.
func TestPaymentPrecedence_DeliverMinAssetIdentity(t *testing.T) {
	e := preflightEngine(allRules())
	amt := state.NewMPTAmountWithIssuanceID(100, precedenceGenesisAddr, mptIDA)
	p := preflightPayment(precedenceGenesisAddr, amt)
	p.SetFlags(payment.PaymentFlagPartialPayment)
	dMin := state.NewMPTAmountWithIssuanceID(1, precedenceGenesisAddr, mptIDB)
	p.DeliverMin = &dMin
	if got := e.preflight(p); got != ter.TemBAD_AMOUNT {
		t.Fatalf("preflight = %v, want TemBAD_AMOUNT", got)
	}
}

// P6: the MPT flags mask is enforced at preflight0, before the body's zero-amount
// check — a zero MPT Amount with tfLimitQuality is temINVALID_FLAG, not
// temBAD_AMOUNT.
func TestPaymentPrecedence_MPTMaskBeforeZeroAmount(t *testing.T) {
	e := preflightEngine(allRules())
	amt := state.NewMPTAmountWithIssuanceID(0, precedenceGenesisAddr, mptIDA)
	p := preflightPayment(precedenceGenesisAddr, amt)
	p.SetFlags(payment.PaymentFlagLimitQuality)
	if got := e.preflight(p); got != ter.TemINVALID_FLAG {
		t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
	}
}

func TestPaymentPrecedence_MPTokensV2MaskBeforeFee(t *testing.T) {
	amount := state.NewMPTAmountWithIssuanceID(100, precedenceGenesisAddr, mptIDA)

	t.Run("MPTokensV1 rejects limit quality before bad fee", func(t *testing.T) {
		rules := amendment.NewRulesBuilder().
			FromPreset(amendment.PresetAllSupported).
			Disable(amendment.FeatureMPTokensV2).
			Build()
		p := preflightPayment(precedenceGenesisAddr, amount)
		p.Fee = "-10"
		p.SetFlags(payment.PaymentFlagLimitQuality)
		if got := preflightEngine(rules).preflight(p); got != ter.TemINVALID_FLAG {
			t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
		}
	})

	t.Run("MPTokensV2 allows limit quality so bad fee wins", func(t *testing.T) {
		rules := amendment.NewRulesBuilder().
			FromPreset(amendment.PresetAllSupported).
			Enable(amendment.FeatureMPTokensV2).
			Build()
		p := preflightPayment(precedenceGenesisAddr, amount)
		p.Fee = "-10"
		p.SetFlags(payment.PaymentFlagLimitQuality)
		if got := preflightEngine(rules).preflight(p); got != ter.TemBAD_FEE {
			t.Fatalf("preflight = %v, want TemBAD_FEE", got)
		}
	})
}

// P7: the MPT+Paths temMALFORMED check precedes the zero-amount temBAD_AMOUNT
// check, so a zero MPT Amount with paths is temMALFORMED.
func TestPaymentPrecedence_MPTPathsBeforeZeroAmount(t *testing.T) {
	e := preflightEngine(allRules())
	amt := state.NewMPTAmountWithIssuanceID(0, precedenceGenesisAddr, mptIDA)
	p := preflightPayment(precedenceGenesisAddr, amt)
	p.Paths = [][]payment.PathStep{{{Account: precedenceSourceAddr}}}
	if got := e.preflight(p); got != ter.TemMALFORMED {
		t.Fatalf("preflight = %v, want TemMALFORMED", got)
	}
}

// P9: the MPT amendment gate lives after the mask. With MPTokensV1 disabled, an
// MPT payment carrying a mask-invalid flag surfaces temINVALID_FLAG (mask first);
// a well-flagged MPT payment surfaces temDISABLED (the gate).
func TestPaymentPrecedence_MPTGateAfterMask(t *testing.T) {
	e := preflightEngine(rulesWithout("MPTokensV1"))

	t.Run("mask beats disabled gate", func(t *testing.T) {
		amt := state.NewMPTAmountWithIssuanceID(100, precedenceGenesisAddr, mptIDA)
		p := preflightPayment(precedenceGenesisAddr, amt)
		p.SetFlags(payment.PaymentFlagLimitQuality)
		if got := e.preflight(p); got != ter.TemINVALID_FLAG {
			t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
		}
	})

	t.Run("valid flags reach disabled gate", func(t *testing.T) {
		amt := state.NewMPTAmountWithIssuanceID(100, precedenceGenesisAddr, mptIDA)
		p := preflightPayment(precedenceGenesisAddr, amt)
		if got := e.preflight(p); got != ter.TemDISABLED {
			t.Fatalf("preflight = %v, want TemDISABLED", got)
		}
	})
}

// --- OfferCreate ---

// O1: isLegalNet passes zero amounts, so a zero TakerPays falls through to the
// non-positive-amount check and is temBAD_OFFER, not temBAD_AMOUNT.
func TestOfferPrecedence_ZeroAmountBadOffer(t *testing.T) {
	e := preflightEngine(allRules())
	o := preflightOfferCreate(txcore.NewIssuedAmountFromFloat64(100, "USD", precedenceGenesisAddr), txcore.NewXRPAmount(0))
	if got := e.preflight(o); got != ter.TemBAD_OFFER {
		t.Fatalf("preflight = %v, want TemBAD_OFFER", got)
	}
}

// O3: the sfDomainID-without-PermissionedDEX temDISABLED gate runs in
// checkExtraFeatures, before the preflight body — it beats a body temBAD_EXPIRATION.
func TestOfferPrecedence_DomainDisabledBeatsBody(t *testing.T) {
	e := preflightEngine(rulesWithout("PermissionedDEX"))
	o := preflightOfferCreate(txcore.NewIssuedAmountFromFloat64(100, "USD", precedenceGenesisAddr), txcore.NewXRPAmount(1_000_000))
	dom := [32]byte{1}
	o.DomainID = &dom
	exp := uint32(0)
	o.Expiration = &exp // temBAD_EXPIRATION if the body were reached
	if got := e.preflight(o); got != ter.TemDISABLED {
		t.Fatalf("preflight = %v, want TemDISABLED", got)
	}
}

// O4: the flags mask is amendment-conditional — with PermissionedDEX disabled it
// includes tfHybrid, rejected at preflight0 ahead of the fee check.
func TestOfferPrecedence_ConditionalMaskBeatsFee(t *testing.T) {
	e := preflightEngine(rulesWithout("PermissionedDEX"))
	o := preflightOfferCreate(txcore.NewIssuedAmountFromFloat64(100, "USD", precedenceGenesisAddr), txcore.NewXRPAmount(1_000_000))
	o.Fee = "-10"          // malformed fee, not reached
	o.SetFlags(0x00100000) // tfHybrid, no DomainID
	if got := e.preflight(o); got != ter.TemINVALID_FLAG {
		t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
	}
}

// --- OfferCancel ---

// OfferCancel adopts the universal flags mask at preflight0.
func TestOfferCancelPrecedence_FlagMask(t *testing.T) {
	e := preflightEngine(allRules())
	o := offer.NewOfferCancel(precedenceSourceAddr, 12345)
	o.Fee = "10"
	o.Sequence = u32(5)
	o.SetFlags(0x00000001)
	if got := e.preflight(o); got != ter.TemINVALID_FLAG {
		t.Fatalf("preflight = %v, want TemINVALID_FLAG", got)
	}
}
