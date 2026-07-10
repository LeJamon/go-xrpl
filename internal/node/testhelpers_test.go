package node

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// feeSettingsHex builds a serialized FeeSettings ledger entry for tests that
// exercise the metadata/JSON decode helpers.
func feeSettingsHex(t *testing.T) string {
	t.Helper()
	blob, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          10,
		ReserveBaseDrops:      10_000_000,
		ReserveIncrementDrops: 2_000_000,
	})
	if err != nil {
		t.Fatalf("serializing fee settings: %v", err)
	}
	return hex.EncodeToString(blob)
}
