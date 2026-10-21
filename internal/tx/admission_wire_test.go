package tx_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/protocol"
)

const (
	admissionWireAccount     = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	admissionWireDestination = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	admissionWirePublicKey   = "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020"
	admissionWireSignature   = "30440220419BDADCB0A215B93C589012AEC8DB06864C0EEE69671C7E5B7D9E7B9E488A470220047738391CF6F0F1FB9EDADBE8132A92475CF70830EB773F6A631F6BA3F8A064"
)

func admissionWireFields(amount any) map[string]any {
	return map[string]any{
		"TransactionType": "Payment",
		"Account":         admissionWireAccount,
		"Destination":     admissionWireDestination,
		"Amount":          amount,
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   admissionWirePublicKey,
		"TxnSignature":    admissionWireSignature,
	}
}

func TestTransactionBinaryDecodePreservesOverCapAmount(t *testing.T) {
	all.RegisterAll()
	fields := admissionWireFields(binarytypes.RawXRPAmount{Drops: uint64(binarytypes.MaxDrops) + 1})
	wire, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("EncodeBytes: %v", err)
	}

	if _, err := binarycodec.DecodeBytes(wire); err == nil {
		t.Fatal("generic DecodeBytes accepted over-cap native amount")
	}
	decoded, err := binarycodec.DecodeBytesAllowOverCap(wire)
	if err != nil {
		t.Fatalf("DecodeBytesAllowOverCap: %v", err)
	}
	raw, ok := decoded["Amount"].(binarytypes.RawXRPAmount)
	if !ok || raw.Drops != uint64(binarytypes.MaxDrops)+1 || raw.Negative {
		t.Fatalf("decoded Amount = %#v, want positive raw over-cap value", decoded["Amount"])
	}

	parsed, err := tx.ParseFromBinary(wire)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	if !bytes.Equal(parsed.GetRawBytes(), wire) {
		t.Fatal("ParseFromBinary did not retain the canonical over-cap wire bytes")
	}
	originalID, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	currentID, err := tx.ComputeCurrentTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeCurrentTransactionHash: %v", err)
	}
	if originalID != currentID {
		t.Fatalf("current transaction ID = %X, want original %X", currentID, originalID)
	}
	serialized, err := tx.SerializeTransaction(parsed)
	if err != nil || !bytes.Equal(serialized, wire) {
		t.Fatalf("SerializeTransaction = %X, %v; want original wire", serialized, err)
	}
	matches, err := tx.CurrentFieldsMatchRaw(parsed)
	if err != nil || !matches {
		t.Fatalf("CurrentFieldsMatchRaw = %t, %v; want true", matches, err)
	}
	flat, err := parsed.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	reencoded, err := binarycodec.EncodeBytes(flat)
	if err != nil || !bytes.Equal(reencoded, wire) {
		t.Fatalf("Flatten re-encoding = %X, %v; want original wire", reencoded, err)
	}
}

func TestTransactionBinaryDecodeAmountControls(t *testing.T) {
	tests := []struct {
		name    string
		amount  any
		wantRaw bool
	}{
		{name: "maximum legal", amount: "100000000000000000"},
		{name: "over cap", amount: binarytypes.RawXRPAmount{Drops: uint64(binarytypes.MaxDrops) + 1}, wantRaw: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire, err := binarycodec.EncodeBytes(admissionWireFields(tt.amount))
			if err != nil {
				t.Fatalf("EncodeBytes: %v", err)
			}
			decoded, err := binarycodec.DecodeBytesAllowOverCap(wire)
			if err != nil {
				t.Fatalf("DecodeBytesAllowOverCap: %v", err)
			}
			_, raw := decoded["Amount"].(binarytypes.RawXRPAmount)
			if raw != tt.wantRaw {
				t.Fatalf("raw amount = %t, want %t (%#v)", raw, tt.wantRaw, decoded["Amount"])
			}
		})
	}

	legal, err := binarycodec.EncodeBytes(admissionWireFields("0"))
	if err != nil {
		t.Fatalf("EncodeBytes legal zero: %v", err)
	}
	negativeZero := append([]byte(nil), legal...)
	positiveZero := []byte{0x40, 0, 0, 0, 0, 0, 0, 0}
	zeroOffset := bytes.Index(negativeZero, positiveZero)
	if zeroOffset < 0 {
		t.Fatal("could not locate native zero amount in legal wire")
	}
	negativeZero[zeroOffset] = 0
	if _, err := binarycodec.DecodeBytesAllowOverCap(negativeZero); err == nil {
		t.Fatal("negative-zero amount was accepted")
	}
	if _, err := binarycodec.DecodeBytesAllowOverCap(append(legal, 0)); err == nil {
		t.Fatal("trailing malformed field was accepted")
	}

	withoutAmount := admissionWireFields("0")
	delete(withoutAmount, "Amount")
	withoutAmountWire, err := binarycodec.EncodeBytes(withoutAmount)
	if err != nil {
		t.Fatalf("EncodeBytes without Amount: %v", err)
	}
	withoutAmountDecoded, err := binarycodec.DecodeBytesAllowOverCap(withoutAmountWire)
	if err != nil {
		t.Fatalf("DecodeBytesAllowOverCap without Amount: %v", err)
	}
	if _, present := withoutAmountDecoded["Amount"]; present {
		t.Fatal("absent Amount was synthesized during binary decoding")
	}
}

func TestTransactionLocalChecksDistinguishDefaultAndExplicitZeroAccounts(t *testing.T) {
	fields := map[string]any{
		"TransactionType": "AccountSet",
		"Account":         protocol.ZeroAccount,
		"Fee":             "10",
		"Sequence":        uint32(1),
	}
	if reason := tx.TransactionMapLocalChecksFailureReason(tx.TypeAccountSet, fields); reason != "" {
		t.Fatalf("explicit zero source account local-check reason = %q, want no local rejection", reason)
	}

	fields["RegularKey"] = protocol.ZeroAccount
	if reason := tx.TransactionMapLocalChecksFailureReason(tx.TypeAccountSet, fields); reason != "" {
		t.Fatalf("explicit zero optional account local-check reason = %q, want no local rejection", reason)
	}

	delete(fields, "RegularKey")
	if reason := tx.TransactionMapLocalChecksFailureReason(tx.TypeAccountSet, fields); reason != "" {
		t.Fatalf("absent optional account local-check reason = %q, want no local rejection", reason)
	}

	fields["RegularKey"] = ""
	if reason := tx.TransactionMapLocalChecksFailureReason(tx.TypeAccountSet, fields); reason == "" {
		t.Fatal("default empty optional account field passed local checks")
	}
}

func TestTransactionJSONAmountKeepsSupplyCap(t *testing.T) {
	all.RegisterAll()
	for _, test := range []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{name: "maximum legal", amount: "100000000000000000"},
		{name: "over cap", amount: "100000000000000001", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{
				"TransactionType": "Payment",
				"Account":         admissionWireAccount,
				"Destination":     admissionWireDestination,
				"Amount":          test.amount,
				"Fee":             "10",
				"Sequence":        uint32(1),
			})
			if err != nil {
				t.Fatalf("marshal JSON: %v", err)
			}
			_, parseErr := tx.ParseJSON(data)
			if test.wantErr {
				if parseErr == nil || !strings.Contains(parseErr.Error(), "out of range") {
					t.Fatalf("ParseJSON over-cap error = %v, want a supply-cap rejection", parseErr)
				}
				return
			}
			if parseErr != nil {
				t.Fatalf("ParseJSON legal maximum: %v", parseErr)
			}
		})
	}
}
