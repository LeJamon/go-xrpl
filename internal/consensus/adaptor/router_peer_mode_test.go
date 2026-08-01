package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
)

type peerAvailabilitySessions struct {
	connected map[peermanagement.PeerID]bool
}

func (s *peerAvailabilitySessions) IsPeerConnected(id peermanagement.PeerID) bool {
	return s.connected[id]
}

func (s *peerAvailabilitySessions) PeerCount() int {
	count := 0
	for _, connected := range s.connected {
		if connected {
			count++
		}
	}
	return count
}

func TestRouterPeerAvailabilityReconcilesOperatingMode(t *testing.T) {
	a := New(Config{})
	r := newTestRouter(nil, a, nil)
	sessions := &peerAvailabilitySessions{connected: make(map[peermanagement.PeerID]bool)}
	r.setPeerSessionView(sessions)

	sessions.connected[1] = true
	r.handlePeerConnect(1)
	require.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeFull)
	sessions.connected[2] = true
	r.handlePeerConnect(2)
	require.Equal(t, consensus.OpModeFull, a.GetOperatingMode(), "additional peers must not demote a higher mode")

	delete(sessions.connected, 1)
	r.HandlePeerDisconnect(1)
	require.Equal(t, consensus.OpModeFull, a.GetOperatingMode(), "remaining peers must retain the higher mode")

	delete(sessions.connected, 2)
	r.HandlePeerDisconnect(2)
	require.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())
}

func TestRouterPeerAvailabilityMaintenanceRepairsMissedLifecycleEvent(t *testing.T) {
	a := New(Config{})
	r := newTestRouter(nil, a, nil)
	sessions := &peerAvailabilitySessions{connected: map[peermanagement.PeerID]bool{1: true}}
	r.setPeerSessionView(sessions)

	r.maintenanceTick()
	require.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeFull)
	delete(sessions.connected, 1)
	r.maintenanceTick()
	require.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())
}
