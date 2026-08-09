package mpt

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestConfidentialMPTWireAmountAndOptionalPresence(t *testing.T) {
	data := []byte(`{
		"Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"TransactionType":"ConfidentialMPTConvert",
		"MPTokenIssuanceID":"00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12",
		"MPTAmount":"9007199254740993",
		"HolderEncryptionKey":"",
		"HolderEncryptedAmount":"00",
		"IssuerEncryptedAmount":"00",
		"AuditorEncryptedAmount":"",
		"BlindingFactor":"00",
		"ZKProof":""
	}`)
	var transaction ConfidentialMPTConvert
	if err := json.Unmarshal(data, &transaction); err != nil {
		t.Fatal(err)
	}
	if transaction.MPTAmount != 9_007_199_254_740_993 {
		t.Fatalf("MPTAmount = %d", transaction.MPTAmount)
	}
	if transaction.HolderEncryptionKey == nil || transaction.AuditorEncryptedAmount == nil || transaction.ZKProof == nil {
		t.Fatal("present-empty optional field was treated as absent")
	}
	flat, err := transaction.Flatten()
	if err != nil {
		t.Fatal(err)
	}
	if got := flat["MPTAmount"]; got != "9007199254740993" {
		t.Fatalf("flattened MPTAmount = %#v", got)
	}
}

func TestConfidentialMPTUnmarshalClearsReusedReceiver(t *testing.T) {
	holderKey, auditorAmount, proof := "02", "03", "04"
	convert := ConfidentialMPTConvert{
		MPTokenIssuanceID: "stale", MPTAmount: 9, HolderEncryptionKey: &holderKey,
		HolderEncryptedAmount: "stale", IssuerEncryptedAmount: "stale",
		AuditorEncryptedAmount: &auditorAmount, BlindingFactor: "stale", ZKProof: &proof,
	}
	if err := json.Unmarshal([]byte(`{"MPTAmount":"7"}`), &convert); err != nil {
		t.Fatal(err)
	}
	if convert.MPTAmount != 7 || convert.MPTokenIssuanceID != "" || convert.HolderEncryptionKey != nil ||
		convert.AuditorEncryptedAmount != nil || convert.ZKProof != nil {
		t.Fatalf("reused Convert retained stale fields: %+v", convert)
	}

	convertBack := ConfidentialMPTConvertBack{
		MPTokenIssuanceID: "stale", MPTAmount: 9, AuditorEncryptedAmount: &auditorAmount,
		HolderEncryptedAmount: "stale", IssuerEncryptedAmount: "stale", BlindingFactor: "stale",
		ZKProof: "stale", BalanceCommitment: "stale",
	}
	if err := json.Unmarshal([]byte(`{"MPTAmount":"8"}`), &convertBack); err != nil {
		t.Fatal(err)
	}
	if convertBack.MPTAmount != 8 || convertBack.MPTokenIssuanceID != "" || convertBack.AuditorEncryptedAmount != nil ||
		convertBack.HolderEncryptedAmount != "" || convertBack.ZKProof != "" || convertBack.BalanceCommitment != "" {
		t.Fatalf("reused ConvertBack retained stale fields: %+v", convertBack)
	}
}

func TestConfidentialMPTProtocolShape(t *testing.T) {
	rules := amendment.AllSupportedRules()
	tests := []struct {
		transaction interface {
			TxType() tx.Type
			GetFlagsMask(*amendment.Rules) uint32
			RequiredAmendments() [][32]byte
		}
		want tx.Type
	}{
		{&ConfidentialMPTConvert{}, tx.TypeConfidentialMPTConvert},
		{&ConfidentialMPTMergeInbox{}, tx.TypeConfidentialMPTMergeInbox},
		{&ConfidentialMPTConvertBack{}, tx.TypeConfidentialMPTConvertBack},
	}
	for _, test := range tests {
		if got := test.transaction.TxType(); got != test.want {
			t.Fatalf("TxType() = %v, want %v", got, test.want)
		}
		if got := test.transaction.GetFlagsMask(rules); got != tx.TfUniversalMask {
			t.Fatalf("GetFlagsMask() = %#x", got)
		}
		features := test.transaction.RequiredAmendments()
		if len(features) != 1 || features[0] != amendment.FeatureConfidentialTransfer {
			t.Fatalf("RequiredAmendments() = %x", features)
		}
	}
}
