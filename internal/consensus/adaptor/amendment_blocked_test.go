package adaptor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

// TestSetOperatingMode_AmendmentBlockedCapsAtConnected pins the mode cap: an
// amendment-blocked node must never report a mode above Connected, no matter
// what transition the sync machinery requests.
func TestSetOperatingMode_AmendmentBlockedCapsAtConnected(t *testing.T) {
	tbl := amendment.NewAmendmentTable()
	svc, err := service.New(service.Config{
		Standalone:     true,
		GenesisConfig:  genesis.DefaultConfig(),
		AmendmentTable: tbl,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	a := New(Config{LedgerService: svc})

	a.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	assert.False(t, a.IsAmendmentBlocked())

	// Activate an amendment this build does not support, then fold a
	// validated ledger so the table latches blocked.
	tbl.Enable([32]byte{0xde, 0xad, 0xbe, 0xef})
	tbl.DoValidatedLedger(256, nil, nil)
	require.True(t, a.IsAmendmentBlocked())

	a.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeTracking)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	// Transitions at or below Connected still pass through.
	a.SetOperatingMode(consensus.OpModeDisconnected)
	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())
}
