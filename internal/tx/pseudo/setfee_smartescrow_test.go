package pseudo

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func feeU32(v uint32) *uint32 { return &v }

// legacySetFee builds a well-formed legacy (pre-XRPFees) SetFee so PreclaimPseudo
// reaches the SmartEscrow extension-fee gate.
func legacySetFee() *SetFee {
	s := NewSetFee()
	s.BaseFee = "a" // 10 drops, hex
	s.ReferenceFeeUnits = feeU32(10)
	s.ReserveBase = feeU32(200000)
	s.ReserveIncrement = feeU32(50000)
	return s
}

// TestSetFeePreclaim_SmartEscrowExtensionFees pins the SmartEscrow extension-fee
// field-set rule: the triple is required together once the amendment is active,
// and forbidden otherwise. Reference: rippled Change.cpp preclaim:126-139.
func TestSetFeePreclaim_SmartEscrowExtensionFees(t *testing.T) {
	withSE := amendment.NewRulesBuilder().Enable(amendment.FeatureSmartEscrow).Build()
	withoutSE := amendment.NewRulesBuilder().Build()

	// SmartEscrow active + full extension triple → success.
	full := legacySetFee()
	full.ExtensionComputeLimit = feeU32(1_000_000)
	full.ExtensionSizeLimit = feeU32(100_000)
	full.GasPrice = feeU32(1_000_000)
	if got := full.PreclaimPseudo(withSE); got != tx.TesSUCCESS {
		t.Errorf("full triple under SmartEscrow: got %v, want tesSUCCESS", got)
	}

	// SmartEscrow active + a missing field → temMALFORMED.
	missing := legacySetFee()
	missing.ExtensionComputeLimit = feeU32(1_000_000)
	missing.ExtensionSizeLimit = feeU32(100_000) // GasPrice omitted
	if got := missing.PreclaimPseudo(withSE); got != tx.TemMALFORMED {
		t.Errorf("missing GasPrice under SmartEscrow: got %v, want temMALFORMED", got)
	}

	// SmartEscrow inactive + an extension field present → temDISABLED.
	present := legacySetFee()
	present.GasPrice = feeU32(1_000_000)
	if got := present.PreclaimPseudo(withoutSE); got != tx.TemDISABLED {
		t.Errorf("extension field without SmartEscrow: got %v, want temDISABLED", got)
	}

	// SmartEscrow inactive + no extension fields → success (plain legacy SetFee).
	plain := legacySetFee()
	if got := plain.PreclaimPseudo(withoutSE); got != tx.TesSUCCESS {
		t.Errorf("plain legacy SetFee without SmartEscrow: got %v, want tesSUCCESS", got)
	}
}
