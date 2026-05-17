package inbound

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildSkipListLeafSLE encodes a LedgerHashes SLE with the supplied
// rolling-256 ancestor list and lastLedgerSeq, returning the raw
// binary-codec payload (the byte string the state-map leaf stores).
func buildSkipListLeafSLE(t *testing.T, hashes [][32]byte, lastSeq uint32) []byte {
	t.Helper()
	hashHexes := make([]string, len(hashes))
	for i, h := range hashes {
		hashHexes[i] = fmt.Sprintf("%064X", h)
	}
	obj := map[string]any{
		"LedgerEntryType":    "LedgerHashes",
		"Flags":              uint32(0),
		"Hashes":             hashHexes,
		"LastLedgerSequence": lastSeq,
	}
	hx, err := binarycodec.Encode(obj)
	require.NoError(t, err)
	out, err := hex.DecodeString(hx)
	require.NoError(t, err)
	return out
}

// buildSkipListProof installs a single LedgerHashes leaf into a fresh
// account-state SHAMap at the canonical keylet::skip() key, then
// returns the map's root hash and the leaf-to-root proof path.
func buildSkipListProof(t *testing.T, leafPayload []byte) (rootHash [32]byte, path [][]byte) {
	t.Helper()
	sm, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)

	skipKL := keylet.LedgerHashes()
	require.NoError(t, sm.PutWithNodeType(skipKL.Key, leafPayload, shamap.NodeTypeAccountState))
	require.NoError(t, sm.SetImmutable())

	root, err := sm.Hash()
	require.NoError(t, err)

	proof, err := sm.GetProofPath(skipKL.Key)
	require.NoError(t, err)
	require.True(t, proof.Found, "proof must locate the inserted leaf")
	require.NotEmpty(t, proof.Path, "proof path must not be empty")

	return root, proof.Path
}

// fixedHashes returns n deterministic 32-byte hashes — entry i has
// every byte equal to byte(i+1). Useful for round-trip assertions that
// verify the verified ordering matches what we encoded.
func fixedHashes(n int) [][32]byte {
	out := make([][32]byte, n)
	for i := range out {
		for j := range out[i] {
			out[i][j] = byte(i + 1)
		}
	}
	return out
}

// TestSkipListAcquire_ValidProof_Accepted verifies the happy path:
// a peer serves a well-formed LedgerHashes leaf with a valid proof
// against the target's stateHash, and we recover the ancestor list
// byte-for-byte.
func TestSkipListAcquire_ValidProof_Accepted(t *testing.T) {
	t.Parallel()
	want := fixedHashes(256)
	leaf := buildSkipListLeafSLE(t, want, 999)
	stateHash, path := buildSkipListProof(t, leaf)

	targetHash := [32]byte{0xAA, 0xBB}

	skipKey := keylet.LedgerHashes().Key
	resp := &message.ProofPathResponse{
		LedgerHash: targetHash[:],
		Key:        skipKey[:],
		MapType:    message.LedgerMapAccountState,
		Path:       path,
	}

	s := NewSkipListAcquire(targetHash, stateHash, 42, nil)
	require.NoError(t, s.GotResponse(resp))
	assert.True(t, s.IsComplete())
	assert.Nil(t, s.Err())

	got := s.Hashes()
	require.Len(t, got, len(want))
	for i := range want {
		assert.Equalf(t, want[i], got[i],
			"hash[%d] mismatch — verified ordering must match wire bytes", i)
	}
}

