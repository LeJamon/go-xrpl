package entry

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

const writerTestAccount = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

func TestWriterStyles(t *testing.T) {
	t.Run("required and optional zero remain present", func(t *testing.T) {
		check := completeWriterCheck(true)
		check.SetExpiration(0)

		got, err := check.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		want, err := binarycodec.EncodeBytes(map[string]any{
			"LedgerEntryType":   "Check",
			"Account":           writerTestAccount,
			"Destination":       writerTestAccount,
			"SendMax":           "0",
			"Sequence":          uint32(0),
			"OwnerNode":         "0",
			"DestinationNode":   "0",
			"Expiration":        uint32(0),
			"Flags":             uint32(0),
			"PreviousTxnID":     strings.Repeat("0", 64),
			"PreviousTxnLgrSeq": uint32(0),
		})
		if err != nil {
			t.Fatalf("encode expected map: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("writer bytes differ:\n got  %X\n want %X", got, want)
		}
	})

	t.Run("default zero is omitted", func(t *testing.T) {
		var amm AMM
		amm.SetTradingFee(500)
		if got := amm.ToMap()["TradingFee"]; got != 500 {
			t.Fatalf("TradingFee after non-default setter = %v, want 500", got)
		}
		amm.SetTradingFee(0)
		if _, ok := amm.ToMap()["TradingFee"]; ok {
			t.Fatal("zero soeDEFAULT TradingFee remained present")
		}

		var vault Vault
		vault.SetAssetsTotal("0")
		if _, ok := vault.ToMap()["AssetsTotal"]; ok {
			t.Fatal("zero soeDEFAULT AssetsTotal remained present")
		}
	})
}

func TestFreshEncodeValidatesRequiredFields(t *testing.T) {
	check := completeWriterCheck(false)
	_, err := check.Encode()
	if err == nil || !strings.Contains(err.Error(), "required field Flags") {
		t.Fatalf("Encode error = %v, want missing Flags", err)
	}

	check.SetFlags(0)
	if _, err := check.Encode(); err != nil {
		t.Fatalf("Encode with all non-threading required fields: %v", err)
	}
}

func TestDecodedEncodeTolerance(t *testing.T) {
	t.Run("missing historical required fields round trip", func(t *testing.T) {
		encoded, err := binarycodec.EncodeBytes(map[string]any{
			"LedgerEntryType": "Check",
			"Flags":           uint32(0),
		})
		if err != nil {
			t.Fatalf("encode fixture: %v", err)
		}

		var check Check
		if err := DecodeLegacy(&check, encoded); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		roundTrip, err := check.Encode()
		if err != nil {
			t.Fatalf("Encode decoded fixture: %v", err)
		}
		canonicalized, err := binarycodec.EncodeBytes(map[string]any{
			"LedgerEntryType":   "Check",
			"Flags":             uint32(0),
			"PreviousTxnID":     strings.Repeat("0", 64),
			"PreviousTxnLgrSeq": uint32(0),
		})
		if err != nil {
			t.Fatalf("encode canonicalized fixture: %v", err)
		}
		if !bytes.Equal(roundTrip, canonicalized) {
			t.Fatalf("canonicalized bytes differ:\n got  %X\n want %X", roundTrip, canonicalized)
		}

		check.SetExpiration(0)
		if _, err := check.Encode(); err == nil || !strings.Contains(err.Error(), "required field Account") {
			t.Fatalf("Encode after mutation error = %v, want missing Account", err)
		}
	})

	t.Run("explicit historical default round trips", func(t *testing.T) {
		encoded, err := binarycodec.EncodeBytes(map[string]any{
			"LedgerEntryType": "AMM",
			"TradingFee":      0,
			"Flags":           uint32(0),
		})
		if err != nil {
			t.Fatalf("encode fixture: %v", err)
		}

		var amm AMM
		if err := DecodeLegacy(&amm, encoded); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		roundTrip, err := amm.Encode()
		if err != nil {
			t.Fatalf("Encode decoded fixture: %v", err)
		}
		if !bytes.Equal(roundTrip, encoded) {
			t.Fatalf("round trip differs:\n got  %X\n want %X", roundTrip, encoded)
		}
	})
}

func TestDecodeValidatesLedgerEntryType(t *testing.T) {
	wrongType, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "Offer",
		"Flags":           uint32(0),
	})
	if err != nil {
		t.Fatalf("encode wrong-type fixture: %v", err)
	}
	var check Check
	if err := check.Decode(wrongType); err == nil || !strings.Contains(err.Error(), "want 67") {
		t.Fatalf("Decode wrong type error = %v", err)
	}

	missingType, err := binarycodec.EncodeBytes(map[string]any{"Flags": uint32(0)})
	if err != nil {
		t.Fatalf("encode missing-type fixture: %v", err)
	}
	if err := check.Decode(missingType); err == nil || !strings.Contains(err.Error(), "missing LedgerEntryType") {
		t.Fatalf("Decode missing type error = %v", err)
	}
}

