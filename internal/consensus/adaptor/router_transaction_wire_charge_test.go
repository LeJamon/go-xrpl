package adaptor

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestRouterTransactionWireCharges(t *testing.T) {
	for _, reduceRelay := range []bool{false, true} {
		name := "direct without reduce relay"
		if reduceRelay {
			name = "mixed transaction list"
		}
		t.Run(name, func(t *testing.T) {
			journal := &chargeLogHandler{}
			previous := slog.Default()
			slog.SetDefault(slog.New(journal))
			t.Cleanup(func() { slog.SetDefault(previous) })
			enableRelay := func(c *peermanagement.Config) { c.EnableTxReduceRelay = reduceRelay }
			connections := make(chan manifestOverlayConnection, 1)
			source := startRunningManifestOverlay(t, peermanagement.WithCompression(false), enableRelay)
			source.overlay.SetPeerConnectCallback(func(peerID peermanagement.PeerID) {
				connections <- manifestOverlayConnection{overlay: source, peerID: peerID}
			})
			client := startRunningManifestOverlay(t,
				peermanagement.WithCompression(false), enableRelay,
				peermanagement.WithMaxOutbound(1),
				peermanagement.WithFixedPeers(source.overlay.ListenAddr()),
			)
			var connection manifestOverlayConnection
			select {
			case connection = <-connections:
			case <-time.After(5 * time.Second):
				t.Fatal("transaction source did not connect")
			}

			a := newTestAdaptor(t)
			router := newTestRouter(&mockEngine{}, a, nil)
			validBlob := routerSignedPaymentWithMemo(t, "")
			pending, err := openledger.ParsePendingTx(validBlob)
			require.NoError(t, err)
			transactions := []message.Transaction{
				{RawTransaction: []byte{1, 2, 3}, Status: message.TxStatusCurrent},
				{RawTransaction: validBlob, Status: message.TxStatusCurrent},
			}
			if reduceRelay {
				for _, memo := range []string{"01", "02"} {
					parsed, err := tx.ParseFromBinary(routerSignedPaymentWithMemo(t, memo))
					require.NoError(t, err)
					parsed.GetCommon().TxnSignature = "DEADBEEF"
					fields, err := parsed.Flatten()
					require.NoError(t, err)
					blob, err := binarycodec.EncodeBytes(fields)
					require.NoError(t, err)
					transactions = append(transactions, message.Transaction{RawTransaction: blob, Status: message.TxStatusCurrent})
				}
			}

			before := len(journal.snapshot())
			if reduceRelay {
				frame, err := message.EncodeFrame(&message.Transactions{Transactions: transactions})
				require.NoError(t, err)
				require.NoError(t, source.overlay.Send(connection.peerID, frame))
			} else {
				for i := range transactions {
					frame, err := message.EncodeFrame(&transactions[i])
					require.NoError(t, err)
					require.NoError(t, source.overlay.Send(connection.peerID, frame))
				}
			}
			for range transactions {
				select {
				case inbound := <-client.overlay.TxMessages():
					router.handleInboundMessage(inbound)
				case <-time.After(5 * time.Second):
					t.Fatal("transaction did not reach the processing lane")
				}
			}
			require.True(t, adaptorHasTx(t, a, consensus.TxID(pending.Hash)))

			counts := make(map[string]int)
			for _, charge := range journal.snapshot()[before:] {
				if strings.HasPrefix(charge.context, message.TypeTransaction.String()) ||
					strings.HasPrefix(charge.context, "transaction-") {
					counts[charge.fee]++
				}
			}
			want := map[string]int{resource.FeeInvalidData().String(): 1}
			if reduceRelay {
				want[resource.FeeInvalidSignature().String()] = 2
			} else {
				want[resource.FeeTrivialPeer().String()] = 1
			}
			require.Equal(t, want, counts)
			if reduceRelay {
				before = len(journal.snapshot())
				// Force submission to panic after the transaction passes admission.
				router.adaptor = nil
				frame, err := message.EncodeFrame(&message.Transactions{Transactions: []message.Transaction{
					{RawTransaction: routerSignedPaymentWithMemo(t, "03"), Status: message.TxStatusCurrent},
					{RawTransaction: []byte{1, 2, 3}, Status: message.TxStatusCurrent},
				}})
				require.NoError(t, err)
				require.NoError(t, source.overlay.Send(connection.peerID, frame))
				for range 2 {
					select {
					case inbound := <-client.overlay.TxMessages():
						router.handleInboundMessage(inbound)
					case <-time.After(5 * time.Second):
						t.Fatal("transaction did not reach the processing lane")
					}
				}
				var contexts []string
				for _, charge := range journal.snapshot()[before:] {
					if charge.fee == resource.FeeInvalidData().String() {
						contexts = append(contexts, charge.context)
					}
				}
				require.ElementsMatch(t, []string{
					"panic-transaction",
					message.TypeTransactions.String() + " transaction-invalid-data",
				}, contexts)
			}
		})
	}
}
