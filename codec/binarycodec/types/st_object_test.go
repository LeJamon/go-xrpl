package types

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestStObject_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       any
		output      []byte
		expectedErr error
	}{
		{
			name:        "fail - input is not a map",
			input:       1,
			output:      nil,
			expectedErr: errors.New("not a valid json"),
		},
		// {}
		{
			name: "fail - not found error",
			input: map[string]any{
				"IncorrectField": 89,
				"Flags":          525288,
				"OfferSequence":  1752791,
			},
			output:      nil,
			expectedErr: errors.New("FieldName IncorrectField not found"),
		},
		{
			name: "pass - convert valid Json",
			input: map[string]any{
				"Fee":           "10",
				"Flags":         uint32(524288),
				"OfferSequence": uint32(1752791),
				"TakerGets":     "150000000000",
			},
			output:      []byte{0x22, 0x0, 0x8, 0x0, 0x0, 0x20, 0x19, 0x0, 0x1a, 0xbe, 0xd7, 0x65, 0x40, 0x0, 0x0, 0x22, 0xec, 0xb2, 0x5c, 0x0, 0x68, 0x40, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0xa},
			expectedErr: nil,
		},
		{
			name: "pass - convert valid STObject with variable length",
			input: map[string]any{
				"TransactionType":   "Payment",
				"TransactionResult": 0,
				"Fee":               "10",
				"Flags":             uint32(524288),
				"OfferSequence":     uint32(1752791),
				"TakerGets":         "150000000000",
			},
			output:      []byte{0x12, 0x0, 0x0, 0x22, 0x0, 0x8, 0x0, 0x0, 0x20, 0x19, 0x0, 0x1a, 0xbe, 0xd7, 0x65, 0x40, 0x0, 0x0, 0x22, 0xec, 0xb2, 0x5c, 0x0, 0x68, 0x40, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0xa, 0x3, 0x10, 0x0},
			expectedErr: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			serializer := serdes.NewBinarySerializer(serdes.NewFieldIDCodec(definitions.Get()))
			stObject := NewSTObject(serializer)

			got, err := stObject.FromJSON(tc.input)
			if tc.expectedErr != nil {
				require.EqualError(t, err, tc.expectedErr.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.output, got)
			}
		})
	}
}

func TestStObject_ToJson(t *testing.T) {
	defs := definitions.Get()

	testcases := []struct {
		name        string
		malleate    func(t *testing.T) *serdes.BinaryParser
		output      any
		expectedErr error
	}{
		{
			"fail - binary parser read field error",
			func(t *testing.T) *serdes.BinaryParser {
				// A structurally valid 3-byte header with no matching field.
				return serdes.NewBinaryParser([]byte{0, 200, 200}, defs)
			},
			nil,
			errors.New("ReadField error: FieldHeader {200 200} not found"),
		},
		{
			"pass - convert valid STObject",
			func(t *testing.T) *serdes.BinaryParser {
				return serdes.NewBinaryParser([]byte{0x22, 0x0, 0x8, 0x0, 0x0, 0x20, 0x19, 0x0, 0x1a, 0xbe, 0xd7, 0x65, 0x40, 0x0, 0x0, 0x22, 0xec, 0xb2, 0x5c, 0x0, 0x68, 0x40, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0xa}, defs)
			},
			map[string]any{
				"Fee":           "10",
				"Flags":         uint32(524288),
				"OfferSequence": uint32(1752791),
				"TakerGets":     "150000000000",
			},
			nil,
		},
		{
			"pass - convert valid STObject with variable length",
			func(t *testing.T) *serdes.BinaryParser {
				return serdes.NewBinaryParser([]byte{0x12, 0x0, 0x0, 0x22, 0x0, 0x8, 0x0, 0x0, 0x20, 0x19, 0x0, 0x1a, 0xbe, 0xd7, 0x65, 0x40, 0x0, 0x0, 0x22, 0xec, 0xb2, 0x5c, 0x0, 0x68, 0x40, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0xa, 0x3, 0x10, 0x0}, defs)
			},
			map[string]any{
				"TransactionType":   "Payment",
				"TransactionResult": "tesSUCCESS",
				"Fee":               "10",
				"Flags":             uint32(524288),
				"OfferSequence":     uint32(1752791),
				"TakerGets":         "150000000000",
			},
			nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			parser := tc.malleate(t)
			stObject := NewSTObject(serdes.NewBinarySerializer(serdes.NewFieldIDCodec(definitions.Get())))
			got, err := stObject.ToJSON(parser)
			if tc.expectedErr != nil {
				require.EqualError(t, err, tc.expectedErr.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.output, got)
			}
		})
	}
}

func TestGetSortedKeys(t *testing.T) {
	tt := []struct {
		name   string
		input  map[definitions.FieldInstance]any
		output []definitions.FieldInstance
	}{
		{
			name: "pass - get sorted keys",
			input: map[definitions.FieldInstance]any{
				getFieldInstance(t, "TransactionType"):   1,
				getFieldInstance(t, "TransactionResult"): 0,
				getFieldInstance(t, "IndexNext"):         5100000,
				getFieldInstance(t, "SourceTag"):         1232,
				getFieldInstance(t, "LedgerEntryType"):   1,
			},
			output: []definitions.FieldInstance{
				getFieldInstance(t, "LedgerEntryType"),
				getFieldInstance(t, "TransactionType"),
				getFieldInstance(t, "SourceTag"),
				getFieldInstance(t, "IndexNext"),
				getFieldInstance(t, "TransactionResult"),
			},
		},
		{
			name: "pass - get sorted keys",
			input: map[definitions.FieldInstance]any{
				getFieldInstance(t, "Account"):      "rMBzp8CgpE441cp5PVyA9rpVV7oT8hP3ys",
				getFieldInstance(t, "TransferRate"): 4234,
				getFieldInstance(t, "Expiration"):   23,
			},
			output: []definitions.FieldInstance{
				getFieldInstance(t, "Expiration"),
				getFieldInstance(t, "TransferRate"),
				getFieldInstance(t, "Account"),
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.output, getSortedKeys(tc.input))
		})
	}
}
