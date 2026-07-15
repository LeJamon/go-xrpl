package nftoken

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

// validNFTokenID is a well-formed 256-bit NFTokenID hex string.
const validNFTokenID = "000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001"

// TestNFTokenBurnFlagValidation verifies NFTokenBurn's FlagsMasker adoption: the
// invalid-flags mask (enforced by the engine at preflight0) rejects a
// non-universal flag while permitting the universal flags (tfFullyCanonicalSig).
func TestNFTokenBurnFlagValidation(t *testing.T) {
	mask := NewNFTokenBurn("rAlice", validNFTokenID).GetFlagsMask(mainnetRules())
	t.Run("non-universal flag in mask", func(t *testing.T) {
		if mask&0x00000001 == 0 {
			t.Fatal("a non-universal flag must be in the NFTokenBurn invalid-flags mask")
		}
	})
	t.Run("universal flag not in mask", func(t *testing.T) {
		if mask&tx.TfFullyCanonicalSig != 0 {
			t.Error("tfFullyCanonicalSig must not be in the invalid-flags mask")
		}
	})
}

// TestNFTokenModifyFlagValidation pins NFTokenModify's FlagsMasker adoption: the
// invalid-flags mask (enforced by the engine at preflight0) rejects a
// non-universal flag while permitting the universal flags.
func TestNFTokenModifyFlagValidation(t *testing.T) {
	mask := NewNFTokenModify("rAlice", validNFTokenID).GetFlagsMask(mainnetRules())
	if mask&0x00000001 == 0 {
		t.Fatal("a non-universal flag must be in the NFTokenModify invalid-flags mask")
	}
	if mask&tx.TfFullyCanonicalSig != 0 {
		t.Error("tfFullyCanonicalSig must not be in the invalid-flags mask")
	}
}

// TestNFTokenCancelOfferIDValidation verifies that malformed and duplicate offer
// IDs are rejected at validation time rather than silently skipped at apply time.
func TestNFTokenCancelOfferIDValidation(t *testing.T) {
	t.Run("valid offer ID accepted", func(t *testing.T) {
		c := NewNFTokenCancelOffer("rAlice", []string{validNFTokenID})
		if err := c.Validate(); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
	t.Run("non-hex offer ID rejected", func(t *testing.T) {
		c := NewNFTokenCancelOffer("rAlice", []string{"not-a-valid-hex-id-zz"})
		if err := c.Validate(); err == nil {
			t.Error("expected temMALFORMED for non-hex offer ID")
		}
	})
	t.Run("wrong-length offer ID rejected", func(t *testing.T) {
		c := NewNFTokenCancelOffer("rAlice", []string{"00FF"})
		if err := c.Validate(); err == nil {
			t.Error("expected temMALFORMED for short offer ID")
		}
	})
	t.Run("duplicate offer ID rejected", func(t *testing.T) {
		c := NewNFTokenCancelOffer("rAlice", []string{validNFTokenID, validNFTokenID})
		if err := c.Validate(); err == nil {
			t.Error("expected temMALFORMED for duplicate offer ID")
		}
	})
}
