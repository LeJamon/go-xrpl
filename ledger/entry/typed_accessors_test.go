package entry

import (
	"bytes"
	"reflect"
	"testing"
)

func TestAccountRootTypedAccessors(t *testing.T) {
	var account AccountRoot
	account.SetAccount("rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn")
	account.SetBalance("0")
	account.SetDomain("ABC")
	account.SetEmailHash("00112233445566778899AABBCCDDEEFF")
	account.SetTickSize(7)
	account.SetAccountTxnID("00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF")
	account.SetSponsor("r3kmLJN5D28dHuH8vZNUZpMC43pEHpaocV")

	balance, err := account.GetBalance()
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if balance != (AmountValue{Value: "0"}) {
		t.Fatalf("GetBalance = %#v", balance)
	}
	if !account.HasBalance() || !account.HasSponsor() {
		t.Fatal("required or optional presence was not recorded")
	}

	domain, err := account.GetDomain()
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if !bytes.Equal(domain, []byte{0x0A, 0xBC}) {
		t.Fatalf("GetDomain = %X, want 0ABC", domain)
	}

	emailHash, err := account.GetEmailHash()
	if err != nil {
		t.Fatalf("GetEmailHash: %v", err)
	}
	if emailHash[0] != 0x00 || emailHash[15] != 0xFF {
		t.Fatalf("GetEmailHash = %X", emailHash)
	}

	tickSize, err := account.GetTickSize()
	if err != nil {
		t.Fatalf("GetTickSize: %v", err)
	}
	if tickSize != 7 {
		t.Fatalf("GetTickSize = %d, want 7", tickSize)
	}

	accountID, err := account.GetAccount()
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if accountID == [20]byte{} {
		t.Fatal("GetAccount returned zero account ID")
	}

	accountTxnID, err := account.GetAccountTxnID()
	if err != nil {
		t.Fatalf("GetAccountTxnID: %v", err)
	}
	if accountTxnID[0] != 0x00 || accountTxnID[31] != 0xFF {
		t.Fatalf("GetAccountTxnID = %X", accountTxnID)
	}

	account.ClearSponsor()
	if account.HasSponsor() || account.Sponsor != "" {
		t.Fatal("ClearSponsor left sponsor present")
	}
	if err := account.SetSponsorValue([20]byte{}); err != nil {
		t.Fatalf("SetSponsorValue(zero): %v", err)
	}
	if !account.HasSponsor() || account.Sponsor == "" {
		t.Fatal("SetSponsorValue(zero) lost explicit zero AccountID presence")
	}
	if !account.HasDomain() {
		t.Fatal("clearing one optional field changed another field's presence")
	}
}

func TestTypedAmountAccessors(t *testing.T) {
	var offer Offer
	iou := AmountValue{
		Value:    "1000000000000000e-1",
		Currency: "USD",
		Issuer:   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
	}
	if err := offer.SetTakerPaysValue(iou); err != nil {
		t.Fatalf("SetTakerPaysValue(IOU): %v", err)
	}
	got, err := offer.GetTakerPays()
	if err != nil {
		t.Fatalf("GetTakerPays(IOU): %v", err)
	}
	if !reflect.DeepEqual(got, iou) {
		t.Fatalf("GetTakerPays(IOU) = %#v, want %#v", got, iou)
	}

	mpt := AmountValue{
		Value:         "-1000000",
		MPTIssuanceID: "1234567890abcdef1234567890abcdef1234567890abcdef",
	}
	if err := offer.SetTakerPaysValue(mpt); err != nil {
		t.Fatalf("SetTakerPaysValue(MPT): %v", err)
	}
	got, err = offer.GetTakerPays()
	if err != nil {
		t.Fatalf("GetTakerPays(MPT): %v", err)
	}
	if !reflect.DeepEqual(got, mpt) {
		t.Fatalf("GetTakerPays(MPT) = %#v, want %#v", got, mpt)
	}

	if err := offer.SetTakerPaysValue(AmountValue{Value: "1", Currency: "USD"}); err == nil {
		t.Fatal("SetTakerPaysValue accepted an issued amount without issuer")
	}
	if err := offer.SetTakerPaysValue(AmountValue{
		Value:         "1",
		Issuer:        "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
		MPTIssuanceID: "1234567890abcdef1234567890abcdef1234567890abcdef",
	}); err == nil {
		t.Fatal("SetTakerPaysValue accepted conflicting MPT identity")
	}
	for name, malformed := range map[string]any{
		"empty MPT ID": map[string]any{
			"value":           "1",
			"mpt_issuance_id": "",
		},
		"empty IOU currency": map[string]any{
			"value":    "1",
			"currency": "",
			"issuer":   iou.Issuer,
		},
		"empty IOU issuer": map[string]any{
			"value":    "1",
			"currency": iou.Currency,
			"issuer":   "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			offer.TakerPays = malformed
			if _, err := offer.GetTakerPays(); err == nil {
				t.Fatal("GetTakerPays accepted an empty asset identity")
			}
		})
	}
}

