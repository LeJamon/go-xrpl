package mpt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
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

	clawback := ConfidentialMPTClawback{MPTokenIssuanceID: "stale", Holder: "stale", MPTAmount: 9, ZKProof: "stale"}
	if err := json.Unmarshal([]byte(`{"MPTAmount":"9223372036854775807"}`), &clawback); err != nil {
		t.Fatal(err)
	}
	if clawback.MPTAmount != protocol.MaxMPTokenAmount || clawback.MPTokenIssuanceID != "" || clawback.Holder != "" || clawback.ZKProof != "" {
		t.Fatalf("reused Clawback retained stale fields: %+v", clawback)
	}
	flat, err := clawback.Flatten()
	if err != nil || flat["MPTAmount"] != "9223372036854775807" {
		t.Fatalf("clawback flatten = %#v, %v", flat, err)
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
		{&ConfidentialMPTSend{}, tx.TypeConfidentialMPTSend},
		{&ConfidentialMPTClawback{}, tx.TypeConfidentialMPTClawback},
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

func TestConfidentialMPTRegisteredJSONRejectsMissingRequiredFields(t *testing.T) {
	Register()
	for _, test := range []struct {
		name string
		data string
	}{
		{
			name: "send",
			data: `{"TransactionType":"ConfidentialMPTSend","Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"}`,
		},
		{
			name: "clawback",
			data: `{"TransactionType":"ConfidentialMPTClawback","Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction, err := tx.FromJSON([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			err = transaction.Validate()
			if err == nil || !strings.HasPrefix(err.Error(), "temMALFORMED: required confidential") {
				t.Fatalf("Validate() = %v, want missing required-field temMALFORMED", err)
			}
		})
	}
}

func TestConfidentialMPTSendCodecRoundTrip(t *testing.T) {
	input := map[string]any{
		"TransactionType":            "ConfidentialMPTSend",
		"Account":                    "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Sequence":                   uint32(1),
		"Fee":                        "100",
		"SigningPubKey":              "",
		"MPTokenIssuanceID":          "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12",
		"Destination":                "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"DestinationTag":             uint32(7),
		"SenderEncryptedAmount":      makeHex(66, 0x11),
		"DestinationEncryptedAmount": makeHex(66, 0x22),
		"IssuerEncryptedAmount":      makeHex(66, 0x33),
		"AuditorEncryptedAmount":     makeHex(66, 0x44),
		"ZKProof":                    makeHex(mptcrypto.SendProofSize, 0x55),
		"AmountCommitment":           makeHex(33, 0x66),
		"BalanceCommitment":          makeHex(33, 0x77),
		"CredentialIDs":              []any{makeHex(32, 0x88)},
	}
	encoded, err := binarycodec.EncodeBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"SenderEncryptedAmount", "DestinationEncryptedAmount", "IssuerEncryptedAmount", "AuditorEncryptedAmount", "ZKProof", "AmountCommitment", "BalanceCommitment", "CredentialIDs"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("decoded send omitted %s", field)
		}
	}
}

func TestConfidentialMPTClawbackCodecRoundTrip(t *testing.T) {
	input := map[string]any{
		"TransactionType":   "ConfidentialMPTClawback",
		"Account":           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Sequence":          uint32(1),
		"Fee":               "100",
		"SigningPubKey":     "",
		"MPTokenIssuanceID": "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12",
		"Holder":            "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"MPTAmount":         "9223372036854775807",
		"ZKProof":           makeHex(mptcrypto.ClawbackProofSize, 0x11),
	}
	encoded, err := binarycodec.EncodeBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded["MPTAmount"] != "9223372036854775807" {
		t.Fatalf("MPTAmount = %#v", decoded["MPTAmount"])
	}
}

func makeHex(size int, value byte) string {
	digits := "0123456789ABCDEF"
	result := make([]byte, size*2)
	for i := 0; i < size; i++ {
		result[2*i], result[2*i+1] = digits[value>>4], digits[value&15]
	}
	return string(result)
}
