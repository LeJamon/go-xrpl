package did_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
)

// TestSetInvalid tests invalid DIDSet scenarios.
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
	jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// no fields
	tx2 := did.DIDSet(alice).Build()
	result = env.Submit(tx2)
	jtx.RequireTxFail(t, result, "temEMPTY_DID")
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// all empty fields
	tx3 := did.DIDSet(alice).URI("").Document("").Data("").Build()
	result = env.Submit(tx3)
	jtx.RequireTxFail(t, result, "temEMPTY_DID")
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// uri is too long
	longString := strings.Repeat("a", 257)
	tx4 := did.DIDSet(alice).URI(longString).Build()
	result = env.Submit(tx4)
	jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// document is too long
	tx5 := did.DIDSet(alice).Document(longString).Build()
	result = env.Submit(tx5)
	jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// attestation (data) is too long
	tx6 := did.DIDSet(alice).Document("data").Data(longString).Build()
	result = env.Submit(tx6)
	jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// some empty fields, some optional fields
	// An empty-only DID is accepted only when fixEmptyDID is disabled.
	tx7 := did.DIDSet(alice).URI("").Build()
	result = env.Submit(tx7)
	if fixEmptyDID {
		jtx.RequireTxClaimed(t, result, "tecEMPTY_DID")
	} else {
		if !result.Success {
			t.Errorf("Expected tesSUCCESS (fixEmptyDID disabled), got %s", result.Code)
		}
	}
	env.Close()
	if fixEmptyDID {
		requireDIDAbsent(t, env, alice)
		require.Zero(t, env.OwnerCount(alice))
	} else {
		view := getDIDEntry(t, env, alice)
		require.NotNil(t, view)
		requireFieldAbsent(t, "URI", view.URI)
		requireFieldAbsent(t, "DIDDocument", view.DIDDocument)
		requireFieldAbsent(t, "Data", view.Data)
		require.Equal(t, uint32(1), env.OwnerCount(alice))
	}
}

// TestDeleteInvalid tests invalid DIDDelete scenarios.
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
	jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// ---- doApply ----

	// DID doesn't exist
	tx2 := did.DIDDelete(alice).Build()
	result = env.Submit(tx2)
	jtx.RequireTxClaimed(t, result, jtx.TecNO_ENTRY)
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}
}
