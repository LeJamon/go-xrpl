package did_test

// DID_test.go - Tests for DID (Decentralized Identifier) transactions
// Reference: rippled/src/test/app/DID_test.cpp
//
// Rippled runs each test with two feature sets:
//   1. all amendments enabled
//   2. all amendments enabled EXCEPT fixEmptyDID
// We mirror this via runWithFeatureSets which runs each test function twice.

import (
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/did"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ---- helpers (matching rippled's checkVL and field checks) ----

// didEntry holds the decoded fields of a DID ledger entry.
// Fields are hex-encoded strings as returned by the binary codec.
type didEntry struct {
	URI         string // hex-encoded, "" if absent
	DIDDocument string // hex-encoded, "" if absent
	Data        string // hex-encoded, "" if absent
}

// getDIDEntry reads and parses the DID ledger entry for the given account
// using the binary codec (same approach as rippled's env.le()).
// Returns nil if the DID does not exist.
func getDIDEntry(t *testing.T, env *jtx.TestEnv, account *jtx.Account) *didEntry {
	t.Helper()
	key := keylet.DID(account.ID)
	data, err := env.LedgerEntry(key)
	if err != nil || data == nil {
		return nil
	}
	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		t.Fatalf("Failed to decode DID entry via binary codec: %v", err)
	}
	entry := &didEntry{}
	if uri, ok := jsonObj["URI"].(string); ok {
		entry.URI = uri
	}
	if doc, ok := jsonObj["DIDDocument"].(string); ok {
		entry.DIDDocument = doc
	}
	if d, ok := jsonObj["Data"].(string); ok {
		entry.Data = d
	}
	return entry
}

// checkVL verifies that a DID field value (hex-encoded in the ledger)
// matches the expected plain-text string.
// Reference: rippled DID_test.cpp checkVL()
func checkVL(t *testing.T, fieldName, hexValue, expected string) {
	t.Helper()
	// Binary codec returns uppercase hex; normalise for decode.
	decoded, err := hex.DecodeString(strings.ToLower(hexValue))
	if err != nil {
		t.Fatalf("Failed to decode %s hex value %q: %v", fieldName, hexValue, err)
	}
	if string(decoded) != expected {
		t.Errorf("%s mismatch: got %q, want %q", fieldName, string(decoded), expected)
	}
}

// requireFieldPresent checks that a DID field is set (non-empty hex string).
func requireFieldPresent(t *testing.T, fieldName, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("Expected %s to be present, but it is absent", fieldName)
	}
}

// requireFieldAbsent checks that a DID field is not set (empty string).
func requireFieldAbsent(t *testing.T, fieldName, value string) {
	t.Helper()
	if value != "" {
		t.Errorf("Expected %s to be absent, but got %q", fieldName, value)
	}
}

// setupEnv creates a TestEnv with the correct feature set.
// When fixEmptyDID is false, the fixEmptyDID amendment is disabled.
func setupEnv(t *testing.T, fixEmptyDID bool) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	if !fixEmptyDID {
		env.DisableFeature("fixEmptyDID")
	}
	return env
}

// runWithFeatureSets runs a test function with both feature set variants:
//   - "AllFeatures": all amendments enabled (including fixEmptyDID)
//   - "WithoutFixEmptyDID": all amendments except fixEmptyDID
//
// Reference: rippled DID_test.cpp run() calls each test with `all` and `all - emptyDID`.
func runWithFeatureSets(t *testing.T, testFn func(t *testing.T, fixEmptyDID bool)) {
	t.Run("AllFeatures", func(t *testing.T) {
		testFn(t, true)
	})
	t.Run("WithoutFixEmptyDID", func(t *testing.T) {
		testFn(t, false)
	})
}

// ---- tests matching rippled DID_test.cpp ----

// TestEnabled tests that DID transactions are disabled without the featureDID amendment.
// Reference: rippled DID_test.cpp testEnabled
func TestEnabled(t *testing.T) {
	runWithFeatureSets(t, testEnabled)
}

func testEnabled(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	// If the DID amendment is not enabled, you should not be able
	// to set or delete DIDs.
	env.DisableFeature("DID")

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// DIDSet should return temDISABLED
	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	if result.Code != "temDISABLED" {
		t.Errorf("Expected temDISABLED for DIDSet, got %s", result.Code)
	}
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after failed DIDSet, got %d", env.OwnerCount(alice))
	}

	// DIDDelete should return temDISABLED
	tx2 := did.DIDDelete(alice).Build()
	result = env.Submit(tx2)
	if result.Code != "temDISABLED" {
		t.Errorf("Expected temDISABLED for DIDDelete, got %s", result.Code)
	}
	env.Close()
}

