package message

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
)

var wantPayloadLimits = map[MessageType]uint32{
	TypeManifests:               MaxMessageSize,
	TypePing:                    MaxMessageSize,
	TypeCluster:                 64 * 1024,
	TypeEndpoints:               64 * 1024,
	TypeTransaction:             1024 * 1024,
	TypeGetLedger:               1024 * 1024,
	TypeLedgerData:              MaxMessageSize,
	TypeProposeLedger:           64 * 1024,
	TypeStatusChange:            64 * 1024,
	TypeHaveSet:                 64 * 1024,
	TypeValidation:              64 * 1024,
	TypeGetObjects:              MaxMessageSize,
	TypeSquelch:                 2048,
	TypeValidatorListCollection: MaxMessageSize,
	TypeProofPathReq:            1024 * 1024,
	TypeProofPathResponse:       MaxMessageSize,
	TypeReplayDeltaReq:          1024 * 1024,
	TypeReplayDeltaResponse:     MaxMessageSize,
	TypeHaveTransactions:        64 * 1024,
	TypeTransactions:            MaxMessageSize,
}

// TestPayloadLimitsAndKnownTypesPinned pins the per-type payload cap and the
// known-type answer for every type code below 128 and the top of the range.
// Unlisted codes are unknown and bounded only by the protocol ceiling.
func TestPayloadLimitsAndKnownTypesPinned(t *testing.T) {
	codes := []MessageType{0xFFFF}
	for code := range MessageType(128) {
		codes = append(codes, code)
	}
	for _, msgType := range codes {
		want, known := wantPayloadLimits[msgType]
		if !known {
			want = MaxMessageSize
		}
		assert.Equal(t, want, MaxPayloadSizeForType(msgType), "limit %d", msgType)
		assert.Equal(t, known, IsKnownMessageType(msgType), "known %d", msgType)
	}
	for code := range proto.MessageType_name {
		_, pinned := wantPayloadLimits[MessageType(code)]
		assert.True(t, pinned, "wire type %d has no pinned limit", code)
	}
}

type collectionLimitKey struct {
	msgType MessageType
	field   protowire.Number
}

var wantRootCollectionLimits = map[collectionLimitKey]WireLimitReason{
	{TypeTransactions, 1}:            WireLimitTransactions,
	{TypeGetObjects, 6}:              WireLimitGetObjects,
	{TypeLedgerData, 4}:              WireLimitLedgerData,
	{TypeEndpoints, 3}:               WireLimitEndpoints,
	{TypeValidatorListCollection, 3}: WireLimitValidatorBlobs,
	{TypeCluster, 1}:                 WireLimitClusterNodes,
	{TypeCluster, 2}:                 WireLimitLoadSources,
}

// TestPreflightRootCollectionLimitsPinned pins which repeated root message
// fields carry a count limit: every repeated message field of every known
// type is flooded past the largest collection limit, and only the pinned
// fields may trip a collection limit.
func TestPreflightRootCollectionLimitsPinned(t *testing.T) {
	const flood = maxGetObjects + 1
	seen := make(map[collectionLimitKey]bool)
	for code := range proto.MessageType_name {
		msgType := MessageType(code)
		desc := wireDescriptorFor(t, msgType)
		fields := desc.Fields()
		for i := range fields.Len() {
			field := fields.Get(i)
			if !field.IsList() || field.Kind() != protoreflect.MessageKind {
				continue
			}
			key := collectionLimitKey{msgType, field.Number()}
			t.Run(fmt.Sprintf("%s/%d", msgType, field.Number()), func(t *testing.T) {
				var payload []byte
				for range flood {
					payload = protowire.AppendTag(payload, field.Number(), protowire.BytesType)
					payload = protowire.AppendBytes(payload, nil)
				}
				err := Preflight(msgType, payload)
				reason, limited := wantRootCollectionLimits[key]
				if !limited {
					assert.NoError(t, err)
					return
				}
				seen[key] = true
				var limitErr *WireLimitError
				require.True(t, errors.As(err, &limitErr), "%v", err)
				assert.Equal(t, reason, limitErr.Reason)
			})
		}
	}
	assert.Len(t, seen, len(wantRootCollectionLimits))
}

func wireDescriptorFor(t *testing.T, msgType MessageType) protoreflect.MessageDescriptor {
	t.Helper()
	c, ok := codecFor(msgType)
	require.True(t, ok, "no codec for %s", msgType)
	return c.newProto().ProtoReflect().Descriptor()
}

// TestCodecTableComplete requires exactly one well-formed codec per wire
// type in the protobuf enum and nothing else in the table.
func TestCodecTableComplete(t *testing.T) {
	for code, c := range codecs {
		msgType := MessageType(code)
		_, inEnum := proto.MessageType_name[int32(code)]
		if c == nil {
			assert.False(t, inEnum, "wire type %s has no codec", msgType)
			continue
		}
		require.True(t, inEnum, "codec for %d is not a protobuf wire type", code)
		assert.NotZero(t, c.maxPayload, msgType.String())
		assert.LessOrEqual(t, c.maxPayload, uint32(MaxMessageSize), msgType.String())
		assert.LessOrEqual(t, len(c.collections), maxCollectionLimits, msgType.String())
		fields := wireDescriptors[code].Fields()
		for _, limit := range c.collections {
			field := fields.ByNumber(limit.field)
			require.NotNil(t, field, "%s collection field %d", msgType, limit.field)
			assert.True(t, field.IsList() && field.Kind() == protoreflect.MessageKind,
				"%s collection field %d must be a repeated message", msgType, limit.field)
			assert.Positive(t, limit.max)
		}
	}
	for code := range proto.MessageType_name {
		assert.True(t, IsKnownMessageType(MessageType(code)), "wire type %d", code)
	}
}

// TestCodecsRoundTripEmptyMessage encodes the message decoded from an empty
// protobuf and requires decode/encode to be stable and type-consistent.
func TestCodecsRoundTripEmptyMessage(t *testing.T) {
	for code, c := range codecs {
		if c == nil {
			continue
		}
		msgType := MessageType(code)
		t.Run(msgType.String(), func(t *testing.T) {
			empty, err := c.decode(c.newProto())
			require.NoError(t, err)
			require.Equal(t, msgType, empty.Type())
			wire, err := Encode(empty)
			require.NoError(t, err)
			decoded, err := Decode(msgType, wire)
			require.NoError(t, err)
			require.Equal(t, msgType, decoded.Type())
			again, err := Encode(decoded)
			require.NoError(t, err)
			assert.Equal(t, wire, again)
		})
	}
}

var benchLimitSink uint32

func BenchmarkPayloadLimitLookup(b *testing.B) {
	types := []MessageType{0, TypeManifests, TypePing, TypeTransaction, TypeProposeLedger, TypeValidation, TypeSquelch, TypeTransactions, 99}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		msgType := types[i%len(types)]
		benchLimitSink += MaxPayloadSizeForType(msgType)
		if IsKnownMessageType(msgType) {
			benchLimitSink++
		}
	}
}
