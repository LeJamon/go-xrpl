package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_GetLedger_HashSeqlessGatesOnLtype pins rippled's
// PeerImp::getLedger fallback: a GetLedger carrying neither a ledger_hash nor
// a ledger_seq serves the closed ledger only when ltype == ltCLOSED. Any other
// (or absent) ltype finds no ledger and serves nothing, rather than
// unconditionally returning the closed ledger.
func TestRouter_GetLedger_HashSeqlessGatesOnLtype(t *testing.T) {
	send := func(t *testing.T, lt message.LedgerType) (*Router, []sentFrame) {
		t.Helper()
		r, rs := makeRouterWithQueryTypeRecorder(t)
		req := &message.GetLedger{
			InfoType: message.LedgerInfoBase,
			LType:    lt,
		}
		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  11,
			Type:    uint16(message.TypeGetLedger),
			Payload: encodePayload(t, req),
		})
		bd, sent := rs.snapshot()
		require.Empty(t, bd, "a hash/seq-less liBASE request is well-formed; no bad-data charge")
		return r, sent
	}

	t.Run("absent ltype serves nothing", func(t *testing.T) {
		_, sent := send(t, message.LedgerTypeAccepted)
		assert.Empty(t, sent, "hash/seq-less request without ltCLOSED must not serve the closed ledger")
	})

	t.Run("ltCURRENT serves nothing", func(t *testing.T) {
		_, sent := send(t, message.LedgerTypeCurrent)
		assert.Empty(t, sent, "hash/seq-less ltCURRENT must not serve the closed ledger")
	})

	t.Run("ltCLOSED serves the closed ledger", func(t *testing.T) {
		r, sent := send(t, message.LedgerTypeClosed)
		require.Len(t, sent, 1, "ltCLOSED must serve the closed ledger")
		assert.Equal(t, uint64(11), sent[0].peerID)

		l := r.adaptor.LedgerService().GetClosedLedger()
		require.NotNil(t, l)
		hash := l.Hash()

		_, decoded := decodeFrame(t, sent[0].frame)
		ld, ok := decoded.(*message.LedgerData)
		require.True(t, ok, "ltCLOSED serve must be a TMLedgerData")
		assert.Equal(t, message.LedgerInfoBase, ld.InfoType)
		assert.Equal(t, l.Sequence(), ld.LedgerSeq, "served seq must be the closed ledger's")
		assert.Equal(t, hash[:], ld.LedgerHash, "served hash must be the closed ledger's")
	})
}
