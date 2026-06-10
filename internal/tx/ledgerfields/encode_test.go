package ledgerfields

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TestRoundTrip_TypedSLE verifies that Decode → Encode is byte-identical
// against the canonical binarycodec output for a representative set of
// ledger entries. Each case covers a distinct value-shape category
// (XRP/IOU Amount, Vector256, STArray, Blob, UInt64-hex, etc.) so a
// regression in any of the typed encoder's per-type paths trips at least
// one case.
func TestRoundTrip_TypedSLE(t *testing.T) {
	cases := []struct {
		name string
		json map[string]any
	}{
		{
			name: "AccountRoot_XRP_Amount",
			json: map[string]any{
				"LedgerEntryType":   "AccountRoot",
				"Account":           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Balance":           "1000000",
				"Sequence":          uint32(1),
				"OwnerCount":        uint32(0),
				"Flags":             uint32(0),
				"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq": uint32(1),
			},
		},
		{
			name: "Offer_IOU_Amounts",
			json: map[string]any{
				"LedgerEntryType": "Offer",
				"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Sequence":        uint32(7),
				"TakerPays":       map[string]any{"value": "100", "currency": "USD", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
				"TakerGets":       "1000000",
				"BookDirectory":   "0000000000000000000000000000000000000000000000000000000000000000",
				"BookNode":        "0",
				"OwnerNode":       "0",
				"Flags":           uint32(0),
			},
		},
		{
			name: "DirectoryNode_Vector256_Indexes",
			json: map[string]any{
				"LedgerEntryType": "DirectoryNode",
				"Flags":           uint32(0),
				"RootIndex":       "1111111111111111111111111111111111111111111111111111111111111111",
				"Indexes": []any{
					"2222222222222222222222222222222222222222222222222222222222222222",
					"3333333333333333333333333333333333333333333333333333333333333333",
				},
				"Owner": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
		},
		{
			name: "SignerList_STArray_SignerEntries",
			json: map[string]any{
				"LedgerEntryType": "SignerList",
				"Flags":           uint32(0),
				"OwnerNode":       "0",
				"SignerQuorum":    uint32(3),
				"SignerEntries": []any{
					map[string]any{
						"SignerEntry": map[string]any{
							"Account":      "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
							"SignerWeight": uint32(1),
						},
					},
				},
				"SignerListID":      uint32(0),
				"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq": uint32(1),
			},
		},
		{
			name: "AMM_Issue_XRP_and_IOU",
			json: map[string]any{
				"LedgerEntryType": "AMM",
				"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"TradingFee":      uint32(500),
				"Asset":           map[string]any{"currency": "XRP"},
				"Asset2": map[string]any{
					"currency": "USD",
					"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
				},
				"LPTokenBalance": map[string]any{
					"value":    "1000",
					"currency": "039C99CD9AB0B70B32ECDA51EAAE471625608EA2",
					"issuer":   "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				},
				"OwnerNode":         "0",
				"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq": uint32(1),
			},
		},
		{
			name: "Bridge_XChainBridge",
			json: map[string]any{
				"LedgerEntryType": "Bridge",
				"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"SignatureReward": "1000",
				"XChainBridge": map[string]any{
					"LockingChainDoor":  "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"LockingChainIssue": "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
					"IssuingChainDoor":  "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
					"IssuingChainIssue": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				},
				"XChainClaimID":            "0",
				"XChainAccountCreateCount": "0",
				"XChainAccountClaimCount":  "0",
				"OwnerNode":                "0",
				"Flags":                    uint32(0),
				"PreviousTxnID":            "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq":        uint32(1),
			},
		},
		{
			name: "XChainOwnedClaimID_Flags",
			json: map[string]any{
				"LedgerEntryType": "XChainOwnedClaimID",
				"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"XChainBridge": map[string]any{
					"LockingChainDoor":  "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"LockingChainIssue": "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
					"IssuingChainDoor":  "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
					"IssuingChainIssue": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				},
				"XChainClaimID":           "1",
				"OtherChainSource":        "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
				"XChainClaimAttestations": []any{},
				"SignatureReward":         "1000",
				"OwnerNode":               "0",
				"Flags":                   uint32(0),
				"PreviousTxnID":           "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq":       uint32(1),
			},
		},
		{
			name: "XChainOwnedCreateAccountClaimID_Flags",
			json: map[string]any{
				"LedgerEntryType": "XChainOwnedCreateAccountClaimID",
				"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"XChainBridge": map[string]any{
					"LockingChainDoor":  "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
					"LockingChainIssue": "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
					"IssuingChainDoor":  "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
					"IssuingChainIssue": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				},
				"XChainAccountCreateCount":        "1",
				"XChainCreateAccountAttestations": []any{},
				"OwnerNode":                       "0",
				"Flags":                           uint32(0),
				"PreviousTxnID":                   "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq":               uint32(1),
			},
		},
		{
			name: "MPTokenIssuance_UInt8_UInt16_BaseTenUInt64",
			json: map[string]any{
				"LedgerEntryType":   "MPTokenIssuance",
				"Issuer":            "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Sequence":          uint32(1),
				"TransferFee":       uint32(500),
				"AssetScale":        uint32(2),
				"MaximumAmount":     "1000000000",
				"OutstandingAmount": "500000000",
				"LockedAmount":      "0",
				"Flags":             uint32(0),
				"OwnerNode":         "0",
				"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq": uint32(1),
			},
		},
		{
			name: "Vault_Number_Hash192",
			json: map[string]any{
				"LedgerEntryType":   "Vault",
				"Sequence":          uint32(1),
				"OwnerNode":         "0",
				"Owner":             "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Account":           "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
				"Asset":             map[string]any{"currency": "XRP"},
				"AssetsTotal":       "1000",
				"AssetsAvailable":   "500",
				"AssetsMaximum":     "10000",
				"LossUnrealized":    "0",
				"ShareMPTID":        "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12",
				"WithdrawalPolicy":  uint32(1),
				"Flags":             uint32(0),
				"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq": uint32(1),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := binarycodec.EncodeBytes(tc.json)
			if err != nil {
				t.Fatalf("binarycodec.EncodeBytes: %v", err)
			}

			entry := New(tc.json["LedgerEntryType"].(string))
			if entry == nil {
				t.Fatalf("no typed entry registered for %q", tc.json["LedgerEntryType"])
			}
			if err := entry.Decode(canonical); err != nil {
				t.Fatalf("Decode: %v", err)
			}

			enc, ok := entry.(interface {
				Encode() ([]byte, error)
			})
			if !ok {
				t.Fatalf("entry %T does not implement Encode()", entry)
			}
			got, err := enc.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(got, canonical) {
				t.Fatalf("round-trip mismatch:\ncanonical: %x\nencoded:   %x", canonical, got)
			}
		})
	}
}

// TestHash_LeafNodeFormula pins the SLE hash to rippled's
// sha512Half(HashPrefixLeafNode || serializedData || index) formula. The
// generated Hash method must produce the same bytes a SHAMap account-state
// leaf would store for this entry.
func TestHash_LeafNodeFormula(t *testing.T) {
	json := map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Balance":           "1000000",
		"Sequence":          uint32(1),
		"OwnerCount":        uint32(0),
		"Flags":             uint32(0),
		"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
		"PreviousTxnLgrSeq": uint32(1),
	}
	canonical, err := binarycodec.EncodeBytes(json)
	if err != nil {
		t.Fatalf("binarycodec.EncodeBytes: %v", err)
	}

	var index [32]byte
	for i := range index {
		index[i] = byte(i + 1)
	}

	expected := common.Sha512Half(protocol.HashPrefixLeafNode[:], canonical, index[:])

	entry := New("AccountRoot")
	if err := entry.Decode(canonical); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	hasher, ok := entry.(interface {
		Hash(index [32]byte) ([32]byte, error)
	})
	if !ok {
		t.Fatalf("entry %T does not implement Hash()", entry)
	}
	got, err := hasher.Hash(index)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if got != expected {
		t.Fatalf("hash mismatch:\nexpected: %x\ngot:      %x", expected, got)
	}
}
