package nftoken

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// secondNFTokenID is a second well-formed 256-bit NFTokenID, distinct from
// validNFTokenID (defined in nftoken_validate_test.go).
const secondNFTokenID = "000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000002"

// wantTER asserts that err carries the given TER code.
func wantTER(t *testing.T, err error, want ter.Result) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %v, got nil", want)
	}
	var re *ter.ResultError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ter.ResultError %v, got %T: %v", want, err, err)
	}
	if re.Code != want {
		t.Fatalf("got %v, want %v", re.Code, want)
	}
}

// mainnetRules has both fixRemoveNFTokenAutoTrustLine and DynamicNFT enabled, as
// on mainnet.
func mainnetRules() *amendment.Rules { return amendment.AllSupportedRules() }

// preV2NFTRules enables NonFungibleTokensV1 only, so fixNFTokenNegOffer,
// fixRemoveNFTokenAutoTrustLine and DynamicNFT all read as disabled — the
// original NFToken behaviour.
func preV2NFTRules() *amendment.Rules {
	return amendment.NewRulesBuilder().EnableByName("NonFungibleTokensV1").Build()
}

// --- Finding 1: NFTokenCancelOffer mask value (~tfUniversal, not 0xFFFFFFFF) ---

func TestNFTokenCancelOffer_FlagsMask(t *testing.T) {
	c := NewNFTokenCancelOffer("rAlice", []string{validNFTokenID})
	mask := c.GetFlagsMask(mainnetRules())

	// Universal bits (tfFullyCanonicalSig, tfInnerBatchTxn) must be permitted.
	if mask&tx.TfFullyCanonicalSig != 0 {
		t.Errorf("tfFullyCanonicalSig must not be in the invalid-flags mask")
	}
	if mask&tx.TfInnerBatchTxn != 0 {
		t.Errorf("tfInnerBatchTxn must not be in the invalid-flags mask")
	}
	// Any other bit is invalid.
	if mask&0x00000001 == 0 {
		t.Errorf("a non-universal flag must be in the invalid-flags mask")
	}
}

// --- Finding 2: NFTokenAcceptOffer rejects zero AND negative broker fees ---

func TestNFTokenAcceptOffer_BrokerFeeSign(t *testing.T) {
	build := func(fee tx.Amount) *NFTokenAcceptOffer {
		n := NewNFTokenAcceptOffer("rBroker")
		n.NFTokenBuyOffer = validNFTokenID
		n.NFTokenSellOffer = secondNFTokenID
		n.NFTokenBrokerFee = &fee
		return n
	}

	t.Run("negative IOU broker fee", func(t *testing.T) {
		fee := state.NewIssuedAmountFromFloat64(-10, "USD", "rIssuer")
		wantTER(t, build(fee).Validate(), ter.TemMALFORMED)
	})
	t.Run("negative XRP broker fee", func(t *testing.T) {
		wantTER(t, build(tx.NewXRPAmount(-10)).Validate(), ter.TemMALFORMED)
	})
	t.Run("zero broker fee", func(t *testing.T) {
		wantTER(t, build(tx.NewXRPAmount(0)).Validate(), ter.TemMALFORMED)
	})
	t.Run("positive broker fee accepted", func(t *testing.T) {
		if err := build(tx.NewXRPAmount(10)).Validate(); err != nil {
			t.Fatalf("positive broker fee should pass Validate, got %v", err)
		}
	})
}

// --- Findings 4 & 5: NFTokenMint amendment-conditional flag mask ---

func TestNFTokenMint_FlagsMask(t *testing.T) {
	m := NewNFTokenMint("rAlice", 0)

	t.Run("mainnet rejects tfTrustLine, allows tfMutable", func(t *testing.T) {
		mask := m.GetFlagsMask(mainnetRules())
		if mask&NFTokenMintFlagTrustLine == 0 {
			t.Errorf("mainnet mask must reject tfTrustLine")
		}
		if mask&NFTokenMintFlagMutable != 0 {
			t.Errorf("mainnet mask (DynamicNFT on) must allow tfMutable")
		}
	})

	t.Run("pre-fix, no DynamicNFT: rejects tfMutable, allows tfTrustLine", func(t *testing.T) {
		rules := amendment.NewRulesBuilder().
			FromPreset(amendment.PresetAllSupported).
			DisableByName("fixRemoveNFTokenAutoTrustLine").
			DisableByName("DynamicNFT").
			Build()
		mask := m.GetFlagsMask(rules)
		if mask&NFTokenMintFlagMutable == 0 {
			t.Errorf("without DynamicNFT the mask must reject tfMutable")
		}
		if mask&NFTokenMintFlagTrustLine != 0 {
			t.Errorf("without fixRemoveNFTokenAutoTrustLine the mask must allow tfTrustLine")
		}
	})
}

// --- Finding 3: NFTokenMint negative-amount temBAD_AMOUNT runs in preflight ---

func TestNFTokenMint_PreflightRules_NegativeAmount(t *testing.T) {
	m := NewNFTokenMint("rAlice", 0)
	m.Issuer = "rNonExistentIssuer"
	amt := state.NewIssuedAmountFromFloat64(-5, "USD", "rIssuer")
	m.Amount = &amt

	// The negative-amount check must win over any later offer-field check, so a
	// negative amount together with an invalid Expiration still returns
	// temBAD_AMOUNT, not temBAD_EXPIRATION.
	zeroExp := uint32(0)
	m.Expiration = &zeroExp

	wantTER(t, m.PreflightRules(mainnetRules()), ter.TemBAD_AMOUNT)
}

// --- Finding 6: NFTokenCreateOffer negative-amount temBAD_AMOUNT in preflight ---

func TestNFTokenCreateOffer_PreflightRules_NegativeAmountFirst(t *testing.T) {
	// Sell offer with a negative amount and a zero Expiration: the negative-amount
	// check precedes the expiration check, matching rippled's order.
	o := NewNFTokenCreateOffer("rAlice", validNFTokenID, tx.NewXRPAmount(-10))
	o.SetSellOffer()
	zeroExp := uint32(0)
	o.Expiration = &zeroExp

	wantTER(t, o.PreflightRules(mainnetRules()), ter.TemBAD_AMOUNT)

	// Validate must NOT reject on the amount/expiration (those are PreflightRules
	// checks now); it only enforces structural presence.
	if err := o.Validate(); err != nil {
		t.Fatalf("Validate should not reject amount/expiration, got %v", err)
	}
}

// --- Finding 7: destination on a buy offer is malformed pre-fixNFTokenNegOffer ---

func TestNFTokenCreateOffer_PreflightRules_DestinationOnBuy(t *testing.T) {
	// Buy offer (no tfSellNFToken), Owner required, Destination set.
	o := NewNFTokenCreateOffer("rAlice", validNFTokenID, tx.NewXRPAmount(10))
	o.Owner = "rOwner"
	o.Destination = "rBroker"

	t.Run("rejected before fixNFTokenNegOffer", func(t *testing.T) {
		wantTER(t, o.PreflightRules(preV2NFTRules()), ter.TemMALFORMED)
	})
	t.Run("allowed with fixNFTokenNegOffer", func(t *testing.T) {
		if err := o.PreflightRules(mainnetRules()); err != nil {
			t.Fatalf("destination on buy offer should be allowed post-fix, got %v", err)
		}
	})
}
