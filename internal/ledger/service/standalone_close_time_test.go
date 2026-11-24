package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

// The ledger closed at startup has its close time rounded to the resolution.
// Each standalone close rounds the close time the same way and keeps it at
// least one second past its parent's, so ledgers accepted within the same
// second still get increasing close times.
func TestStandaloneAcceptCloseTimeIsEffective(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.GetClosedLedger()
	require.Zero(t, protocol.ToRippleTime(parent.CloseTime())%parent.CloseTimeResolution())
	for range 4 {
		seq, err := svc.AcceptLedger(t.Context())
		require.NoError(t, err)
		closed, err := svc.GetLedgerBySequence(seq)
		require.NoError(t, err)

		closeTime := protocol.ToRippleTime(closed.CloseTime())
		parentCloseTime := protocol.ToRippleTime(parent.CloseTime())
		require.GreaterOrEqual(t, closeTime, parentCloseTime+1, "ledger %d", seq)
		if closeTime != parentCloseTime+1 {
			require.Zero(t, closeTime%closed.CloseTimeResolution(), "ledger %d", seq)
		}
		parent = closed
	}
}
