package message

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecompressLZ4LongLiterals(t *testing.T) {
	for _, literalSize := range []int{2048, 4096, 8192} {
		for _, matchSize := range []int{4, 19, 64} {
			t.Run(fmt.Sprintf("literals=%d/match=%d", literalSize, matchSize), func(t *testing.T) {
				literals := make([]byte, literalSize)
				for i := range literals {
					literals[i] = byte(i)
				}
				matchLength := matchSize - 4
				block := []byte{0xf0 | byte(min(matchLength, 15))}
				remaining := literalSize - 15
				for remaining >= 255 {
					block = append(block, 255)
					remaining -= 255
				}
				block = append(block, byte(remaining))
				block = append(block, literals...)
				block = binary.LittleEndian.AppendUint16(block, uint16(literalSize))
				if matchLength >= 15 {
					block = append(block, byte(matchLength-15))
				}
				ending := []byte("lastpart")
				block = append(block, 0x80)
				block = append(block, ending...)

				want := append(bytes.Clone(literals), literals[:matchSize]...)
				want = append(want, ending...)
				got, err := DecompressLZ4(block, len(want))
				require.NoError(t, err)
				require.Equal(t, want, got)
				_, err = DecompressLZ4(block, len(want)-1)
				require.ErrorIs(t, err, ErrDecompressFailed)
				_, err = DecompressLZ4(block, len(want)+1)
				require.ErrorIs(t, err, ErrDecompressFailed)
			})
		}
	}
}

func TestCompressionManifestWireRoundtrip(t *testing.T) {
	msg := &Manifests{List: []Manifest{{STObject: bytes.Repeat([]byte("manifest"), 1024)}}}
	frame, err := EncodeFrame(msg)
	require.NoError(t, err)
	wire, compressed := CompressFrameIfWorthwhile(frame)
	require.True(t, compressed)
	require.Equal(t, byte(0x90), wire[0]&0xfc)
	require.Equal(t, uint16(TypeManifests), binary.BigEndian.Uint16(wire[4:6]))
	require.Equal(t, uint32(len(frame)-6), binary.BigEndian.Uint32(wire[6:10]))
	require.Equal(t, uint32(len(wire)-10), binary.BigEndian.Uint32(wire[:4])&0x03ffffff)

	reader := bytes.NewReader(wire)
	header, err := ReadHeader(reader)
	require.NoError(t, err)
	payload, err := ReadPayload(reader, *header)
	require.NoError(t, err)
	require.Zero(t, reader.Len())
	plain, err := DecompressLZ4(payload, int(header.UncompressedSize))
	require.NoError(t, err)
	require.Equal(t, frame[HeaderSizeUncompressed:], plain)
	decoded, err := Decode(header.MessageType, plain)
	require.NoError(t, err)
	require.Equal(t, msg, decoded)
}