func TestDecodeEnforcesLedgerTemplate(t *testing.T) {
	missingRequired, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "Check",
		"Flags":           uint32(0),
	})
	if err != nil {
		t.Fatalf("encode missing-required fixture: %v", err)
	}
	var check Check
	if err := check.Decode(missingRequired); err == nil || !strings.Contains(err.Error(), "required field Account is missing") {
		t.Fatalf("Decode missing required error = %v", err)
	}

	explicitDefault, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           writerTestAccount,
		"Balance":           "0",
		"Sequence":          uint32(0),
		"OwnerCount":        uint32(0),
		"MintedNFTokens":    uint32(0),
		"Flags":             uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatalf("encode explicit-default fixture: %v", err)
	}
	var account AccountRoot
	if err := account.Decode(explicitDefault); err == nil || !strings.Contains(err.Error(), "default field MintedNFTokens is explicitly set") {
		t.Fatalf("Decode explicit default error = %v", err)
	}
}

func TestDecodeRejectsDuplicateFields(t *testing.T) {
	typeField, err := binarycodec.EncodeBytes(map[string]any{"LedgerEntryType": "Check"})
	if err != nil {
		t.Fatalf("encode type field: %v", err)
	}
	valid, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "Check",
		"Flags":           uint32(0),
	})
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	duplicate := append(append([]byte(nil), typeField...), valid...)

	var check Check
	if err := check.Decode(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate field") {
		t.Fatalf("Decode duplicate error = %v, want duplicate field", err)
	}
}

func TestNFTokenOfferLegacyAccountDecodesAsOwner(t *testing.T) {
	legacy, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "NFTokenOffer",
		"Account":         writerTestAccount,
	})
	if err != nil {
		t.Fatalf("encode legacy fixture: %v", err)
	}

	var offer NFTokenOffer
	if err := offer.Decode(legacy); err == nil || !strings.Contains(err.Error(), "is not allowed") {
		t.Fatalf("strict Decode legacy sfAccount error = %v", err)
	}
	if err := DecodeLegacy(&offer, legacy); err != nil {
		t.Fatalf("Decode legacy sfAccount: %v", err)
	}
	fields := offer.ToMap()
	if got := fields["Owner"]; got != writerTestAccount {
		t.Fatalf("Owner = %v, want %s", got, writerTestAccount)
	}
	if _, ok := fields["Account"]; ok {
		t.Fatal("legacy Account remained in canonical map")
	}

	canonical, err := offer.Encode()
	if err != nil {
		t.Fatalf("Encode canonical offer: %v", err)
	}
	decoded, err := binarycodec.DecodeBytes(canonical)
	if err != nil {
		t.Fatalf("decode canonical bytes: %v", err)
	}
	if got := decoded["Owner"]; got != writerTestAccount {
		t.Fatalf("canonical Owner = %v, want %s", got, writerTestAccount)
	}
	if _, ok := decoded["Account"]; ok {
		t.Fatal("canonical bytes contain legacy Account")
	}
}

func completeWriterCheck(setFlags bool) *Check {
	check := new(Check)
	check.SetAccount(writerTestAccount)
	check.SetDestination(writerTestAccount)
	check.SetSendMax("0")
	check.SetSequence(0)
	check.SetOwnerNode("0")
	check.SetDestinationNode("0")
	if setFlags {
		check.SetFlags(0)
	}
	return check
}
