package adaptor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

// TestSetOperatingMode_AmendmentBlockedCapsAtConnected pins the mode cap: an
// amendment-blocked node must never report a mode above Connected, no matter
// what transition the sync machinery requests.
func TestSetOperatingMode_AmendmentBlockedCapsAtConnected(t *testing.T) {
	tbl := amendment.NewTable()
	svc, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		Table:         tbl,
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

func TestSetOperatingMode_SupportedFixBatchV12RemainsUnblocked(t *testing.T) {
	feature := amendment.FeatureByName("fixBatchV1_2")
	require.NotNil(t, feature)

	tbl := amendment.NewTable()
	svc, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		Table:         tbl,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	a := New(Config{LedgerService: svc})
	tbl.DoValidatedLedger(256, map[[32]byte]bool{feature.ID: true}, nil)

	assert.False(t, a.IsAmendmentBlocked(), "supported fixBatchV1_2 activation must not block the node")
	a.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
}

func TestSetOperatingMode_ConfidentialTransferCapability(t *testing.T) {
	feature := amendment.FeatureByName("ConfidentialTransfer")
	require.NotNil(t, feature)
	for _, enabled := range []bool{false, true} {
		name := "majority"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			tbl := amendment.NewTable()
			svc, err := service.New(service.Config{
				Standalone:    true,
				GenesisConfig: genesis.DefaultConfig(),
				Table:         tbl,
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			a := New(Config{LedgerService: svc})
			if enabled {
				tbl.DoValidatedLedger(256, map[[32]byte]bool{feature.ID: true}, nil)
			} else {
				tbl.DoValidatedLedger(256, nil, map[[32]byte]uint32{feature.ID: 1_000_000})
			}
			wantBlocked := enabled && !mptcrypto.Available()
			require.Equal(t, wantBlocked, a.IsAmendmentBlocked())
			a.SetOperatingMode(consensus.OpModeFull)
			wantMode := consensus.OpModeFull
			if wantBlocked {
				wantMode = consensus.OpModeConnected
			}
			assert.Equal(t, wantMode, a.GetOperatingMode())
		})
	}
}