// TestSkipListAcquire_InvalidProof_Rejected covers every way a peer
// can fail to produce a valid proof. Each sub-case must terminate in
// StateFailed with no Hashes() leak.
func TestSkipListAcquire_InvalidProof_Rejected(t *testing.T) {
	t.Parallel()
	want := fixedHashes(8)
	leaf := buildSkipListLeafSLE(t, want, 100)
	goodStateHash, goodPath := buildSkipListProof(t, leaf)
	targetHash := [32]byte{0xCC}
	skipKey := keylet.LedgerHashes().Key

	cases := []struct {
		name    string
		mutate  func(resp *message.ProofPathResponse, stateHash *[32]byte)
		wantErr error
	}{
		{
			name: "tampered_leaf",
			mutate: func(resp *message.ProofPathResponse, _ *[32]byte) {
				// Re-encode a leaf with different content and substitute
				// it in for the proof's leaf node. The recomputed leaf
				// hash will no longer match the parent inner-node's
				// branch hash, so the proof must fail.
				tampered := buildSkipListLeafSLE(t, fixedHashes(4), 7)
				// path[0] is the leaf node (leaf-to-root order). Build a
				// fresh leaf node at the same key with the tampered
				// payload, then re-serialize it for wire.
				sm, err := shamap.New(shamap.TypeState)
				require.NoError(t, err)
				require.NoError(t, sm.PutWithNodeType(skipKey, tampered, shamap.NodeTypeAccountState))
				require.NoError(t, sm.SetImmutable())
				badProof, err := sm.GetProofPath(skipKey)
				require.NoError(t, err)
				// Substitute only the leaf — keep the rest of the original
				// (good) inner-node stack so the failure is unambiguously
				// "the leaf doesn't match", not "wrong inner node".
				newPath := make([][]byte, len(resp.Path))
				copy(newPath, resp.Path)
				newPath[0] = badProof.Path[0]
				resp.Path = newPath
			},
			wantErr: ErrSkipListProofInvalid,
		},
		{
			name: "wrong_state_hash",
			mutate: func(_ *message.ProofPathResponse, stateHash *[32]byte) {
				stateHash[0] ^= 0xFF
			},
			wantErr: ErrSkipListProofInvalid,
		},
		{
			name: "wrong_ledger_hash",
			mutate: func(resp *message.ProofPathResponse, _ *[32]byte) {
				bad := make([]byte, 32)
				bad[0] = 0x99
				resp.LedgerHash = bad
			},
			wantErr: ErrSkipListResponseMismatch,
		},
		{
			name: "wrong_map_type",
			mutate: func(resp *message.ProofPathResponse, _ *[32]byte) {
				resp.MapType = message.LedgerMapTransaction
			},
			wantErr: ErrSkipListResponseMismatch,
		},
		{
			name: "wrong_key",
			mutate: func(resp *message.ProofPathResponse, _ *[32]byte) {
				bogus := make([]byte, 32)
				bogus[0] = 0x77
				resp.Key = bogus
			},
			wantErr: ErrSkipListResponseMismatch,
		},
		{
			name: "empty_path",
			mutate: func(resp *message.ProofPathResponse, _ *[32]byte) {
				resp.Path = nil
			},
			wantErr: ErrSkipListProofInvalid,
		},
		{
			name: "peer_signaled_error",
			mutate: func(resp *message.ProofPathResponse, _ *[32]byte) {
				resp.Error = message.ReplyErrorNoLedger
			},
			wantErr: ErrSkipListResponseMismatch,
		},
		{
			// Non-empty header bytes that don't hash back to
			// targetHash must be rejected, matching rippled's
			// processProofPathResponse hash-mismatch check.
			name: "header_mismatch",
			mutate: func(resp *message.ProofPathResponse, _ *[32]byte) {
				resp.LedgerHeader = []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB}
			},
			wantErr: ErrSkipListResponseMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Build fresh copies so each sub-case mutates independently.
			pathCopy := make([][]byte, len(goodPath))
			for i, b := range goodPath {
				pathCopy[i] = append([]byte(nil), b...)
			}
			resp := &message.ProofPathResponse{
				LedgerHash: append([]byte(nil), targetHash[:]...),
				Key:        append([]byte(nil), skipKey[:]...),
				MapType:    message.LedgerMapAccountState,
				Path:       pathCopy,
			}
			stateHash := goodStateHash
			tc.mutate(resp, &stateHash)

			s := NewSkipListAcquire(targetHash, stateHash, 1, nil)
			err := s.GotResponse(resp)
			require.Error(t, err, "sub-case %q must reject", tc.name)
			assert.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, StateFailed, s.State())
			assert.Nil(t, s.Hashes(), "no hash leakage on failure")
		})
	}
}

// TestSkipListAcquire_DecodeLedgerHashesSLE_NonHashesLeaf rejects a
// proof whose leaf decodes as a different SLE type. A peer that serves
// a valid Merkle proof for a non-LedgerHashes entry under the
// keylet::skip() key has produced a cryptographically valid but
// semantically nonsense proof — must reject as proof-invalid.
func TestSkipListAcquire_DecodeLedgerHashesSLE_NonHashesLeaf(t *testing.T) {
	t.Parallel(
	// Encode a FeeSettings SLE — not a LedgerHashes — and prove it
	// under the same key. The proof is valid; only the leaf's
	// LedgerEntryType is wrong.
	)

	hx, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "FeeSettings",
		"Flags":             uint32(0),
		"BaseFee":           "A",
		"ReferenceFeeUnits": uint32(10),
		"ReserveBase":       uint32(200000000),
		"ReserveIncrement":  uint32(50000000),
	})
	require.NoError(t, err)
	leaf, err := hex.DecodeString(hx)
	require.NoError(t, err)
	stateHash, path := buildSkipListProof(t, leaf)

	targetHash := [32]byte{0xDD}
	skipKey := keylet.LedgerHashes().Key
	resp := &message.ProofPathResponse{
		LedgerHash: targetHash[:],
		Key:        skipKey[:],
		MapType:    message.LedgerMapAccountState,
		Path:       path,
	}

	s := NewSkipListAcquire(targetHash, stateHash, 1, nil)
	err = s.GotResponse(resp)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSkipListProofInvalid)
	assert.Equal(t, StateFailed, s.State())
}
