package xchain

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestXChainUInt64BinaryRoundTrip(t *testing.T) {
	const (
		account      = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
		otherAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		publicKey    = "ED0000000000000000000000000000000000000000000000000000000000000000"
		wireValue    = "ABCDEF1234567890"
		value        = uint64(0xABCDEF1234567890)
	)
	bridge := bridgeMap(XChainBridge{
		LockingChainDoor:  account,
		LockingChainIssue: tx.Asset{Currency: "XRP"},
		IssuingChainDoor:  otherAccount,
		IssuingChainIssue: tx.Asset{Currency: "XRP"},
	})
	attestationFields := func(transactionType string) map[string]any {
		return map[string]any{
			"Account":                  account,
			"TransactionType":          transactionType,
			"XChainBridge":             bridge,
			"OtherChainSource":         otherAccount,
			"Amount":                   "1000000",
			"AttestationRewardAccount": account,
			"AttestationSignerAccount": account,
			"PublicKey":                publicKey,
			"Signature":                "00",
			"WasLockingChainSend":      1,
		}
	}

	tests := []struct {
		name      string
		wireField string
		fields    map[string]any
		assert    func(*testing.T, tx.Transaction)
	}{
		{
			name:      "commit claim ID",
			wireField: "XChainClaimID",
			fields: map[string]any{
				"Account":         account,
				"TransactionType": tx.TypeXChainCommit.String(),
				"XChainBridge":    bridge,
				"XChainClaimID":   wireValue,
				"Amount":          "1000000",
			},
			assert: func(t *testing.T, transaction tx.Transaction) {
				t.Helper()
				parsed, ok := transaction.(*XChainCommit)
				require.True(t, ok)
				require.Equal(t, value, parsed.XChainClaimID)
			},
		},
		{
			name:      "claim claim ID",
			wireField: "XChainClaimID",
			fields: map[string]any{
				"Account":         account,
				"TransactionType": tx.TypeXChainClaim.String(),
				"XChainBridge":    bridge,
				"XChainClaimID":   wireValue,
				"Destination":     otherAccount,
				"Amount":          "1000000",
			},
			assert: func(t *testing.T, transaction tx.Transaction) {
				t.Helper()
				parsed, ok := transaction.(*XChainClaim)
				require.True(t, ok)
				require.Equal(t, value, parsed.XChainClaimID)
			},
		},
		{
			name:      "claim attestation claim ID",
			wireField: "XChainClaimID",
			fields: func() map[string]any {
				fields := attestationFields(tx.TypeXChainAddClaimAttestation.String())
				fields["XChainClaimID"] = wireValue
				return fields
			}(),
			assert: func(t *testing.T, transaction tx.Transaction) {
				t.Helper()
				parsed, ok := transaction.(*XChainAddClaimAttestation)
				require.True(t, ok)
				require.Equal(t, value, parsed.XChainClaimID)
			},
		},
		{
			name:      "account-create attestation count",
			wireField: "XChainAccountCreateCount",
			fields: func() map[string]any {
				fields := attestationFields(tx.TypeXChainAddAccountCreateAttest.String())
				fields["XChainAccountCreateCount"] = wireValue
				fields["Destination"] = otherAccount
				fields["SignatureReward"] = "10"
				return fields
			}(),
			assert: func(t *testing.T, transaction tx.Transaction) {
				t.Helper()
				parsed, ok := transaction.(*XChainAddAccountCreateAttestation)
				require.True(t, ok)
				require.Equal(t, value, parsed.XChainAccountCreateCount)
			},
		},
	}

	Register()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.fields["Sequence"] = uint32(1)
			test.fields["Fee"] = "12"
			test.fields["SigningPubKey"] = publicKey
			blob, err := binarycodec.EncodeBytes(test.fields)
			require.NoError(t, err)

			decoded, err := binarycodec.DecodeBytes(blob)
			require.NoError(t, err)
			require.Equal(t, "abcdef1234567890", decoded[test.wireField])

			parsed, err := tx.ParseFromBinary(blob)
			require.NoError(t, err)
			test.assert(t, parsed)
			require.Equal(t, blob, parsed.GetRawBytes())
			matches, err := tx.CurrentFieldsMatchRaw(parsed)
			require.NoError(t, err)
			require.True(t, matches)

			flattened, err := parsed.Flatten()
			require.NoError(t, err)
			roundTripped, err := binarycodec.EncodeBytes(flattened)
			require.NoError(t, err)
			require.Equal(t, blob, roundTripped)
		})
	}
}
