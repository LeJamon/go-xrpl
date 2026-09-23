package types

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
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
			name: "pass - valid xrp issue object with null issuer",
			input: map[string]any{
				"currency": "XRP",
				"issuer":   nil,
			},
			expected:    make([]byte, 20),
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
		{
			name: "fail - iou missing issuer",
			input: map[string]any{
				"currency": "USD",
			},
			expectedErr: ErrInvalidIssuer,
		},
		{
			name: "fail - iou non-string issuer",
			input: map[string]any{
				"currency": "USD",
				"issuer":   nil,
			},
			expectedErr: ErrInvalidIssuer,
		},
		{
			name: "fail - xrp with issuer",
			input: map[string]any{
				"currency": "XRP",
				"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			},
			expectedErr: ErrInvalidIssuer,
		},
		{
			name: "fail - iou with xrp account issuer",
			input: map[string]any{
				"currency": "USD",
				"issuer":   "rrrrrrrrrrrrrrrrrrrrrhoLvTp",
			},
			expectedErr: ErrInvalidIssuer,
		},
		{
			name: "fail - iou with no account issuer",
			input: map[string]any{
				"currency": "USD",
				"issuer":   "rrrrrrrrrrrrrrrrrrrrBZbvji",
			},
			expectedErr: ErrInvalidIssuer,
		},
		{
			name: "fail - iou with invalid base58 issuer",
			input: map[string]any{
				"currency": "USD",
				"issuer":   "not_a_valid_address",
			},
			expectedErr: addresscodec.ErrInvalidClassicAddress,
		},
		{
			name: "fail - no currency sentinel",
			input: map[string]any{
				"currency": "1",
				"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			},
			expectedErr: ErrInvalidCurrency,
		},
		{
			name: "fail - bad currency sentinel",
			input: map[string]any{
				"currency": "0000000000000000000000005852500000000000",
				"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
			},
			expectedErr: ErrInvalidCurrency,
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

func TestIssue_MPTSequenceEndianVectors(t *testing.T) {
	tests := []struct {
		name        string
		canonicalID string
		wire        []byte
	}{
		{
			name:        "sequence 00000001",
			canonicalID: "00000001AE123A8556F3CF91154711376AFB0F894F832B3D",
			wire: []byte{
				0xAE, 0x12, 0x3A, 0x85, 0x56, 0xF3, 0xCF, 0x91, 0x15, 0x47,
				0x11, 0x37, 0x6A, 0xFB, 0x0F, 0x89, 0x4F, 0x83, 0x2B, 0x3D,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
				0x01, 0x00, 0x00, 0x00,
			},
		},
		{
			name:        "sequence 01020304",
			canonicalID: "01020304AE123A8556F3CF91154711376AFB0F894F832B3D",
			wire: []byte{
				0xAE, 0x12, 0x3A, 0x85, 0x56, 0xF3, 0xCF, 0x91, 0x15, 0x47,
				0x11, 0x37, 0x6A, 0xFB, 0x0F, 0x89, 0x4F, 0x83, 0x2B, 0x3D,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
				0x04, 0x03, 0x02, 0x01,
			},
		},
		{
			name:        "sequence A1B2C3D4",
			canonicalID: "A1B2C3D4AE123A8556F3CF91154711376AFB0F894F832B3D",
			wire: []byte{
				0xAE, 0x12, 0x3A, 0x85, 0x56, 0xF3, 0xCF, 0x91, 0x15, 0x47,
				0x11, 0x37, 0x6A, 0xFB, 0x0F, 0x89, 0x4F, 0x83, 0x2B, 0x3D,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
				0xD4, 0xC3, 0xB2, 0xA1,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issue := &Issue{}
			json := map[string]any{"mpt_issuance_id": tc.canonicalID}

			encoded, err := issue.FromJSON(json)
			require.NoError(t, err)
			require.Equal(t, tc.wire, encoded)

			decoded, err := issue.ToJSON(testParser(tc.wire))
			require.NoError(t, err)
			require.Equal(t, json, decoded)

			reencoded, err := issue.FromJSON(decoded)
			require.NoError(t, err)
			require.Equal(t, tc.wire, reencoded)
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
