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

func TestTypedVector256AccessorPreservesPresenceAndCopies(t *testing.T) {
	var directory DirectoryNode
	if got, err := directory.GetIndexes(); err != nil || got != nil {
		t.Fatalf("absent Indexes = %#v, %v; want nil, nil", got, err)
	}

	value := Vector256Value{{0x00, 0x11, 0x22, 0x33}}
	directory.SetIndexesValue(value)
	value[0][0] = 0xFF
	got, err := directory.GetIndexes()
	if err != nil {
		t.Fatalf("GetIndexes: %v", err)
	}
	if got[0][0] != 0x00 {
		t.Fatalf("SetIndexesValue retained caller alias: %#x", got[0][0])
	}
	got[0][1] = 0xEE
	again, err := directory.GetIndexes()
	if err != nil {
		t.Fatalf("GetIndexes after mutation: %v", err)
	}
	if again[0][1] != 0x11 {
		t.Fatalf("GetIndexes retained returned-slice alias: %#x", again[0][1])
	}

	directory.SetIndexesValue(Vector256Value{})
	if !directory.HasIndexes() {
		t.Fatal("empty Vector256 value lost explicit field presence")
	}
	if got, err := directory.GetIndexes(); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("explicit empty Indexes = %#v, %v; want non-nil empty vector", got, err)
	}

	directory.Indexes = []string{"not-a-hash"}
	if _, err := directory.GetIndexes(); err == nil {
		t.Fatal("GetIndexes accepted malformed hash")
	}
}

func TestTypedIssueNumberBridgeAndNestedAccessors(t *testing.T) {
	iou := IssueValue{Currency: "USD", Issuer: "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"}
	var amm AMM
	if err := amm.SetAssetValue(iou); err != nil {
		t.Fatalf("SetAssetValue: %v", err)
	}
	gotIssue, err := amm.GetAsset()
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if !reflect.DeepEqual(gotIssue, iou) {
		t.Fatalf("GetAsset = %#v, want %#v", gotIssue, iou)
	}
	amm.Asset = map[string]any{"currency": "USD"}
	if _, err := amm.GetAsset(); err == nil {
		t.Fatal("GetAsset accepted issued currency without issuer")
	}

	var vault Vault
	if got, err := vault.GetAssetsTotal(); err != nil || got != NumberValue("0") {
		t.Fatalf("absent Number = %q, %v; want 0, nil", got, err)
	}
	if err := vault.SetAssetsTotalValue(NumberValue("123.4500")); err != nil {
		t.Fatalf("SetAssetsTotalValue: %v", err)
	}
	if got, err := vault.GetAssetsTotal(); err != nil || got != NumberValue("123.4500") {
		t.Fatalf("Number round-trip = %q, %v", got, err)
	}

	bridgeValue := XChainBridgeValue{
		LockingChainDoor:  [20]byte{1},
		LockingChainIssue: IssueValue{Currency: "XRP"},
		IssuingChainDoor:  [20]byte{2},
		IssuingChainIssue: iou,
	}
	var bridge Bridge
	if err := bridge.SetXChainBridgeValue(bridgeValue); err != nil {
		t.Fatalf("SetXChainBridgeValue: %v", err)
	}
	gotBridge, err := bridge.GetXChainBridge()
	if err != nil {
		t.Fatalf("GetXChainBridge: %v", err)
	}
	if !reflect.DeepEqual(gotBridge, bridgeValue) {
		t.Fatalf("GetXChainBridge = %#v, want %#v", gotBridge, bridgeValue)
	}
}

