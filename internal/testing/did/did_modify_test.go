package did_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
)

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

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Create DID: expected DID entry to exist")
		}
		requireFieldPresent(t, "URI", entry.URI)
		checkVL(t, "URI", entry.URI, initialURI)
		requireFieldAbsent(t, "DIDDocument", entry.DIDDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

	{
		tx1 := did.DIDSet(alice).URI("").Build()
		result := env.Submit(tx1)
		jtx.RequireTxClaimed(t, result, "tecEMPTY_DID")
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Delete URI: expected owner count 1, got %d", env.OwnerCount(alice))
		}

		entry := getDIDEntry(t, env, alice)
		if entry == nil {
			t.Fatal("Delete URI: expected DID entry to still exist")
		}
		checkVL(t, "URI", entry.URI, initialURI)
		requireFieldAbsent(t, "DIDDocument", entry.DIDDocument)
		requireFieldAbsent(t, "Data", entry.Data)
	}

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

	{
		tx1 := did.DIDDelete(alice).Build()
		result := env.Submit(tx1)
		if !result.Success {
			t.Fatalf("Delete DID: expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 0 {
			t.Errorf("Delete DID: expected owner count 0, got %d", env.OwnerCount(alice))
		}

		requireDIDAbsent(t, env, alice)
	}
}
