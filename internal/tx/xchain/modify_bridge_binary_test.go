package xchain

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestXChainModifyBridgeBinaryRoundTrip(t *testing.T) {
	const lockingDoor = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	const issuingDoor = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const publicKey = "ED0000000000000000000000000000000000000000000000000000000000000000"
	issues := []struct {
		name   string
		fields map[string]any
		asset  tx.Asset
	}{
		{"XRP", map[string]any{"currency": "XRP"}, tx.Asset{Currency: "XRP"}},
		{"IOU", map[string]any{"currency": "USD", "issuer": issuingDoor}, tx.Asset{Currency: "USD", Issuer: issuingDoor}},
	}
	registerBinaryParseTransactionTypes()
	for _, locking := range issues {
		for _, issuing := range issues {
			t.Run(locking.name+"/"+issuing.name, func(t *testing.T) {
				bridge := map[string]any{
					"LockingChainDoor": lockingDoor, "LockingChainIssue": locking.fields,
					"IssuingChainDoor": issuingDoor, "IssuingChainIssue": issuing.fields,
				}
				blob, err := binarycodec.EncodeBytes(map[string]any{
					"TransactionType": "XChainModifyBridge", "Account": lockingDoor,
					"Sequence": uint32(1), "Fee": "12", "SigningPubKey": publicKey,
					"XChainBridge": bridge, "SignatureReward": "1000000",
				})
				require.NoError(t, err)
				parsed, err := tx.ParseFromBinary(blob)
				require.NoError(t, err)
				modify, ok := parsed.(*XChainModifyBridge)
				require.True(t, ok)
				require.Equal(t, XChainBridge{
					LockingChainDoor: lockingDoor, LockingChainIssue: locking.asset,
					IssuingChainDoor: issuingDoor, IssuingChainIssue: issuing.asset,
				}, modify.XChainBridge)
				require.Equal(t, blob, parsed.GetRawBytes())
				matches, err := tx.CurrentFieldsMatchRaw(parsed)
				require.NoError(t, err)
				require.True(t, matches)
				fields, err := parsed.Flatten()
				require.NoError(t, err)
				require.Equal(t, bridge, fields["XChainBridge"])
				require.Equal(t, "1000000", fields["SignatureReward"])
				roundTrip, err := binarycodec.EncodeBytes(fields)
				require.NoError(t, err)
				require.Equal(t, blob, roundTrip)
			})
		}
	}
}
