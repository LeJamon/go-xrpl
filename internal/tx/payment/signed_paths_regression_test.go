package payment

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

const signedPathsPaymentWire = "12000024000000016140000000000F424068400000000000000A732103EE83BB432547885C219634A1BC407A9DB0474145D69737D09CCDC63E1DEE7FE37404DEADBEEF8114DD76483FACDEE26E60D8A586BB58D09F27045C46831493B89AFCAD4C8EAC2B131C1331FEF12AE1522BBE011201F3B1997562FD742B54D4EBDEA1D6AEA3D4906B8F30000000000000000000000000555344000000000069D33B18D53385F8A3185516C2EDA5DEDB8AC5C6100000000000000000000000000000000000000000FF614B4E9C06F24296074F7BC48F92A97916C6DC5EA90102030405060708090A0B0C0D0E0F10111213141516171869D33B18D53385F8A3185516C2EDA5DEDB8AC5C6400102030405060708090A0B0C0D0E0F101112131415161718FF01F3B1997562FD742B54D4EBDEA1D6AEA3D4906B8F30000000000000000000000000555344000000000069D33B18D53385F8A3185516C2EDA5DEDB8AC5C610000000000000000000000000000000000000000000"

func TestParseBinaryPaymentPathsPreservesOrderAndDuplicates(t *testing.T) {
	wire, err := hex.DecodeString(signedPathsPaymentWire)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}

	parsedTx, err := tx.ParseFromBinary(wire)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	parsed, ok := parsedTx.(*Payment)
	if !ok {
		t.Fatalf("parsed transaction type = %T, want *Payment", parsedTx)
	}

	wantPaths := [][]PathStep{
		{
			{Account: "rPDXxSZcuVL3ZWoyU82bcde3zwvmShkRyF", Type: 1, TypeHex: "0000000000000001"},
			{Currency: "USD", Issuer: "rweYz56rfmQ98cAdRaeTxQS9wVMGnrdsFp", Type: 48, TypeHex: "0000000000000030"},
			{Currency: "XRP", Type: 16, TypeHex: "0000000000000010"},
		},
		{
			{Account: "rf1BiGeXwwQoi8Z2ueFYTEXSwuJYfV2Jpn", MPTIssuanceID: "0102030405060708090A0B0C0D0E0F101112131415161718", Issuer: "rweYz56rfmQ98cAdRaeTxQS9wVMGnrdsFp", Type: 97, TypeHex: "0000000000000061"},
			{MPTIssuanceID: "0102030405060708090A0B0C0D0E0F101112131415161718", Type: 64, TypeHex: "0000000000000040"},
		},
		{
			{Account: "rPDXxSZcuVL3ZWoyU82bcde3zwvmShkRyF", Type: 1, TypeHex: "0000000000000001"},
			{Currency: "USD", Issuer: "rweYz56rfmQ98cAdRaeTxQS9wVMGnrdsFp", Type: 48, TypeHex: "0000000000000030"},
			{Currency: "XRP", Type: 16, TypeHex: "0000000000000010"},
		},
	}
	if !reflect.DeepEqual(parsed.Paths, wantPaths) {
		t.Fatalf("parsed Paths = %#v, want %#v", parsed.Paths, wantPaths)
	}
	if !bytes.Equal(parsed.GetRawBytes(), wire) {
		t.Fatal("ParseFromBinary did not preserve canonical bytes")
	}

	flat, err := parsed.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	tx.PopulateRequiredWireFields(flat, parsed.GetCommon())
	encoded, err := binarycodec.Encode(flat)
	if err != nil {
		t.Fatalf("Encode flattened payment: %v", err)
	}
	if encoded != signedPathsPaymentWire {
		t.Fatalf("re-encoded payment = %s, want %s", encoded, signedPathsPaymentWire)
	}

	hash, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	if got, want := strings.ToUpper(hex.EncodeToString(hash[:])), "CE0B4AD3934903228059B78F77948584D76B70A0F2E8AE59CD026A43A60476D8"; got != want {
		t.Fatalf("transaction hash = %s, want %s", got, want)
	}
}
