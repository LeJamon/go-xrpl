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
		"BlindingFactor":"1111111111111111111111111111111111111111111111111111111111111111",
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
	holderKey := "02"
	auditorAmount := "03"
	proof := "04"
	convert := ConfidentialMPTConvert{
		MPTokenIssuanceID:      "stale",
		MPTAmount:              9,
		HolderEncryptionKey:    &holderKey,
		HolderEncryptedAmount:  "stale",
		IssuerEncryptedAmount:  "stale",
		AuditorEncryptedAmount: &auditorAmount,
		BlindingFactor:         "stale",
		ZKProof:                &proof,
	}
	if err := json.Unmarshal([]byte(`{"MPTAmount":"7"}`), &convert); err != nil {
		t.Fatal(err)
	}
	if convert.MPTAmount != 7 || convert.MPTokenIssuanceID != "" || convert.HolderEncryptedAmount != "" ||
		convert.IssuerEncryptedAmount != "" || convert.BlindingFactor != "" || convert.HolderEncryptionKey != nil ||
		convert.AuditorEncryptedAmount != nil || convert.ZKProof != nil {
		t.Fatalf("reused Convert retained stale fields: %+v", convert)
	}

	convertBack := ConfidentialMPTConvertBack{
		MPTokenIssuanceID:      "stale",
		MPTAmount:              9,
		HolderEncryptedAmount:  "stale",
		IssuerEncryptedAmount:  "stale",
		AuditorEncryptedAmount: &auditorAmount,
		BlindingFactor:         "stale",
		ZKProof:                "stale",
		BalanceCommitment:      "stale",
	}
	if err := json.Unmarshal([]byte(`{"MPTAmount":"8"}`), &convertBack); err != nil {
		t.Fatal(err)
	}
	if convertBack.MPTAmount != 8 || convertBack.MPTokenIssuanceID != "" || convertBack.HolderEncryptedAmount != "" ||
		convertBack.IssuerEncryptedAmount != "" || convertBack.BlindingFactor != "" || convertBack.ZKProof != "" ||
		convertBack.BalanceCommitment != "" || convertBack.AuditorEncryptedAmount != nil {
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

func TestConfidentialMPTCodecRoundTrip(t *testing.T) {
	input := map[string]any{
		"TransactionType":       "ConfidentialMPTConvertBack",
		"Account":               "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Sequence":              uint32(1),
		"Fee":                   "100",
		"SigningPubKey":         "",
		"MPTokenIssuanceID":     "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12",
		"MPTAmount":             "9007199254740993",
		"HolderEncryptedAmount": makeHex(66, 0x11),
		"IssuerEncryptedAmount": makeHex(66, 0x22),
		"BlindingFactor":        makeHex(32, 0x33),
		"ZKProof":               makeHex(816, 0x44),
		"BalanceCommitment":     makeHex(33, 0x55),
	}
	encoded, err := binarycodec.EncodeBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded["TransactionType"] != "ConfidentialMPTConvertBack" {
		t.Fatalf("TransactionType = %#v", decoded["TransactionType"])
	}
	if decoded["MPTAmount"] != "9007199254740993" {
		t.Fatalf("MPTAmount = %#v", decoded["MPTAmount"])
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

func TestConfidentialMPTBaseFee(t *testing.T) {
	transactions := []interface {
		CalculateBaseFee(tx.LedgerView, tx.EngineConfig) uint64
	}{
		&ConfidentialMPTMergeInbox{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTMergeInbox, "")},
		&ConfidentialMPTSend{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTSend, "")},
		&ConfidentialMPTClawback{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, "")},
	}
	for _, transaction := range transactions {
		if got := transaction.CalculateBaseFee(nil, tx.EngineConfig{BaseFee: 10}); got != 100 {
			t.Fatalf("base fee = %d, want 100", got)
		}
	}
	merge := transactions[0].(*ConfidentialMPTMergeInbox)
	merge.Signers = make([]tx.SignerWrapper, 2)
	if got := merge.CalculateBaseFee(nil, tx.EngineConfig{BaseFee: 10}); got != 120 {
		t.Fatalf("multisigned base fee = %d, want 120", got)
	}
}

func TestMPTokenIssuanceSetConfidentialPreflight(t *testing.T) {
	const issuanceID = "000000000000000000000000000000000000000000000001"
	confidentialRules := amendment.NewRulesBuilder().
		Enable(amendment.FeatureConfidentialTransfer).
		Build()
	allRules := amendment.NewRulesBuilder().
		Enable(amendment.FeatureConfidentialTransfer).
		Enable(amendment.FeatureDynamicMPT).
		Build()

	t.Run("Dynamic checks precede encryption key validation", func(t *testing.T) {
		badKey := "00"
		fee := protocol.MaxMPTokenTransferFee + 1
		transaction := NewMPTokenIssuanceSet("rAlice", issuanceID)
		transaction.TransferFee = &fee
		transaction.IssuerEncryptionKey = &badKey
		if err := transaction.PreflightRules(allRules); err == nil || !strings.HasPrefix(err.Error(), "temBAD_TRANSFER_FEE:") {
			t.Fatalf("PreflightRules() = %v, want temBAD_TRANSFER_FEE", err)
		}

		zero := uint32(0)
		transaction = NewMPTokenIssuanceSet("rAlice", issuanceID)
		transaction.ImmutableFlags = &zero
		transaction.IssuerEncryptionKey = &badKey
		if err := transaction.PreflightRules(allRules); err == nil || !strings.HasPrefix(err.Error(), "temINVALID_FLAG:") {
			t.Fatalf("PreflightRules() = %v, want temINVALID_FLAG", err)
		}
	})

	t.Run("privacy enable requires DynamicMPT", func(t *testing.T) {
		transaction := NewMPTokenIssuanceSet("rAlice", issuanceID)
		transaction.Flags = ptrUint32AccountSet(MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance)
		if err := transaction.PreflightRules(confidentialRules); err == nil || !strings.HasPrefix(err.Error(), "temDISABLED:") {
			t.Fatalf("PreflightRules() = %v, want temDISABLED", err)
		}
	})

	t.Run("privacy enable cannot lock", func(t *testing.T) {
		transaction := NewMPTokenIssuanceSet("rAlice", issuanceID)
		transaction.Flags = ptrUint32AccountSet(MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance | MPTokenIssuanceSetFlagLock)
		if err := transaction.PreflightRules(allRules); err == nil || !strings.HasPrefix(err.Error(), "temMALFORMED:") {
			t.Fatalf("PreflightRules() = %v, want temMALFORMED", err)
		}
	})

	t.Run("privacy enable rejects transfer fee", func(t *testing.T) {
		transaction := NewMPTokenIssuanceSet("rAlice", issuanceID)
		transaction.Flags = ptrUint32AccountSet(MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance)
		fee := uint16(1)
		transaction.TransferFee = &fee
		if err := transaction.PreflightRules(allRules); err == nil || !strings.HasPrefix(err.Error(), "temBAD_TRANSFER_FEE:") {
			t.Fatalf("PreflightRules() = %v, want temBAD_TRANSFER_FEE", err)
		}
	})
}

func makeHex(size int, value byte) string {
	digits := "0123456789ABCDEF"
	result := make([]byte, size*2)
	for i := 0; i < size; i++ {
		result[2*i], result[2*i+1] = digits[value>>4], digits[value&15]
	}
	return string(result)
}
