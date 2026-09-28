package state

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func TestAccountRootTypedAdapterPreservesAndClearsOptionals(t *testing.T) {
	fields := map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": walkerTestAccount,
		"Balance": "123456789", "Sequence": uint32(3), "OwnerCount": uint32(0), "Flags": uint32(0),
		"PreviousTxnID": zeroHash256, "PreviousTxnLgrSeq": uint32(0),
		"Domain": "", "MessageKey": "", "RegularKey": "", "NFTokenMinter": "",
		"TransferRate": uint32(0), "TickSize": 0, "TicketCount": uint32(0),
		"EmailHash": strings.Repeat("00", 16), "WalletLocator": zeroHash256,
		"WalletSize": uint32(0), "FirstNFTokenSequence": uint32(0), "AccountTxnID": zeroHash256,
		"AMMID": zeroHash256, "VaultID": zeroHash256, "LoanBrokerID": zeroHash256,
	}
	raw, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatal(err)
	}
	account, err := ParseAccountRoot(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := SerializeAccountRoot(account)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, encoded) {
		t.Fatalf("untouched fields changed: want %X, got %X", raw, encoded)
	}
	account.Balance++
	account.HasWalletSize = false
	account.HasAccountTxnID = false
	account.HasFirstNFTSeq = false
	encoded, err = SerializeAccountRoot(account)
	if err != nil {
		t.Fatal(err)
	}
	got, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"WalletSize", "AccountTxnID", "FirstNFTokenSequence"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s was not removed", name)
		}
		delete(fields, name)
	}
	fields["Balance"] = "123456790"
	want, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, encoded) {
		t.Fatalf("unrelated mutation changed fields: want %X, got %X", want, encoded)
	}

	account.Domain = "changed"
	account.TransferRate = 1000000001
	account.TickSize = 5
	account.TicketCount = 1
	account.AMMID[0] = 1
	encoded, err = SerializeAccountRoot(account)
	if err != nil {
		t.Fatal(err)
	}
	account, err = ParseAccountRoot(encoded)
	if err != nil {
		t.Fatal(err)
	}
	account.Domain = ""
	account.TransferRate = 0
	account.TickSize = 0
	account.TicketCount = 0
	account.AMMID = [32]byte{}
	encoded, err = SerializeAccountRoot(account)
	if err != nil {
		t.Fatal(err)
	}
	got, err = binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Domain", "TransferRate", "TickSize", "TicketCount", "AMMID"} {
		if _, ok := got[name]; ok {
			t.Errorf("changed-to-default %s was not removed", name)
		}
	}
}

func TestCheckTypedAdapterPreservesUntouchedFields(t *testing.T) {
	amounts := map[string]any{
		"XRP": "100000000000000000",
		"IOU": map[string]any{"value": "9.999999999999999e80", "currency": "USD", "issuer": walkerTestAccount},
		"MPT": map[string]any{"value": "9223372036854775807", "mpt_issuance_id": strings.Repeat("AB", 24)},
	}
	for name, amount := range amounts {
		t.Run(name, func(t *testing.T) {
			fields := map[string]any{
				"LedgerEntryType": "Check", "Account": walkerTestAccount, "Destination": walkerTestAccount,
				"SendMax": amount, "Sequence": uint32(1), "OwnerNode": "ffffffffffffffff", "DestinationNode": "0",
				"Flags": uint32(0x80000000), "Sponsor": walkerTestAccount, "Expiration": uint32(0),
				"SourceTag": uint32(0), "DestinationTag": uint32(0), "InvoiceID": zeroHash256,
				"PreviousTxnID": zeroHash256, "PreviousTxnLgrSeq": uint32(5),
			}
			raw, err := binarycodec.EncodeBytes(fields)
			if err != nil {
				t.Fatal(err)
			}
			check, err := ParseCheck(raw)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := SerializeCheckFromData(check)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, encoded) {
				t.Fatalf("round trip changed bytes: want %X, got %X", raw, encoded)
			}
			check.Sequence++
			check.HasSourceTag = false
			check.HasDestTag = false
			check.HasInvoiceID = false
			encoded, err = SerializeCheckFromData(check)
			if err != nil {
				t.Fatal(err)
			}
			fields["Sequence"] = uint32(2)
			for _, name := range []string{"SourceTag", "DestinationTag", "InvoiceID"} {
				delete(fields, name)
			}
			want, err := binarycodec.EncodeBytes(fields)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, encoded) {
				t.Fatalf("mutation changed untouched fields: want %X, got %X", want, encoded)
			}
		})
	}
}

func TestCheckTypedAdapterPreservesEmptyAccounts(t *testing.T) {
	for _, address := range []string{"", EncodeAccountIDSafe([20]byte{})} {
		t.Run(address, func(t *testing.T) {
			raw, err := binarycodec.EncodeBytes(map[string]any{
				"LedgerEntryType": "Check", "Account": address, "Destination": address,
				"SendMax": "0", "Sequence": uint32(1), "OwnerNode": "0", "DestinationNode": "0", "Flags": uint32(0),
				"PreviousTxnID": zeroHash256, "PreviousTxnLgrSeq": uint32(0),
			})
			if err != nil {
				t.Fatal(err)
			}
			check, err := ParseCheck(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := SerializeCheckFromData(check)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, got) {
				t.Fatalf("account representation changed: want %X, got %X", raw, got)
			}
		})
	}
}
