package entry

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestAccountRootDecodeVariableLengthBounds(t *testing.T) {
	base, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           writerTestAccount,
		"Balance":           "0",
		"Sequence":          uint32(1),
		"OwnerCount":        uint32(0),
		"Flags":             uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
		"Domain":            "00",
	})
	require.NoError(t, err)
	header, err := serdes.DefaultFieldIDCodec().Encode("Domain")
	require.NoError(t, err)
	marker := append(append([]byte(nil), header...), 0x01, 0x00)
	offset := bytes.Index(base, marker)
	require.NotEqual(t, -1, offset)

	for _, test := range []struct {
		name      string
		prefix    []byte
		length    int
		invalid   bool
		oversized bool
	}{
		{name: "empty", prefix: []byte{0x00}},
		{name: "one byte maximum", prefix: []byte{0xC0}, length: 192},
		{name: "two byte minimum", prefix: []byte{0xC1, 0x00}, length: 193},
		{name: "two byte maximum", prefix: []byte{0xF0, 0xFF}, length: 12480},
		{name: "three byte minimum", prefix: []byte{0xF1, 0x00, 0x00}, length: 12481},
		{name: "maximum", prefix: []byte{0xFE, 0xD4, 0x17}, length: 918744},
		{name: "first oversized", prefix: []byte{0xFE, 0xD4, 0x18}, length: 918745, invalid: true, oversized: true},
		{name: "last oversized", prefix: []byte{0xFE, 0xFF, 0xFF}, length: 929984, invalid: true, oversized: true},
		{name: "reserved prefix", prefix: []byte{0xFF}, invalid: true},
		{name: "truncated prefix", prefix: []byte{0xFE, 0xD4}, invalid: true},
		{name: "truncated payload", prefix: []byte{0xFE, 0xD4, 0x17}, length: 918743, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte(nil), base[:offset+len(header)]...)
			data = append(data, test.prefix...)
			data = append(data, bytes.Repeat([]byte{0xA5}, test.length)...)
			if !test.invalid || test.oversized {
				data = append(data, base[offset+len(marker):]...)
			}

			var account AccountRoot
			err := account.Decode(data)
			if test.invalid {
				require.Error(t, err)
				if test.oversized {
					require.ErrorIs(t, err, serdes.ErrVariableLengthTooLong)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("A5", test.length), account.Domain)
			roundTrip, err := account.Encode()
			require.NoError(t, err)
			require.Equal(t, data, roundTrip)
		})
	}
}
