package lending_test

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
)

func TestLoanSetCounterpartySignatureIngress(t *testing.T) {
	registerLending()
	jsonTransaction := []byte(`{
		"Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"TransactionType":"LoanSet",
		"Fee":"20",
		"Sequence":1,
		"SigningPubKey":"ED0000000000000000000000000000000000000000000000000000000000000001",
		"LoanBrokerID":"1111111111111111111111111111111111111111111111111111111111111111",
		"PrincipalRequested":"1",
		"CounterpartySignature":{
			"SigningPubKey":"ED0000000000000000000000000000000000000000000000000000000000000002",
			"TxnSignature":"AA"
		}
	}`)

	assertCounterparty := func(name string, transaction tx.Transaction) *lending.LoanSet {
		t.Helper()
		loanSet, ok := transaction.(*lending.LoanSet)
		if !ok {
			t.Fatalf("%s parsed as %T, want *lending.LoanSet", name, transaction)
		}
		counterparty := loanSet.GetCommon().CounterpartySignature
		if counterparty == nil || counterparty.TxnSignature != "AA" {
			t.Fatalf("%s CounterpartySignature = %#v", name, counterparty)
		}
		if got := loanSet.CalculateBaseFee(nil, tx.EngineConfig{BaseFee: 10}); got != 20 {
			t.Fatalf("%s CalculateBaseFee = %d, want 20", name, got)
		}
		return loanSet
	}

	parsedJSON, err := tx.FromJSON(jsonTransaction)
	if err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	loanSet := assertCounterparty("JSON", parsedJSON)
	flat, err := loanSet.Flatten()
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	blob, err := binarycodec.Encode(flat)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	raw, err := hex.DecodeString(blob)
	if err != nil {
		t.Fatalf("decode blob: %v", err)
	}
	parsedBinary, err := tx.ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("parse binary: %v", err)
	}
	assertCounterparty("binary", parsedBinary)
}