// TestAccountReserve tests that the reserve behaves as expected for DID creation.
// Reference: rippled DID_test.cpp testAccountReserve
func TestAccountReserve(t *testing.T) {
	runWithFeatureSets(t, testAccountReserve)
}

func testAccountReserve(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")

	// Fund alice enough to exist, but not enough to meet
	// the reserve for creating a DID.
	acctReserve := env.ReserveBase()
	incReserve := env.ReserveIncrement()
	baseFee := env.BaseFee()

	env.FundAmount(alice, acctReserve)
	env.Close()

	balance := env.Balance(alice)
	if balance != acctReserve {
		t.Logf("Expected balance %d, got %d", acctReserve, balance)
	}
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// alice does not have enough XRP to cover the reserve for a DID
	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	if result.Code != "tecINSUFFICIENT_RESERVE" {
		t.Errorf("Expected tecINSUFFICIENT_RESERVE, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// Pay alice almost enough to make the reserve for a DID.
	master := env.MasterAccount()
	payTx := payment.Pay(master, alice, incReserve+2*baseFee-1).Build()
	env.Submit(payTx)
	env.Close()

	// alice still does not have enough XRP for the reserve of a DID.
	tx2 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx2)
	if result.Code != "tecINSUFFICIENT_RESERVE" {
		t.Errorf("Expected tecINSUFFICIENT_RESERVE, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// Pay alice enough to make the reserve for a DID.
	payTx2 := payment.Pay(master, alice, baseFee+1).Build()
	env.Submit(payTx2)
	env.Close()

	// Now alice can create a DID.
	tx3 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx3)
	if !result.Success {
		t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
	}
	env.Close()
	if env.OwnerCount(alice) != 1 {
		t.Errorf("Expected owner count 1, got %d", env.OwnerCount(alice))
	}

	// alice deletes her DID.
	tx4 := did.DIDDelete(alice).Build()
	result = env.Submit(tx4)
	if !result.Success {
		t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
	}
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after delete, got %d", env.OwnerCount(alice))
	}
	env.Close()
}

// TestSetInvalid tests invalid DIDSet scenarios.
// Reference: rippled DID_test.cpp testSetInvalid
func TestSetInvalid(t *testing.T) {
	runWithFeatureSets(t, testSetInvalid)
}

func testSetInvalid(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// ---- preflight ----

	// invalid flags
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}
	tx1 := did.DIDSet(alice).URI("uri").Flags(0x00010000).Build()
	result := env.Submit(tx1)
	if result.Code != "temINVALID_FLAG" {
		t.Errorf("Expected temINVALID_FLAG, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// no fields
	tx2 := did.DIDSet(alice).Build()
	result = env.Submit(tx2)
	if result.Code != "temEMPTY_DID" {
		t.Errorf("Expected temEMPTY_DID, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// all empty fields
	tx3 := did.DIDSet(alice).URI("").Document("").Data("").Build()
	result = env.Submit(tx3)
	if result.Code != "temEMPTY_DID" {
		t.Errorf("Expected temEMPTY_DID, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// uri is too long
	longString := strings.Repeat("a", 257)
	tx4 := did.DIDSet(alice).URI(longString).Build()
	result = env.Submit(tx4)
	if result.Code != "temMALFORMED" {
		t.Errorf("Expected temMALFORMED for long URI, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// document is too long
	tx5 := did.DIDSet(alice).Document(longString).Build()
	result = env.Submit(tx5)
	if result.Code != "temMALFORMED" {
		t.Errorf("Expected temMALFORMED for long document, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// attestation (data) is too long
	tx6 := did.DIDSet(alice).Document("data").Data(longString).Build()
	result = env.Submit(tx6)
	if result.Code != "temMALFORMED" {
		t.Errorf("Expected temMALFORMED for long data, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// some empty fields, some optional fields
	// pre-fix amendment: without fixEmptyDID, creating a DID with only empty URI succeeds (bug)
	// post-fix: returns tecEMPTY_DID
	tx7 := did.DIDSet(alice).URI("").Build()
	result = env.Submit(tx7)
	if fixEmptyDID {
		if result.Code != "tecEMPTY_DID" {
			t.Errorf("Expected tecEMPTY_DID (fixEmptyDID enabled), got %s", result.Code)
		}
	} else {
		if !result.Success {
			t.Errorf("Expected tesSUCCESS (fixEmptyDID disabled), got %s", result.Code)
		}
	}
	env.Close()

	expectedOwnerCount := uint32(0)
	if !fixEmptyDID {
		expectedOwnerCount = 1
	}
	if env.OwnerCount(alice) != expectedOwnerCount {
		t.Errorf("Expected owner count %d, got %d", expectedOwnerCount, env.OwnerCount(alice))
	}
}

// TestDeleteInvalid tests invalid DIDDelete scenarios.
// Reference: rippled DID_test.cpp testDeleteInvalid
func TestDeleteInvalid(t *testing.T) {
	runWithFeatureSets(t, testDeleteInvalid)
}

func testDeleteInvalid(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// ---- preflight ----

	// invalid flags
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}
	tx1 := did.DIDDelete(alice).Flags(0x00010000).Build()
	result := env.Submit(tx1)
	if result.Code != "temINVALID_FLAG" {
		t.Errorf("Expected temINVALID_FLAG, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// ---- doApply ----

	// DID doesn't exist
	tx2 := did.DIDDelete(alice).Build()
	result = env.Submit(tx2)
	if result.Code != "tecNO_ENTRY" {
		t.Errorf("Expected tecNO_ENTRY, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}
}

// TestSetValidInitial tests valid initial DIDSet transactions with various field combinations.
// Reference: rippled DID_test.cpp testSetValidInitial
// Note: rippled defines this test but does not call it from run(). We include it for completeness.
func TestSetValidInitial(t *testing.T) {
	runWithFeatureSets(t, testSetValidInitial)
}

func testSetValidInitial(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	charlie := jtx.NewAccount("charlie")
	dave := jtx.NewAccount("dave")
	edna := jtx.NewAccount("edna")
	francis := jtx.NewAccount("francis")
	george := jtx.NewAccount("george")

	env.Fund(alice, bob, charlie, dave, edna, francis)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 for alice, got %d", env.OwnerCount(alice))
	}
	if env.OwnerCount(bob) != 0 {
		t.Errorf("Expected owner count 0 for bob, got %d", env.OwnerCount(bob))
	}
	if env.OwnerCount(charlie) != 0 {
		t.Errorf("Expected owner count 0 for charlie, got %d", env.OwnerCount(charlie))
	}

	// only URI
	{
		tx1 := did.DIDSet(alice).URI("uri").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Errorf("Only URI: expected success, got %s", result.Code)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Only URI: expected owner count 1, got %d", env.OwnerCount(alice))
		}
	}

	// only DIDDocument
	{
		tx1 := did.DIDSet(bob).Document("data").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Errorf("Only Document: expected success, got %s", result.Code)
		}
		if env.OwnerCount(bob) != 1 {
			t.Errorf("Only Document: expected owner count 1, got %d", env.OwnerCount(bob))
		}
	}

	// only Data
	{
		tx1 := did.DIDSet(charlie).Data("data").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Errorf("Only Data: expected success, got %s", result.Code)
		}
		if env.OwnerCount(charlie) != 1 {
			t.Errorf("Only Data: expected owner count 1, got %d", env.OwnerCount(charlie))
		}
	}

	// URI + Data
	{
		tx1 := did.DIDSet(dave).URI("uri").Data("attest").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Errorf("URI+Data: expected success, got %s", result.Code)
		}
		if env.OwnerCount(dave) != 1 {
			t.Errorf("URI+Data: expected owner count 1, got %d", env.OwnerCount(dave))
		}
	}

	// URI + DIDDocument
	{
		tx1 := did.DIDSet(edna).URI("uri").Document("data").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Errorf("URI+Document: expected success, got %s", result.Code)
		}
		if env.OwnerCount(edna) != 1 {
			t.Errorf("URI+Document: expected owner count 1, got %d", env.OwnerCount(edna))
		}
	}

	// DIDDocument + Data
	{
		tx1 := did.DIDSet(francis).Document("data").Data("attest").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Errorf("Document+Data: expected success, got %s", result.Code)
		}
		if env.OwnerCount(francis) != 1 {
			t.Errorf("Document+Data: expected owner count 1, got %d", env.OwnerCount(francis))
		}
	}

	// URI + DIDDocument + Data
	// Note: george is not funded in rippled (line 230), but the test still
	// calls did::set(george). We fund george to avoid tecNO_ENTRY.
	env.Fund(george)
	env.Close()
	{
		tx1 := did.DIDSet(george).URI("uri").Document("data").Data("attest").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Errorf("All fields: expected success, got %s", result.Code)
		}
		if env.OwnerCount(george) != 1 {
			t.Errorf("All fields: expected owner count 1, got %d", env.OwnerCount(george))
		}
	}
}

// TestSetModify tests modifying an existing DID with DIDSet.
// Reference: rippled DID_test.cpp testSetModify
func TestSetModify(t *testing.T) {
	runWithFeatureSets(t, testSetModify)
}

func testSetModify(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// Create DID with only URI
	initialURI := "uri"
	{
		tx1 := did.DIDSet(alice).URI(initialURI).Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Create DID: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Create DID: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		// Verify DID entry fields
		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Create DID: expected DID entry to exist")
		}
		requireFieldPresent(t, "URI", entry.URI)
		checkVL(t, "URI", entry.URI, initialURI)
		requireFieldAbsent(t, "DIDDocument", entry.DIDDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

	// Try to delete URI, fails because no elements would remain
	{
		tx1 := did.DIDSet(alice).URI("").Build()
		result := env.Submit(tx1)
		if result.Code != "tecEMPTY_DID" {
			t.Errorf("Delete URI: expected tecEMPTY_DID, got %s", result.Code)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Delete URI: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		// DID should remain unchanged
		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Delete URI: expected DID entry to still exist")
		}
		checkVL(t, "URI", entry.URI, initialURI)
		requireFieldAbsent(t, "DIDDocument", entry.DIDDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

	// Set DIDDocument
	initialDocument := "data"
	{
		tx1 := did.DIDSet(alice).Document(initialDocument).Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Set Document: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Set Document: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Set Document: expected DID entry to exist")
		}
		checkVL(t, "URI", entry.URI, initialURI)
		checkVL(t, "DIDDocument", entry.DIDDocument, initialDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

	// Set Data
	initialData := "attest"
	{
		tx1 := did.DIDSet(alice).Data(initialData).Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Set Data: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Set Data: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Set Data: expected DID entry to exist")
		}
		checkVL(t, "URI", entry.URI, initialURI)
		checkVL(t, "DIDDocument", entry.DIDDocument, initialDocument)
		checkVL(t, "Data", entry.Data, initialData)
	}

	// Remove URI
	{
		tx1 := did.DIDSet(alice).URI("").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Remove URI: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Remove URI: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Remove URI: expected DID entry to exist")
		}
		requireFieldAbsent(t, "URI", entry.URI)
		checkVL(t, "DIDDocument", entry.DIDDocument, initialDocument)
		checkVL(t, "Data", entry.Data, initialData)
	}

	// Remove Data
	{
		tx1 := did.DIDSet(alice).Data("").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Remove Data: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Remove Data: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Remove Data: expected DID entry to exist")
		}
		requireFieldAbsent(t, "URI", entry.URI)
		checkVL(t, "DIDDocument", entry.DIDDocument, initialDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

	// Remove DIDDocument + set URI
	secondURI := "uri2"
	{
		tx1 := did.DIDSet(alice).URI(secondURI).Document("").Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Remove Doc + Set URI: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Remove Doc + Set URI: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Remove Doc + Set URI: expected DID entry to exist")
		}
		checkVL(t, "URI", entry.URI, secondURI)
		requireFieldAbsent(t, "DIDDocument", entry.DIDDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

	// Remove URI + set DIDDocument
	secondDocument := "data2"
	{
		tx1 := did.DIDSet(alice).URI("").Document(secondDocument).Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Remove URI + Set Doc: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Remove URI + Set Doc: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Remove URI + Set Doc: expected DID entry to exist")
		}
		requireFieldAbsent(t, "URI", entry.URI)
		checkVL(t, "DIDDocument", entry.DIDDocument, secondDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

	// Remove DIDDocument + set Data
	secondData := "randomData"
	{
		tx1 := did.DIDSet(alice).Document("").Data(secondData).Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Remove Doc + Set Data: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Remove Doc + Set Data: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Remove Doc + Set Data: expected DID entry to exist")
		}
		requireFieldAbsent(t, "URI", entry.URI)
		requireFieldAbsent(t, "DIDDocument", entry.DIDDocument)
		checkVL(t, "Data", entry.Data, secondData)
	}

	// Delete DID
	{
		tx1 := did.DIDDelete(alice).Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Delete DID: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 0 {
			t.Errorf("Delete DID: expected owner count 0, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry != nil {
			t.Error("Delete DID: expected DID entry to not exist")
		}
	}
}
