package message

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompressFrameIfWorthwhilePolicy(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		payload  []byte
		compress bool
	}{
		{
			name:     "exactly 70 bytes",
			msgType:  TypeManifests,
			payload:  bytes.Repeat([]byte{'A'}, MinCompressibleSize),
			compress: false,
		},
		{
			name:     "eligible and compressible",
			msgType:  TypeManifests,
			payload:  bytes.Repeat([]byte("manifest"), 512),
			compress: true,
		},
		{
			name:     "ineligible",
			msgType:  TypePing,
			payload:  bytes.Repeat([]byte{'A'}, maxPingSize-HeaderSizeUncompressed),
			compress: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := BuildWireMessage(tt.msgType, tt.payload)
			require.NoError(t, err)
			original := bytes.Clone(frame)

			wire, compressed := CompressFrameIfWorthwhile(frame)

			assert.Equal(t, tt.compress, compressed)
			assert.Equal(t, original, frame)
			if !tt.compress {
				assert.Equal(t, frame, wire)
				return
			}

			header, err := DecodeHeader(wire)
			require.NoError(t, err)
			assert.True(t, header.Compressed)
			assert.Equal(t, tt.msgType, header.MessageType)
			assert.Equal(t, uint32(len(tt.payload)), header.UncompressedSize)
			assert.Less(t, len(wire), len(frame))

			decoded, err := DecompressLZ4(wire[HeaderSizeCompressed:], len(tt.payload))
			require.NoError(t, err)
			assert.Equal(t, tt.payload, decoded)
		})
	}
}

func TestShouldCompressMatchesRippledMessageTypes(t *testing.T) {
	eligible := []MessageType{
		TypeManifests,
		TypeEndpoints,
		TypeTransaction,
		TypeGetLedger,
		TypeLedgerData,
		TypeGetObjects,
		TypeValidatorList,
		TypeValidatorListCollection,
		TypeReplayDeltaResponse,
		TypeTransactions,
	}
	for _, msgType := range eligible {
		assert.True(t, ShouldCompress(msgType), msgType.String())
	}

	for _, msgType := range []MessageType{TypePing, TypeSquelch, TypeValidation, TypeReplayDeltaReq} {
		assert.False(t, ShouldCompress(msgType), msgType.String())
	}
}

func TestCompressIfWorthwhileAccountsForCompressedHeader(t *testing.T) {
	rng := rand.New(rand.NewSource(1392))
	base := make([]byte, 512)
	_, err := rng.Read(base)
	require.NoError(t, err)

	var payload []byte
	var compressed []byte
	for repeated := 4; repeated < len(base); repeated++ {
		candidate := bytes.Clone(base)
		copy(candidate[len(candidate)-repeated:], candidate[:repeated])
		encoded, err := CompressLZ4(candidate)
		require.NoError(t, err)
		if encoded != nil && len(encoded)+HeaderSizeCompressed >= len(candidate)+HeaderSizeUncompressed {
			payload = candidate
			compressed = encoded
			break
		}
	}
	require.NotNil(t, payload, "test fixture must compress without saving the longer header")
	require.Less(t, len(compressed), len(payload))

	selected, ok := CompressIfWorthwhile(TypeManifests, payload)

	assert.False(t, ok)
	assert.Equal(t, payload, selected)
}

func TestCompressFrameIfWorthwhileLeavesIncompressiblePayloadUnchanged(t *testing.T) {
	payload := make([]byte, 4096)
	_, err := rand.New(rand.NewSource(1392)).Read(payload)
	require.NoError(t, err)
	frame, err := BuildWireMessage(TypeManifests, payload)
	require.NoError(t, err)

	wire, compressed := CompressFrameIfWorthwhile(frame)

	assert.False(t, compressed)
	assert.Equal(t, frame, wire)
}
