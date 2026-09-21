package binarycodec

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestDecodeBytesRejectsCompleteUnencodableVL(t *testing.T) {
	header, err := serdes.DefaultFieldIDCodec().Encode("PublicKey")
	require.NoError(t, err)
	data := append(header, 0xFE, 0xD4, 0x18)
	data = append(data, make([]byte, 918745)...)

	_, err = DecodeBytes(data)
	require.ErrorIs(t, err, serdes.ErrVariableLengthTooLong)
}
