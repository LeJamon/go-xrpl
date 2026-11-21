package adaptor

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// skipListProofResponse builds a proof path response that proves the skip
// list of the ledger whose header it carries.
func skipListProofResponse(t *testing.T) *message.ProofPathResponse {
	t.Helper()
	skip := keylet.LedgerHashes().Key
	state := shamap.New(shamap.TypeState)
	require.NoError(t, state.Put(skip, bytes.Repeat([]byte{0x5A}, 16)))
	for i := range byte(16) {
		require.NoError(t, state.Put([32]byte{i << 4, 1}, bytes.Repeat([]byte{i}, 16)))
	}
	root, err := state.Hash()
	require.NoError(t, err)
	proof, err := state.GetProofPath(skip)
	require.NoError(t, err)
	require.True(t, proof.Found)
	require.Greater(t, len(proof.Path), 1)

	ledger := header.LedgerHeader{LedgerIndex: 9, Drops: 1, AccountHash: root, CloseTimeResolution: 10}
	hash := header.CalculateHash(ledger)
	return &message.ProofPathResponse{
		Key:          skip[:],
		LedgerHash:   hash[:],
		MapType:      message.LedgerMapAccountState,
		LedgerHeader: header.AddRaw(ledger, false),
		Path:         proof.Path,
	}
}

func proofPathResponseCharges(t *testing.T, payload []byte) []badDataCall {
	t.Helper()
	r, rs := makeRouterWithBadDataRecorder(t)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeProofPathResponse,
		Payload: payload,
	})
	return rs.getBadDataCalls()
}

// TestRouter_HandleProofPathResponse_Charges mirrors rippled's verification of
// every proof path response it receives: a peer-reported error is invalid
// data, and a response that does not prove the skip list of the ledger it
// names is malformed. A valid response is dropped uncharged because this node
// never requests proof paths.
func TestRouter_HandleProofPathResponse_Charges(t *testing.T) {
	otherKey := [32]byte{0x10, 1}
	tests := []struct {
		name   string
		mutate func(*message.ProofPathResponse)
		reason string
	}{
		{"valid", func(*message.ProofPathResponse) {}, ""},
		{"header with trailing bytes", func(r *message.ProofPathResponse) {
			r.LedgerHeader = append(r.LedgerHeader, 0xAB)
		}, ""},
		{"peer error", func(r *message.ProofPathResponse) {
			r.Error, r.ErrorSet = message.ReplyErrorNoLedger, true
		}, "proof-path-resp-error"},
		{"peer error with malformed fields", func(r *message.ProofPathResponse) {
			r.Key, r.LedgerHeader, r.Path = nil, nil, nil
			r.Error, r.ErrorSet = message.ReplyErrorBadRequest, true
		}, "proof-path-resp-error"},
		{"undersized ledger hash", func(r *message.ProofPathResponse) { r.LedgerHash = bytes.Repeat([]byte{1}, 31) }, "proof-path-resp-malformed"},
		{"oversized ledger hash", func(r *message.ProofPathResponse) { r.LedgerHash = bytes.Repeat([]byte{1}, 33) }, "proof-path-resp-malformed"},
		{"empty ledger hash", func(r *message.ProofPathResponse) { r.LedgerHash = nil }, "proof-path-resp-malformed"},
		{"undersized key", func(r *message.ProofPathResponse) { r.Key = bytes.Repeat([]byte{1}, 31) }, "proof-path-resp-malformed"},
		{"oversized key", func(r *message.ProofPathResponse) { r.Key = bytes.Repeat([]byte{1}, 33) }, "proof-path-resp-malformed"},
		{"empty key", func(r *message.ProofPathResponse) { r.Key = nil }, "proof-path-resp-malformed"},
		{"missing header", func(r *message.ProofPathResponse) { r.LedgerHeader = nil }, "proof-path-resp-malformed"},
		{"short header", func(r *message.ProofPathResponse) { r.LedgerHeader = r.LedgerHeader[:header.SizeBase-1] }, "proof-path-resp-malformed"},
		{"empty path", func(r *message.ProofPathResponse) { r.Path = nil }, "proof-path-resp-malformed"},
		{"transaction map", func(r *message.ProofPathResponse) { r.MapType = message.LedgerMapTransaction }, "proof-path-resp-malformed"},
		{"header hash mismatch", func(r *message.ProofPathResponse) { r.LedgerHeader[header.SizeBase-1]-- }, "proof-path-resp-malformed"},
		{"key is not the skip list", func(r *message.ProofPathResponse) { r.Key = otherKey[:] }, "proof-path-resp-malformed"},
		{"truncated path", func(r *message.ProofPathResponse) { r.Path = r.Path[:len(r.Path)-1] }, "proof-path-resp-malformed"},
		{"corrupt leaf", func(r *message.ProofPathResponse) { r.Path[0] = []byte{0xff} }, "proof-path-resp-malformed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := skipListProofResponse(t)
			tc.mutate(resp)
			payload, err := message.Encode(resp)
			require.NoError(t, err)

			want := []badDataCall{}
			if tc.reason != "" {
				want = []badDataCall{{peerID: 7, reason: tc.reason}}
			}
			assert.Equal(t, want, proofPathResponseCharges(t, payload))
		})
	}

	t.Run("missing required key", func(t *testing.T) {
		payload := protowire.AppendTag(nil, 2, protowire.BytesType)
		payload = protowire.AppendBytes(payload, make([]byte, 32))
		payload = protowire.AppendTag(payload, 3, protowire.VarintType)
		payload = protowire.AppendVarint(payload, uint64(message.LedgerMapAccountState))
		assert.Equal(t, []badDataCall{{peerID: 7, reason: "proof-path-resp-decode"}},
			proofPathResponseCharges(t, payload))
	})
}
