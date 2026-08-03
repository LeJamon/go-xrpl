package adaptor

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// TestRouter_GetLedger_HashSeqlessGatesOnLtype pins rippled's
// onMessage(TMGetLedger) + getLedger fallback. A GetLedger carrying neither a
// ledger_hash nor a ledger_seq serves the closed ledger only when ltype ==
// ltCLOSED; any other (or absent) ltype is a malformed "Invalid request" that
// charges the peer and serves nothing. An ltype outside [ltACCEPTED, ltCLOSED]
// is treated as absent by the proto2 parser.
func TestRouter_GetLedger_HashSeqlessGatesOnLtype(t *testing.T) {
	send := func(t *testing.T, req *message.GetLedger) (*Router, []badDataCall, []sentFrame) {
		t.Helper()
		r, rs := makeRouterWithQueryTypeRecorder(t)
		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  11,
			Type:    message.TypeGetLedger,
			Payload: encodePayload(t, req),
		})
		bd, sent := rs.snapshot()
		return r, bd, sent
	}

	t.Run("absent ltype is invalid: charges bad data, serves nothing", func(t *testing.T) {
		_, bd, sent := send(t, &message.GetLedger{
			InfoType: message.LedgerInfoBase,
			LType:    message.LedgerTypeAccepted,
		})
		require.Len(t, bd, 1, "a hash/seq-less request without ltCLOSED is malformed")
		assert.Equal(t, uint64(11), bd[0].peerID)
		assert.Equal(t, "get-ledger-invalid-request", bd[0].reason)
		assert.Empty(t, sent, "malformed request must not serve the closed ledger")
	})

	t.Run("ltCURRENT is invalid: charges bad data, serves nothing", func(t *testing.T) {
		_, bd, sent := send(t, &message.GetLedger{
			InfoType: message.LedgerInfoBase,
			LType:    message.LedgerTypeCurrent,
		})
		require.Len(t, bd, 1)
		assert.Equal(t, "get-ledger-invalid-request", bd[0].reason)
		assert.Empty(t, sent, "hash/seq-less ltCURRENT must not serve the closed ledger")
	})

	t.Run("unknown ltype normalizes to absent", func(t *testing.T) {
		r, rs := makeRouterWithQueryTypeRecorder(t)
		payload := encodePayload(t, &message.GetLedger{InfoType: message.LedgerInfoBase})
		payload = protowire.AppendTag(payload, 2, protowire.VarintType)
		payload = protowire.AppendVarint(payload, 7)

		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  11,
			Type:    message.TypeGetLedger,
			Payload: payload,
		})
		bd, sent := rs.snapshot()
		require.Len(t, bd, 1, "an unknown optional enum is absent after proto2 parsing")
		assert.Equal(t, "get-ledger-invalid-request", bd[0].reason)
		assert.Empty(t, sent, "an absent ltype must not serve anything")
	})

	t.Run("absent ltype with hash skips ltype validation", func(t *testing.T) {
		_, bd, sent := send(t, &message.GetLedger{
			InfoType:   message.LedgerInfoBase,
			LedgerHash: bytes.Repeat([]byte{0xAB}, 32),
		})
		assert.Empty(t, bd)
		assert.Empty(t, sent)
	})

	t.Run("explicit ltACCEPTED with hash is valid", func(t *testing.T) {
		_, bd, sent := send(t, &message.GetLedger{
			InfoType:   message.LedgerInfoBase,
			LTypeSet:   true,
			LedgerHash: bytes.Repeat([]byte{0xAB}, 32),
		})
		assert.Empty(t, bd)
		assert.Empty(t, sent)
	})

	t.Run("ltCLOSED serves the closed ledger", func(t *testing.T) {
		r, bd, sent := send(t, &message.GetLedger{
			InfoType: message.LedgerInfoBase,
			LType:    message.LedgerTypeClosed,
		})
		require.Empty(t, bd, "a hash/seq-less ltCLOSED request is well-formed")
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

	t.Run("explicit empty hash is invalid and does not serve the closed ledger", func(t *testing.T) {
		_, bd, sent := send(t, &message.GetLedger{
			InfoType:   message.LedgerInfoBase,
			LType:      message.LedgerTypeClosed,
			LedgerHash: []byte{},
		})
		require.Equal(t, []badDataCall{{peerID: 11, reason: "get-ledger-invalid-hash"}}, bd)
		assert.Empty(t, sent)
	})
}
