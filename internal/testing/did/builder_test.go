package did

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txdid "github.com/LeJamon/go-xrpl/internal/tx/did"
)

func TestDIDSetBuilderEncodingIntent(t *testing.T) {
	alice := jtx.NewAccount("alice")

	for _, raw := range []string{"AB", "4142", "deadbeef"} {
		t.Run("raw URI "+raw, func(t *testing.T) {
			built := DIDSet(alice).URI(raw).Build()
			require.Equal(t, hex.EncodeToString([]byte(raw)), built.URI)
			require.True(t, built.HasField("URI"))
		})
	}

	for _, tc := range []struct {
		name string
		hex  string
	}{
		{"lowercase", "deadbeef"},
		{"uppercase", "DEADBEEF"},
	} {
		t.Run("hex "+tc.name, func(t *testing.T) {
			built := DIDSet(alice).URIHex(tc.hex).Build()
			require.Equal(t, tc.hex, built.URI)
			require.NoError(t, built.Validate())
		})
	}

	for _, invalid := range []string{"A", "GG"} {
		t.Run("invalid hex "+invalid, func(t *testing.T) {
			require.Error(t, DIDSet(alice).URIHex(invalid).Build().Validate())
		})
	}
}

func TestDIDSetBuilderFieldPresenceAndOptions(t *testing.T) {
	alice := jtx.NewAccount("alice")
	built := DIDSet(alice).
		URI("").
		DocumentHex("").
		Data("").
		Fee(0).
		Sequence(0).
		Build()

	require.IsType(t, (*txdid.DIDSet)(nil), built)
	require.Empty(t, built.URI)
	require.Empty(t, built.DIDDocument)
	require.Empty(t, built.Data)
	require.True(t, built.HasField("URI"))
	require.True(t, built.HasField("DIDDocument"))
	require.True(t, built.HasField("Data"))
	require.Equal(t, "0", built.Fee)
	require.NotNil(t, built.Sequence)
	require.Zero(t, *built.Sequence)

	omitted := DIDSet(alice).Build()
	require.False(t, omitted.HasField("URI"))
	require.False(t, omitted.HasField("DIDDocument"))
	require.False(t, omitted.HasField("Data"))
	require.IsType(t, (*txdid.DIDDelete)(nil), DIDDelete(alice).Fee(0).Sequence(0).Build())
}
