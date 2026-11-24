package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/stretchr/testify/require"
)

func TestRouterHandlesForwardedMessageTypes(t *testing.T) {
	for code := range proto.MessageType_name {
		msgType := message.MessageType(code)
		switch msgType {
		// The overlay consumes these frames. Batched transactions are
		// forwarded individually as TypeTransaction.
		case message.TypePing, message.TypeCluster, message.TypeEndpoints, message.TypeSquelch,
			message.TypeProofPathReq, message.TypeReplayDeltaReq, message.TypeHaveTransactions, message.TypeTransactions:
			continue
		}
		require.Less(t, int(msgType), len(inboundHandlers), "%s has no router handler", msgType)
		require.NotNil(t, inboundHandlers[msgType], "%s has no router handler", msgType)
	}
}
