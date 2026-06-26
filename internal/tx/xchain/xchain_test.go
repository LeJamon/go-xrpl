package xchain

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestXChainAmendmentRemainsUnsupported guards the stubbed Apply implementations.
// Every XChain Apply method returns a hard error and mutates no state because
// the real cross-chain semantics are not implemented. XChainBridge MUST stay
// SupportedNo so the engine rejects these transactions at preflight
// (temDISABLED) and Apply is never reached. Do not flip this to SupportedYes
// until the Apply methods are fully implemented.
func TestXChainAmendmentRemainsUnsupported(t *testing.T) {
	f := amendment.GetFeature(amendment.FeatureXChainBridge)
	require.NotNil(t, f, "XChainBridge must be registered")
	assert.Equal(t, amendment.SupportedNo, f.Supported,
		"XChainBridge must stay SupportedNo while xchain Apply is stubbed")
}
