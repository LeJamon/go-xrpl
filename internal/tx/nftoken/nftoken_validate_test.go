package nftoken

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

// validNFTokenID is a well-formed 256-bit NFTokenID hex string.
const validNFTokenID = "000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001"

// TestNFTokenBurnFlagValidation verifies NFTokenBurn validates flags against the
// universal mask: arbitrary low flags are rejected while universal flags (e.g.
// tfFullyCanonicalSig) are accepted. The previous invented tfBurnNFToken mask
// did the opposite in both directions.
func TestNFTokenBurnFlagValidation(t *testing.T) {
	t.Run("non-universal flag rejected", func(t *testing.T) {
		b := NewNFTokenBurn("rAlice", validNFTokenID)
		b.SetFlags(0x00000001)
		if err := b.Validate(); err == nil {
			t.Fatal("expected temINVALID_FLAG, got nil")
		}
	})
	t.Run("universal flag accepted", func(t *testing.T) {
		b := NewNFTokenBurn("rAlice", validNFTokenID)
		b.SetFlags(tx.TfFullyCanonicalSig)
		if err := b.Validate(); err != nil {
			t.Errorf("universal flag should be accepted, got %v", err)
		}
	})
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
