package adaptor

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouter_LedgerDataDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		replyError message.ReplyError
		nodes      []message.LedgerNode
		wantMsg    string
	}{
		{
			name:       "peer reply error",
			replyError: message.ReplyErrorNoLedger,
			nodes:      []message.LedgerNode{{NodeData: []byte{1}}},
			wantMsg:    "inbound ledger: peer returned reply error",
		},
		{
			name:    "empty reply",
			wantMsg: "invalid ledger_data node count",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _, svc := makeRouter(t)
			var output bytes.Buffer
			r.logger = slog.New(slog.NewJSONHandler(&output, nil))

			seq := svc.GetClosedLedgerIndex() + 1
			hash := [32]byte{0xA4}
			payload := encodePayload(t, &message.LedgerData{
				LedgerHash: hash[:],
				LedgerSeq:  seq,
				InfoType:   message.LedgerInfoBase,
				Nodes:      tt.nodes,
				Error:      tt.replyError,
			})
			r.handleMessage(&peermanagement.InboundMessage{
				PeerID:  17,
				Type:    uint16(message.TypeLedgerData),
				Payload: payload,
			})

			var record map[string]any
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
			assert.Equal(t, tt.wantMsg, record["msg"])
			assert.Equal(t, float64(17), record["peer"])
			assert.Equal(t, float64(seq), record["seq"])
			assert.Equal(t, float64(message.LedgerInfoBase), record["info_type"])
			assert.Equal(t, float64(tt.replyError), record["reply_error"])
			assert.Equal(t, float64(len(tt.nodes)), record["nodes"])
		})
	}
}
