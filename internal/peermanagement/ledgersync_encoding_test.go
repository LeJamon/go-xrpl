package peermanagement

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func BenchmarkLedgerSyncResponseEncoding(b *testing.B) {
	const chunkSize = 32 * 1024
	chunk := make([]byte, chunkSize)

	b.Run("ProofPath", func(b *testing.B) {
		path := make([][]byte, 8)
		for i := range path {
			path[i] = chunk
		}
		h := NewLedgerSyncHandler(nil)
		h.SetProvider(&fakeProofPathProvider{header: chunk, path: path})
		h.SetPrioritySender(func(context.Context, PeerID, []byte) error { return nil })
		req := &message.ProofPathRequest{
			Key:        fixedKey(),
			LedgerHash: fixedHash(),
			MapType:    message.LedgerMapAccountState,
		}

		b.ReportAllocs()
		b.SetBytes(chunkSize * int64(len(path)+1))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.HandleMessage(context.Background(), PeerID(1), req); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ReplayDelta", func(b *testing.B) {
		txLeaves := make([][]byte, 8)
		for i := range txLeaves {
			txLeaves[i] = chunk
		}
		h := NewLedgerSyncHandler(nil)
		h.SetProvider(&fakeReplayDeltaProvider{header: chunk, txLeaves: txLeaves})
		h.SetPrioritySender(func(context.Context, PeerID, []byte) error { return nil })
		req := &message.ReplayDeltaRequest{LedgerHash: fixedHash()}

		b.ReportAllocs()
		b.SetBytes(chunkSize * int64(len(txLeaves)+1))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := h.HandleMessage(context.Background(), PeerID(1), req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
