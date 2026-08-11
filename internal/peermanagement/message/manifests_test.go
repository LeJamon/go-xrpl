package message

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestWalkManifestsValidatesBeforeVisiting(t *testing.T) {
	validEntry := protowire.AppendTag(nil, 1, protowire.BytesType)
	validEntry = protowire.AppendBytes(validEntry, []byte("manifest"))
	payload := protowire.AppendTag(nil, 1, protowire.BytesType)
	payload = protowire.AppendBytes(payload, validEntry)
	payload = append(payload, protowire.AppendTag(nil, 1, protowire.BytesType)...)
	payload = protowire.AppendVarint(payload, 5)
	payload = append(payload, 0x01)

	visits := 0
	_, err := WalkManifests(payload, func([]byte) { visits++ })
	require.Error(t, err)
	require.Zero(t, visits)
}

func TestManifestPayloadSizeFormula(t *testing.T) {
	require.Equal(t, 358, MaxManifestBytes)
	require.Equal(t, 8, ManifestFramingBytes)
	require.Equal(t, 219600, MaximumManifestsMessageSize(300, 300))
	require.Equal(t, 732000, MaximumManifestsMessageSize(1000, 1000))
	require.Equal(t, 219600, DefaultMaxManifestPayload)
	require.Equal(t, 732000, MaxConfiguredManifestPayload)
}

func TestManifestsAcceptMoreThanFormer100EntryBoundary(t *testing.T) {
	original := &Manifests{List: make([]Manifest, 101)}
	for i := range original.List {
		original.List[i].STObject = []byte{byte(i)}
	}

	payload, err := Encode(original)
	require.NoError(t, err)
	decoded, err := Decode(TypeManifests, payload)
	require.NoError(t, err)
	require.Len(t, decoded.(*Manifests).List, 101)
}

func TestWalkManifestsRejectsMissingRequiredSTObjectBeforeVisiting(t *testing.T) {
	validEntry := protowire.AppendTag(nil, 1, protowire.BytesType)
	validEntry = protowire.AppendBytes(validEntry, []byte("manifest"))
	payload := protowire.AppendTag(nil, 1, protowire.BytesType)
	payload = protowire.AppendBytes(payload, validEntry)
	payload = protowire.AppendTag(payload, 1, protowire.BytesType)
	payload = protowire.AppendBytes(payload, nil)

	visits := 0
	_, err := WalkManifests(payload, func([]byte) { visits++ })
	require.ErrorContains(t, err, "missing required stobject")
	require.Zero(t, visits)
}

func TestWalkManifestsVisitsAliasedEntriesInOrder(t *testing.T) {
	var payload []byte
	for _, wire := range [][]byte{[]byte("first"), []byte("second")} {
		entry := protowire.AppendTag(nil, 1, protowire.BytesType)
		entry = protowire.AppendBytes(entry, wire)
		payload = protowire.AppendTag(payload, 1, protowire.BytesType)
		payload = protowire.AppendBytes(payload, entry)
	}
	payload = protowire.AppendTag(payload, 2, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	payload = protowire.AppendTag(payload, 9, protowire.BytesType)
	payload = protowire.AppendBytes(payload, []byte("ignored"))

	var got [][]byte
	count, err := WalkManifests(payload, func(wire []byte) {
		got = append(got, append([]byte(nil), wire...))
	})
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, [][]byte{[]byte("first"), []byte("second")}, got)
}

func TestWalkManifestsUsesLastSingularSTObject(t *testing.T) {
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendBytes(entry, []byte("old"))
	entry = protowire.AppendTag(entry, 1, protowire.BytesType)
	entry = protowire.AppendBytes(entry, []byte("new"))
	payload := protowire.AppendTag(nil, 1, protowire.BytesType)
	payload = protowire.AppendBytes(payload, entry)

	var got []byte
	count, err := WalkManifests(payload, func(wire []byte) { got = wire })
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, []byte("new"), got)
}

func TestWalkManifestsMatchesDecoderKnownFieldWireErrors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "list",
			payload: protowire.AppendTag(nil, 1, protowire.VarintType),
		},
		{
			name:    "history",
			payload: protowire.AppendTag(nil, 2, protowire.BytesType),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := protowire.AppendVarint(tt.payload, 0)
			_, decodeErr := Decode(TypeManifests, payload)
			_, walkErr := WalkManifests(payload, nil)
			require.NoError(t, decodeErr)
			require.NoError(t, walkErr)
		})
	}
}
