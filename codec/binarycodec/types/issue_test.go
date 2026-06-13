package types

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

func TestIssue_FromJson(t *testing.T) {
	tt := []struct {
		name        string
		input       any
		expected    []byte
		expectedErr error
	}{
		{
			name: "pass - valid xrp issue object",
			input: map[string]any{
				"currency": "XRP",
			},
			expected: []byte{
				0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0,
			},
			expectedErr: nil,
		},
		{
			name: "pass - valid issue iou object",
			input: map[string]any{
				"currency": "USD",
				"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			},
			expected: []byte{
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 85, 83, 68, 0, 0, 0, 0, 0,
				174, 18, 58, 133, 86, 243, 207, 145, 21, 71,
				17, 55, 106, 251, 15, 137, 79, 131, 43, 61,
			},
		},
		{
			name: "pass - valid xrp issue object",
			input: map[string]any{
				"currency": "0123456789ABCDEF0123456789ABCDEF01234567",
				"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			},
			expected: []byte{
				1, 35, 69, 103, 137, 171, 205, 239, 1,
				35, 69, 103, 137, 171, 205, 239, 1, 35,
				69, 103, 174, 18, 58, 133, 86, 243, 207,
				145, 21, 71, 17, 55, 106, 251, 15, 137,
				79, 131, 43, 61,
			},
		},
		{
			// MPT asset serializes to the 44-byte wire form (issuer + noAccount
			// marker + little-endian sequence), the inverse of ToJSON and matching
			// rippled's STIssue::add. This is exactly the wire blob the ToJSON MPT
			// test decodes back into this mpt_issuance_id.
			name: "pass - valid mpt issuance id",
			input: map[string]any{
				"mpt_issuance_id": "BAADF00DBAADF00DBAADF00DBAADF00DBAADF00DBAADF00D",
			},
			expected: []byte{
				// issuer (20 bytes) = mpt_issuance_id bytes 4..24
				186, 173, 240, 13, 186, 173, 240, 13, 186, 173,
				240, 13, 186, 173, 240, 13, 186, 173, 240, 13,
				// noAccount black-hole marker (20 bytes)
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0, 0, 1,
				// sequence 0xBAADF00D little-endian (4 bytes)
				13, 240, 173, 186,
			},
		},
		{
			name:        "fail - invalid Issue",
			input:       "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p2",
			expected:    nil,
			expectedErr: ErrInvalidIssueObject,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			issue := &Issue{}
			actual, err := issue.FromJSON(tc.input)
			require.Equal(t, tc.expected, actual)
			require.Equal(t, tc.expectedErr, err)
		})
	}
}

func TestIssue_ToJson(t *testing.T) {
	tt := []struct {
		name     string
		input    []byte
		expected any
		err      error
	}{
		{
			name: "pass - valid issue object",
			input: []byte{
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 85, 83, 68, 0, 0, 0, 0, 0,
				174, 18, 58, 133, 86, 243, 207, 145, 21, 71,
				17, 55, 106, 251, 15, 137, 79, 131, 43, 61,
			},
			expected: map[string]any{
				"currency": "USD",
				"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			},
			err: nil,
		},
		{
			name:  "pass - valid xrp issue object",
			input: append([]byte(nil), zeroByteArray...),
			expected: map[string]any{
				"currency": "XRP",
			},
			err: nil,
		},
		{
			name: "pass - mpt issuance id",
			// Wire format: issuerAccount (20) + NO_ACCOUNT (20) + sequence LE (4)
			input: []byte{
				0xBA, 0xAD, 0xF0, 0x0D, 0xBA, 0xAD, 0xF0, 0x0D, 0xBA, 0xAD,
				0xF0, 0x0D, 0xBA, 0xAD, 0xF0, 0x0D, 0xBA, 0xAD, 0xF0, 0x0D,
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0, 0, 1,
				0x0D, 0xF0, 0xAD, 0xBA,
			},
			expected: map[string]any{
				// mpt_issuance_id = sequence BE (4 bytes) + issuerAccount (20 bytes)
				"mpt_issuance_id": "BAADF00DBAADF00DBAADF00DBAADF00DBAADF00DBAADF00D",
			},
			err: nil,
		},
		{
			name:     "fail - truncated data",
			input:    []byte{0, 0},
			expected: nil,
			err:      serdes.ErrParserOutOfBound,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			issue := &Issue{}
			actual, err := issue.ToJSON(testParser(tc.input))

			if tc.err != nil {
				require.Error(t, err)
				require.Equal(t, tc.err, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

func TestDecodeCurrencyBytes(t *testing.T) {
	std := func(b12, b13, b14 byte) []byte {
		c := make([]byte, 20)
		c[12], c[13], c[14] = b12, b13, b14
		return c
	}

	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{"all-zero renders XRP", make([]byte, 20), "XRP"},
		{"noCurrency sentinel renders 1", noCurrencyBytes, "1"},
		{"standard iso code", std(0x55, 0x53, 0x44), "USD"},
		{"lowercase iso code round-trips unmodified", std(0x75, 0x73, 0x64), "usd"},
		// rippled to_string forbids an ISO-style "XRP", so it renders as hex.
		{"iso-form XRP renders as hex", std(0x58, 0x52, 0x50), "0000000000000000000000005852500000000000"},
		// A non-printable code in standard position is not a valid ISO code.
		{"non-printable standard position renders hex", std(0x80, 0x41, 0x42), "0000000000000000000000008041420000000000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeCurrencyCode(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, got)
		})
	}
}
