package service

import (
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestProposedOwnerFundsFailureSuppressesPublication(t *testing.T) {
	svc := newOfferTestService(t)
	t.Cleanup(svc.Stop)

	account, _ := addressFromBytes(t, 0x70)
	issuer, _ := addressFromBytes(t, 0x80)
	insertAccountRoot(t, svc, account, 1_000_000_000, 0)
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         account,
		"TakerGets":       "1000000",
		"TakerPays": map[string]any{
			"currency": "USD",
			"issuer":   issuer,
			"value":    "1",
		},
		"Fee":           "10",
		"Sequence":      uint32(1),
		"SigningPubKey": "",
	})
	if err != nil {
		t.Fatalf("encode OfferCreate: %v", err)
	}
	parsed, err := tx.ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("parse OfferCreate: %v", err)
	}
	if err := svc.openLedger.Update(keylet.Fees(), []byte{
		0x11, 0x00, 0x73, 0xff, 0, 0, 0, 0, 0, 0, 0, 0,
	}); err != nil {
		t.Fatalf("corrupt FeeSettings: %v", err)
	}

	if _, applicable, err := proposedOwnerFunds(raw, svc.openLedger); err == nil || !applicable {
		t.Fatalf("proposedOwnerFunds = applicable %t, err %v; want applicable error", applicable, err)
	}
	iouRaw, err := binarycodec.EncodeBytes(map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         account,
		"TakerGets": map[string]any{
			"currency": "USD",
			"issuer":   issuer,
			"value":    "1",
		},
		"TakerPays":     "1000000",
		"Fee":           "10",
		"Sequence":      uint32(1),
		"SigningPubKey": "",
	})
	if err != nil {
		t.Fatalf("encode IOU OfferCreate: %v", err)
	}
	if funds, applicable, err := proposedOwnerFunds(iouRaw, svc.openLedger); err != nil || !applicable || funds != "0" {
		t.Fatalf("IOU proposedOwnerFunds = (%q, %t, %v), want (0, true, nil)", funds, applicable, err)
	}
	published := make(chan SubmittedTxEvent, 1)
	svc.SetSubmittedTxCallback(func(event SubmittedTxEvent) { published <- event })
	svc.dispatchProposedTransaction(openledger.PendingTx{Parsed: parsed}, raw, openledger.SubmitOutcome{
		Result:  ter.TesSUCCESS,
		Applied: true,
	}, svc.openLedger)
	select {
	case event := <-published:
		t.Fatalf("corrupt owner funds published event: %+v", event)
	case <-time.After(25 * time.Millisecond):
	}
}
