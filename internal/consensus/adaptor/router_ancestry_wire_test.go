package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestHeaderDiscoveryWireHeaderForms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		suffix   int
		prefixed bool
		truncate bool
		accepted bool
	}{
		{name: "raw", accepted: true},
		{name: "one trailing byte", suffix: 1, accepted: true},
		{name: "four trailing bytes", suffix: 4, accepted: true},
		{name: "ignored trailing hash", suffix: 32, accepted: true},
		{name: "thirty-six trailing bytes", suffix: 36, accepted: true},
		{name: "long suffix", suffix: 64, accepted: true},
		{name: "ledger-master prefix", prefixed: true},
		{name: "prefix and trailing hash", prefixed: true, suffix: 32},
		{name: "truncated", truncate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender := makeRouterWithBadDataRecorder(t)
			base := r.adaptor.LedgerService().GetClosedLedger()
			link := buildAlternativeReplaySuccessor(t, base, time.Second)
			startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)
			data := header.AddRaw(link.ledger.Header(), false)
			data = append(data, make([]byte, tc.suffix)...)
			if tc.prefixed {
				data = append(protocol.HashPrefixLedgerMaster().Bytes(), data...)
			}
			if tc.truncate {
				data = data[:header.SizeBase-1]
			}
			r.handleMessage(&peermanagement.InboundMessage{
				PeerID: 7, Type: message.TypeLedgerData,
				Payload: encodePayload(t, &message.LedgerData{
					LedgerHash: link.hash[:], LedgerSeq: link.seq,
					InfoType: message.LedgerInfoBase,
					Nodes:    []message.LedgerNode{{NodeData: data}},
				}),
			})
			entry, published := r.lookupSeqHash(link.seq)
			if tc.accepted {
				require.True(t, published)
				require.True(t, entry.haveParent)
				require.Equal(t, link.hash, entry.hash)
				require.Equal(t, base.Hash(), entry.parentHash)
				require.Empty(t, sender.getBadDataCalls())
			} else {
				require.False(t, published)
				bad := sender.getBadDataCalls()
				require.Len(t, bad, 1)
				require.Equal(t, "ledger-header-ancestry", bad[0].reason)
			}
			require.Equal(t, base.Hash(), r.adaptor.LedgerService().GetValidatedLedger().Hash())
		})
	}
}