func TestNestedWrapperPresenceAndCopySemantics(t *testing.T) {
	var credential CredentialValue
	credentialType := []byte{0x01, 0x02, 0x03}
	credential.SetCredentialType(credentialType)
	credentialType[0] = 0xFF
	gotType, err := credential.GetCredentialType()
	if err != nil || !bytes.Equal(gotType, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("CredentialType after input mutation = %X, %v", gotType, err)
	}
	gotType[1] = 0xEE
	againType, err := credential.GetCredentialType()
	if err != nil || !bytes.Equal(againType, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("CredentialType after output mutation = %X, %v", againType, err)
	}

	var slot AuctionSlotValue
	if err := slot.SetAccount([20]byte{4}); err != nil {
		t.Fatalf("SetAccount: %v", err)
	}
	slot.SetExpiration(10)
	if err := slot.SetPrice(AmountValue{Value: "1"}); err != nil {
		t.Fatalf("SetPrice: %v", err)
	}
	if err := slot.SetAuthAccountsValue(nil); err != nil {
		t.Fatalf("SetAuthAccountsValue(nil): %v", err)
	}
	if !slot.HasAuthAccounts() {
		t.Fatal("SetAuthAccountsValue(nil) lost explicit presence")
	}
	serialized, err := slot.ToMap()
	if err != nil {
		t.Fatalf("ToMap: %v", err)
	}
	if _, ok := serialized["AuthAccounts"]; !ok {
		t.Fatalf("ToMap omitted present empty AuthAccounts: %#v", serialized)
	}

	copySlot := slot
	copySlot.SetExpiration(11)
	if got, _ := slot.GetExpiration(); got != 10 {
		t.Fatalf("copy mutation changed original wrapper: %d", got)
	}

	var amm AMM
	amm.SetAuctionSlot(map[string]any{
		"Account":    "",
		"Expiration": uint32(1),
		"Price":      "1",
	})
	decodedSlot, err := amm.GetAuctionSlot()
	if err != nil {
		t.Fatalf("GetAuctionSlot(empty account): %v", err)
	}
	mapped, err := decodedSlot.ToMap()
	if err != nil {
		t.Fatalf("empty AuctionSlot ToMap: %v", err)
	}
	if mapped["Account"] != "" {
		t.Fatalf("empty nested Account became %#v", mapped["Account"])
	}

	amm.SetAuctionSlot(map[string]any{
		"Account":    innerTemplateAccount,
		"Expiration": uint32(1),
		"Price":      "1",
		"Unexpected": true,
	})
	if _, err := amm.GetAuctionSlot(); err == nil {
		t.Fatal("GetAuctionSlot accepted an unknown nested field")
	}

	var bridge Bridge
	bridge.SetXChainBridge(map[string]any{
		"LockingChainDoor":  "",
		"LockingChainIssue": map[string]any{"currency": "XRP"},
		"IssuingChainDoor":  "",
		"IssuingChainIssue": map[string]any{"currency": "XRP"},
	})
	decodedBridge, err := bridge.GetXChainBridge()
	if err != nil {
		t.Fatalf("GetXChainBridge(empty doors): %v", err)
	}
	encodedBridge, err := xchainBridgeValueToAny(decodedBridge, "Bridge.XChainBridge")
	if err != nil {
		t.Fatalf("XChainBridge empty-door encode: %v", err)
	}
	bridgeMap, ok := encodedBridge.(map[string]any)
	if !ok || bridgeMap["LockingChainDoor"] != "" || bridgeMap["IssuingChainDoor"] != "" {
		t.Fatalf("empty XChainBridge doors became %#v", encodedBridge)
	}

	var token NFTokenValue
	tokenID := [32]byte{0x01, 0x23, 0x45, 0x67}
	token.SetNFTokenID(tokenID)
	tokenMap, err := token.ToMap()
	if err != nil {
		t.Fatalf("NFToken ToMap: %v", err)
	}
	decodedToken, err := nFTokenValueFromAny(tokenMap, "NFToken")
	if err != nil {
		t.Fatalf("NFToken from map: %v", err)
	}
	gotTokenID, err := decodedToken.GetNFTokenID()
	if err != nil || gotTokenID != tokenID {
		t.Fatalf("NFTokenID round-trip = %X, %v", gotTokenID, err)
	}

	var book BookValue
	bookDirectory := [32]byte{0x89, 0xAB, 0xCD, 0xEF}
	book.SetBookDirectory(bookDirectory)
	book.SetBookNode(0x1234)
	bookMap, err := book.ToMap()
	if err != nil {
		t.Fatalf("Book ToMap: %v", err)
	}
	decodedBook, err := bookValueFromAny(bookMap, "Book")
	if err != nil {
		t.Fatalf("Book from map: %v", err)
	}
	gotBookDirectory, err := decodedBook.GetBookDirectory()
	if err != nil || gotBookDirectory != bookDirectory {
		t.Fatalf("BookDirectory round-trip = %X, %v", gotBookDirectory, err)
	}
}

func TestNestedAccountAddressAccessorsPreserveEmptyValues(t *testing.T) {
	var signer SignerEntryValue
	const account = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	if err := signer.SetAccountAddress(account); err != nil {
		t.Fatalf("SetAccountAddress: %v", err)
	}
	got, err := signer.GetAccountAddress()
	if err != nil || got != account {
		t.Fatalf("GetAccountAddress = %q, %v; want %q", got, err, account)
	}

	var empty AuctionSlotValue
	if err := empty.SetAccountAddress(""); err != nil {
		t.Fatalf("SetAccountAddress(empty): %v", err)
	}
	got, err = empty.GetAccountAddress()
	if err != nil || got != "" {
		t.Fatalf("GetAccountAddress(empty) = %q, %v; want empty address", got, err)
	}
	mapped, err := empty.ToMap()
	if err != nil {
		t.Fatalf("empty account ToMap: %v", err)
	}
	if mapped["Account"] != "" {
		t.Fatalf("empty account ToMap = %#v; want empty Account", mapped["Account"])
	}
}
