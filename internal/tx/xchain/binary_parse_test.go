package xchain

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
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
			"WasLockingChainSend":      2,
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
				require.Equal(t, uint8(2), parsed.WasLockingChainSend)
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
				require.Equal(t, uint8(2), parsed.WasLockingChainSend)
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

func TestXChainUInt64BatchBinaryRoundTrip(t *testing.T) {
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
	inner := func(transactionType string, sequence uint32) map[string]any {
		fields := map[string]any{
			"Account":         account,
			"TransactionType": transactionType,
			"XChainBridge":    bridge,
			"XChainClaimID":   wireValue,
			"Amount":          "1000000",
			"Sequence":        sequence,
			"Fee":             "0",
			"SigningPubKey":   "",
			"Flags":           tx.TfInnerBatchTxn,
		}
		if transactionType == tx.TypeXChainClaim.String() {
			fields["Destination"] = otherAccount
		}
		return fields
	}
	fields := map[string]any{
		"Account":         account,
		"TransactionType": tx.TypeBatch.String(),
		"Sequence":        uint32(1),
		"Fee":             "40",
		"SigningPubKey":   publicKey,
		"Flags":           batch.BatchFlagAllOrNothing,
		"RawTransactions": []any{
			map[string]any{"RawTransaction": inner(tx.TypeXChainCommit.String(), 2)},
			map[string]any{"RawTransaction": inner(tx.TypeXChainClaim.String(), 3)},
		},
	}

	Register()
	batch.Register()
	blob, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)

	parsedTransaction, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	parsed, ok := parsedTransaction.(*batch.Batch)
	require.True(t, ok)
	require.Len(t, parsed.RawTransactions, 2)
	commit, ok := parsed.RawTransactions[0].RawTransaction.InnerTx.(*XChainCommit)
	require.True(t, ok)
	require.Equal(t, value, commit.XChainClaimID)
	claim, ok := parsed.RawTransactions[1].RawTransaction.InnerTx.(*XChainClaim)
	require.True(t, ok)
	require.Equal(t, value, claim.XChainClaimID)
	require.Equal(t, blob, parsed.GetRawBytes())
	matches, err := tx.CurrentFieldsMatchRaw(parsed)
	require.NoError(t, err)
	require.True(t, matches)

	flattened, err := parsed.Flatten()
	require.NoError(t, err)
	roundTripped, err := binarycodec.EncodeBytes(flattened)
	require.NoError(t, err)
	require.Equal(t, blob, roundTripped)
}