func TestTypedUInt64Accessors(t *testing.T) {
	var offer Offer
	offer.SetOwnerNode("1a")
	value, err := offer.GetOwnerNode()
	if err != nil {
		t.Fatalf("GetOwnerNode: %v", err)
	}
	if value != 0x1a {
		t.Fatalf("GetOwnerNode = %d, want 26", value)
	}
	offer.SetOwnerNodeValue(42)
	if offer.OwnerNode != "2a" {
		t.Fatalf("SetOwnerNodeValue stored %q, want 2a", offer.OwnerNode)
	}
}

func TestTypedGettersReturnConversionErrors(t *testing.T) {
	var account AccountRoot
	account.AccountTxnID = "not-hex"
	if _, err := account.GetAccountTxnID(); err == nil {
		t.Fatal("GetAccountTxnID accepted malformed hex")
	}
	account.TickSize = 256
	if _, err := account.GetTickSize(); err == nil {
		t.Fatal("GetTickSize accepted out-of-range value")
	}
}

func TestPresenceClearAndMetadataSemantics(t *testing.T) {
	var current AccountRoot
	current.SetTickSize(0)
	if !current.HasTickSize() {
		t.Fatal("SetTickSize(0) did not preserve optional-field presence")
	}
	newFields := make(map[string]any)
	current.EmitNewFields(newFields)
	if _, ok := newFields["TickSize"]; ok {
		t.Fatal("NewFields emitted an optional default value")
	}
	finalFields := make(map[string]any)
	current.EmitFinalFields(finalFields)
	if value, ok := finalFields["TickSize"]; !ok || value != 0 {
		t.Fatalf("FinalFields TickSize = %#v, want present zero", value)
	}

	var previous AccountRoot
	previous.SetTickSize(7)
	current.ClearTickSize()
	if current.HasTickSize() {
		t.Fatal("ClearTickSize left the field present")
	}
	previousFields := make(map[string]any)
	current.EmitPreviousFields(&previous, previousFields)
	if value, ok := previousFields["TickSize"]; !ok || value != 7 {
		t.Fatalf("PreviousFields TickSize = %#v, want 7 after clear", value)
	}

	var defaultValue AccountRoot
	defaultValue.SetSponsoredOwnerCount(0)
	if defaultValue.HasSponsoredOwnerCount() {
		t.Fatal("SetSponsoredOwnerCount(0) preserved a StyleDefault field")
	}

	var directory DirectoryNode
	directory.SetRootIndex("1")
	directory.SetIndexes([]string{"00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF"})
	metadata := make(map[string]any)
	directory.EmitFinalFields(metadata)
	if _, ok := metadata["RootIndex"]; !ok {
		t.Fatal("MetaAlways RootIndex was omitted from FinalFields")
	}
	if _, ok := metadata["Indexes"]; ok {
		t.Fatal("MetaNever Indexes was emitted in FinalFields")
	}
}
