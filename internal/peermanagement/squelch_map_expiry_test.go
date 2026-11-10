package peermanagement

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Expired squelches for keys that are no longer relayed (a rotated validator
// signing key, a validator that went away) must not fill the per-peer table:
// rippled keeps no cap, so a well-behaved peer's new squelch is honoured and
// not charged.
func TestPeerAddSquelch_ExpiredEntriesDoNotFillTable(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(PeerID(9), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))

	peer.squelchMu.Lock()
	for i := range maxSquelchesPerPeer {
		peer.squelchMap[fmt.Sprintf("old-key-%03d", i)] = time.Now().Add(-time.Second)
	}
	peer.squelchMu.Unlock()

	validator := make([]byte, 33)
	validator[0] = 0x02
	require.True(t, peer.AddSquelch(validator, MinUnsquelchExpire))
	require.False(t, peer.ExpireSquelch(validator))
	require.Zero(t, peer.BadDataCount())
}
