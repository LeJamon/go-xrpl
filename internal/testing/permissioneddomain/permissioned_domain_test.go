package permissioneddomain_test

// permissioned_domain_test.go - Tests for PermissionedDomain transactions
// Reference: rippled/src/test/app/PermissionedDomains_test.cpp

import (
	"encoding/hex"
	"fmt"
	"testing"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binary-codec"
	"github.com/LeJamon/goXRPLd/keylet"
	acctx "github.com/LeJamon/goXRPLd/internal/core/tx/account"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	pd "github.com/LeJamon/goXRPLd/internal/testing/permissioneddomain"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// credTypeHex returns a hex-encoded credential type of the given byte length.
func credTypeHex(byteLen int) string {
	b := make([]byte, byteLen)
	for i := range b {
		b[i] = 0xAB
	}
	return hex.EncodeToString(b)
}

// domainKeylet computes the PermissionedDomain keylet from owner account and sequence.
// The sequence must be the account's sequence at the time the DomainSet was submitted.
func domainKeylet(owner *jtx.Account, seq uint32) keylet.Keylet {
	return keylet.PermissionedDomain(owner.ID, seq)
}

// getDomainEntry reads and decodes the PermissionedDomain ledger entry.
// Returns nil if the domain does not exist.
func getDomainEntry(t *testing.T, env *jtx.TestEnv, k keylet.Keylet) map[string]any {
	t.Helper()
	data, err := env.LedgerEntry(k)
	if err != nil || data == nil {
		return nil
	}
	jsonObj, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		t.Fatalf("failed to decode PermissionedDomain entry: %v", err)
	}
	return jsonObj
}

// domainIDHex returns the hex-encoded domain ID for a given keylet.
func domainIDHex(k keylet.Keylet) string {
	return hex.EncodeToString(k.Key[:])
}

// TestEnabled verifies PermissionedDomain transactions work when both featurePermissionedDomains
// and featureCredentials are enabled.
// Reference: rippled PermissionedDomains_test.cpp testEnabled()
func TestEnabled(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	// Create a domain
	seq := env.Seq(alice)
	tx1 := pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build()
	result := env.Submit(tx1)
	if !result.Success {
		t.Fatalf("Expected tesSUCCESS, got %s: %s", result.Code, result.Message)
	}
	env.Close()

	if env.OwnerCount(alice) != 1 {
		t.Errorf("Expected OwnerCount 1 after domain creation, got %d", env.OwnerCount(alice))
	}

	dk := domainKeylet(alice, seq)
	entry := getDomainEntry(t, env, dk)
	if entry == nil {
		t.Fatal("Expected PermissionedDomain entry to exist")
	}

	// Delete the domain
	tx2 := pd.DomainDelete(alice, domainIDHex(dk)).Build()
	result = env.Submit(tx2)
	if !result.Success {
		t.Fatalf("Expected tesSUCCESS for delete, got %s: %s", result.Code, result.Message)
	}
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected OwnerCount 0 after delete, got %d", env.OwnerCount(alice))
	}

	if getDomainEntry(t, env, dk) != nil {
		t.Error("Expected domain entry to be deleted")
	}
}

// TestCredentialsDisabled verifies that PermissionedDomainSet requires featureCredentials.
// Reference: rippled PermissionedDomains_test.cpp testCredentialsDisabled()
func TestCredentialsDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("Credentials")

	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	tx1 := pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build()
	result := env.Submit(tx1)
	if result.Code != "temDISABLED" {
		t.Errorf("Expected temDISABLED when Credentials disabled, got %s", result.Code)
	}
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected OwnerCount 0, got %d", env.OwnerCount(alice))
	}
}

// TestDisabled verifies neither transaction works when featurePermissionedDomains is disabled.
// Reference: rippled PermissionedDomains_test.cpp testDisabled()
func TestDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("PermissionedDomains")

	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	// DomainSet should return temDISABLED
	tx1 := pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build()
	result := env.Submit(tx1)
	if result.Code != "temDISABLED" {
		t.Errorf("Expected temDISABLED for DomainSet, got %s", result.Code)
	}
	env.Close()

	// DomainDelete should return temDISABLED
	fakeDomainID := fmt.Sprintf("%062x01", 0)
	tx2 := pd.DomainDelete(alice, fakeDomainID).Build()
	result = env.Submit(tx2)
	if result.Code != "temDISABLED" {
		t.Errorf("Expected temDISABLED for DomainDelete, got %s", result.Code)
	}
	env.Close()
}

// TestBadData verifies validation logic for malformed inputs on domain creation.
// Reference: rippled PermissionedDomains_test.cpp testBadData() (create path)
func TestBadData(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	issuer2 := jtx.NewAccount("issuer2")
	env.Fund(alice, issuer, issuer2)
	env.Close()

	// Empty credentials array → temARRAY_EMPTY
	t.Run("empty credentials", func(t *testing.T) {
		result := env.Submit(pd.DomainSet(alice).Build())
		if result.Code != "temARRAY_EMPTY" {
			t.Errorf("Expected temARRAY_EMPTY, got %s", result.Code)
		}
		env.Close()
	})

	// 11 credentials (exceeds max 10) → temARRAY_TOO_LARGE
	t.Run("too many credentials", func(t *testing.T) {
		b := pd.DomainSet(alice)
		for i := 0; i < 11; i++ {
			b.Credential(issuer, credTypeHex(i+1))
		}
		result := env.Submit(b.Build())
		if result.Code != "temARRAY_TOO_LARGE" {
			t.Errorf("Expected temARRAY_TOO_LARGE for 11 credentials, got %s", result.Code)
		}
		env.Close()
	})

	// Non-existent issuer → tecNO_ISSUER
	t.Run("non-existent issuer", func(t *testing.T) {
		ghost := jtx.NewAccount("ghost") // not funded
		result := env.Submit(pd.DomainSet(alice).Credential(ghost, credTypeHex(10)).Build())
		if result.Code != "tecNO_ISSUER" {
			t.Errorf("Expected tecNO_ISSUER for non-existent issuer, got %s", result.Code)
		}
		env.Close()
	})

	// Bad fee → temBAD_FEE
	// Reference: rippled testBadData fee(1, true) → temBAD_FEE
	t.Run("bad fee", func(t *testing.T) {
		result := env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).BadFee().Build())
		if result.Code != "temBAD_FEE" {
			t.Errorf("Expected temBAD_FEE for negative fee, got %s", result.Code)
		}
		env.Close()
	})

	// Empty CredentialType → temMALFORMED
	t.Run("empty CredentialType", func(t *testing.T) {
		result := env.Submit(pd.DomainSet(alice).Credential(issuer, "").Build())
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED for empty CredentialType, got %s", result.Code)
		}
		env.Close()
	})

	// CredentialType too long (>64 bytes) → temMALFORMED
	t.Run("CredentialType too long", func(t *testing.T) {
		result := env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(65)).Build())
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED for CredentialType too long, got %s", result.Code)
		}
		env.Close()
	})

	// Duplicate credentials → temMALFORMED
	// Reference: rippled testBadData duplicate credentials block
	t.Run("duplicate credentials", func(t *testing.T) {
		b := pd.DomainSet(alice)
		b.Credential(issuer2, credTypeHex(6))
		b.Credential(issuer, credTypeHex(1))
		b.Credential(issuer, credTypeHex(2))
		b.Credential(issuer, credTypeHex(1)) // duplicate
		b.Credential(issuer2, credTypeHex(4))
		result := env.Submit(b.Build())
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED for duplicate credentials, got %s", result.Code)
		}
		env.Close()
	})
}

// TestBadDataUpdate verifies that the same validation rules apply when updating an existing domain.
// Reference: rippled PermissionedDomains_test.cpp testBadData() called with domain argument (update path)
func TestBadDataUpdate(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, issuer)
	env.Close()

	// Create a domain to use as the update target
	seq := env.Seq(alice)
	result := env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build())
	env.Close()
	if !result.Success {
		t.Fatalf("setup failed: %s", result.Code)
	}
	dk := domainKeylet(alice, seq)
	domainID := domainIDHex(dk)

	t.Run("empty credentials on update", func(t *testing.T) {
		result := env.Submit(pd.DomainSet(alice).DomainID(domainID).Build())
		if result.Code != "temARRAY_EMPTY" {
			t.Errorf("Expected temARRAY_EMPTY, got %s", result.Code)
		}
		env.Close()
	})

	t.Run("too many credentials on update", func(t *testing.T) {
		b := pd.DomainSet(alice).DomainID(domainID)
		for i := 0; i < 11; i++ {
			b.Credential(issuer, credTypeHex(i+1))
		}
		result := env.Submit(b.Build())
		if result.Code != "temARRAY_TOO_LARGE" {
			t.Errorf("Expected temARRAY_TOO_LARGE, got %s", result.Code)
		}
		env.Close()
	})

	t.Run("non-existent issuer on update", func(t *testing.T) {
		ghost := jtx.NewAccount("ghost")
		result := env.Submit(pd.DomainSet(alice).DomainID(domainID).Credential(ghost, credTypeHex(10)).Build())
		if result.Code != "tecNO_ISSUER" {
			t.Errorf("Expected tecNO_ISSUER, got %s", result.Code)
		}
		env.Close()
	})

	t.Run("duplicate credentials on update", func(t *testing.T) {
		b := pd.DomainSet(alice).DomainID(domainID)
		b.Credential(issuer, credTypeHex(1))
		b.Credential(issuer, credTypeHex(2))
		b.Credential(issuer, credTypeHex(1)) // duplicate
		result := env.Submit(b.Build())
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED for duplicate on update, got %s", result.Code)
		}
		env.Close()
	})

	// Cleanup
	env.Submit(pd.DomainDelete(alice, domainID).Build())
	env.Close()
}

// TestSet verifies PermissionedDomainSet transaction behavior.
// Reference: rippled PermissionedDomains_test.cpp testSet()
func TestSet(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	issuer1 := jtx.NewAccount("issuer1")
	issuer2 := jtx.NewAccount("issuer2")
	env.Fund(alice, bob, issuer1, issuer2)
	env.Close()

	// Create new domain with single credential; verify stored object fields.
	// Reference: rippled testSet verifies LedgerEntryType, Owner, Sequence
	t.Run("create single credential", func(t *testing.T) {
		seq := env.Seq(alice)
		result := env.Submit(pd.DomainSet(alice).Credential(issuer1, credTypeHex(10)).Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Expected OwnerCount 1, got %d", env.OwnerCount(alice))
		}
		dk := domainKeylet(alice, seq)
		entry := getDomainEntry(t, env, dk)
		if entry == nil {
			t.Fatal("Expected domain to exist")
		}
		// Verify stored object fields match rippled expectations
		if entry["LedgerEntryType"] != "PermissionedDomain" {
			t.Errorf("Expected LedgerEntryType PermissionedDomain, got %v", entry["LedgerEntryType"])
		}
		if entry["Owner"] != alice.Address {
			t.Errorf("Expected Owner %s, got %v", alice.Address, entry["Owner"])
		}
		storedSeq, ok := entry["Sequence"]
		if !ok {
			t.Error("Expected Sequence field in stored object")
		} else {
			var storedSeqUint32 uint32
			switch v := storedSeq.(type) {
			case float64:
				storedSeqUint32 = uint32(v)
			case uint32:
				storedSeqUint32 = v
			}
			if storedSeqUint32 != seq {
				t.Errorf("Expected Sequence %d, got %d", seq, storedSeqUint32)
			}
		}
		env.Submit(pd.DomainDelete(alice, domainIDHex(dk)).Build())
		env.Close()
	})

	// Create domain with longest possible CredentialType (64 bytes)
	t.Run("max CredentialType length", func(t *testing.T) {
		seq := env.Seq(alice)
		result := env.Submit(pd.DomainSet(alice).Credential(issuer1, credTypeHex(64)).Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		dk := domainKeylet(alice, seq)
		env.Submit(pd.DomainDelete(alice, domainIDHex(dk)).Build())
		env.Close()
	})

	// One account can create multiple domains
	t.Run("multiple domains per account", func(t *testing.T) {
		seq1 := env.Seq(alice)
		r1 := env.Submit(pd.DomainSet(alice).Credential(issuer1, credTypeHex(5)).Build())
		env.Close()
		seq2 := env.Seq(alice)
		r2 := env.Submit(pd.DomainSet(alice).Credential(issuer2, credTypeHex(5)).Build())
		env.Close()
		if !r1.Success || !r2.Success {
			t.Fatalf("Both creates should succeed, got %s and %s", r1.Code, r2.Code)
		}
		if env.OwnerCount(alice) != 2 {
			t.Errorf("Expected OwnerCount 2, got %d", env.OwnerCount(alice))
		}
		dk1 := domainKeylet(alice, seq1)
		dk2 := domainKeylet(alice, seq2)
		env.Submit(pd.DomainDelete(alice, domainIDHex(dk1)).Build())
		env.Close()
		env.Submit(pd.DomainDelete(alice, domainIDHex(dk2)).Build())
		env.Close()
	})

	// Create with max credentials (10)
	t.Run("max credentials", func(t *testing.T) {
		b := pd.DomainSet(alice)
		for i := 1; i <= 10; i++ {
			b.Credential(issuer1, credTypeHex(i))
		}
		seq := env.Seq(alice)
		result := env.Submit(b.Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Expected success for 10 credentials, got %s: %s", result.Code, result.Message)
		}
		dk := domainKeylet(alice, seq)
		env.Submit(pd.DomainDelete(alice, domainIDHex(dk)).Build())
		env.Close()
	})

	// Update existing domain with 1 credential
	t.Run("update with 1 credential", func(t *testing.T) {
		seq := env.Seq(alice)
		createResult := env.Submit(pd.DomainSet(alice).Credential(issuer1, credTypeHex(5)).Build())
		env.Close()
		if !createResult.Success {
			t.Fatalf("Create failed: %s", createResult.Code)
		}
		dk := domainKeylet(alice, seq)
		domainID := domainIDHex(dk)

		updateResult := env.Submit(pd.DomainSet(alice).DomainID(domainID).Credential(issuer2, credTypeHex(8)).Build())
		env.Close()
		if !updateResult.Success {
			t.Fatalf("Update failed: %s: %s", updateResult.Code, updateResult.Message)
		}
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Expected OwnerCount 1 after update, got %d", env.OwnerCount(alice))
		}
		env.Submit(pd.DomainDelete(alice, domainID).Build())
		env.Close()
	})

	// Update existing domain with 10 credentials
	t.Run("update with 10 credentials", func(t *testing.T) {
		seq := env.Seq(alice)
		env.Submit(pd.DomainSet(alice).Credential(issuer1, credTypeHex(5)).Build())
		env.Close()
		dk := domainKeylet(alice, seq)
		domainID := domainIDHex(dk)

		b := pd.DomainSet(alice).DomainID(domainID)
		for i := 1; i <= 10; i++ {
			b.Credential(issuer1, credTypeHex(i))
		}
		result := env.Submit(b.Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Update with 10 creds failed: %s", result.Code)
		}
		env.Submit(pd.DomainDelete(alice, domainID).Build())
		env.Close()
	})

	// Prevent update from wrong owner → tecNO_PERMISSION
	t.Run("update by non-owner returns tecNO_PERMISSION", func(t *testing.T) {
		seq := env.Seq(alice)
		env.Submit(pd.DomainSet(alice).Credential(issuer1, credTypeHex(5)).Build())
		env.Close()
		dk := domainKeylet(alice, seq)
		domainID := domainIDHex(dk)

		result := env.Submit(pd.DomainSet(bob).DomainID(domainID).Credential(issuer2, credTypeHex(5)).Build())
		env.Close()
		if result.Code != "tecNO_PERMISSION" {
			t.Errorf("Expected tecNO_PERMISSION, got %s", result.Code)
		}
		env.Submit(pd.DomainDelete(alice, domainID).Build())
		env.Close()
	})

	// Prevent updating zero domain → temMALFORMED
	t.Run("update zero domain returns temMALFORMED", func(t *testing.T) {
		zeroDomain := hex.EncodeToString(make([]byte, 32))
		result := env.Submit(pd.DomainSet(alice).DomainID(zeroDomain).Credential(issuer1, credTypeHex(5)).Build())
		env.Close()
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED for zero DomainID, got %s", result.Code)
		}
	})

	// Prevent updating non-existent domain → tecNO_ENTRY
	t.Run("update non-existent domain returns tecNO_ENTRY", func(t *testing.T) {
		fakeDomain := fmt.Sprintf("%062x01", 0)
		result := env.Submit(pd.DomainSet(alice).DomainID(fakeDomain).Credential(issuer1, credTypeHex(5)).Build())
		env.Close()
		if result.Code != "tecNO_ENTRY" {
			t.Errorf("Expected tecNO_ENTRY, got %s", result.Code)
		}
	})

	// Invalid flags → temINVALID_FLAG
	t.Run("invalid flags returns temINVALID_FLAG", func(t *testing.T) {
		result := env.Submit(pd.DomainSet(alice).Credential(issuer1, credTypeHex(5)).Flags(0x00010000).Build())
		env.Close()
		if result.Code != "temINVALID_FLAG" {
			t.Errorf("Expected temINVALID_FLAG, got %s", result.Code)
		}
	})

	// Credentials are sorted in the stored entry
	t.Run("credentials sorted", func(t *testing.T) {
		b := pd.DomainSet(alice)
		b.Credential(issuer2, credTypeHex(5))
		b.Credential(issuer1, credTypeHex(5))
		seq := env.Seq(alice)
		result := env.Submit(b.Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Expected success, got %s", result.Code)
		}
		dk := domainKeylet(alice, seq)
		entry := getDomainEntry(t, env, dk)
		if entry == nil {
			t.Fatal("Expected domain to exist")
		}
		creds, ok := entry["AcceptedCredentials"].([]any)
		if !ok || len(creds) != 2 {
			t.Fatalf("Expected 2 credentials in entry, got %v", entry["AcceptedCredentials"])
		}
		env.Submit(pd.DomainDelete(alice, domainIDHex(dk)).Build())
		env.Close()
	})

	// Same-issuer different-type credential sorting verification.
	// Reference: rippled testBadData credentialsSame block
	t.Run("same-issuer different-type sorting", func(t *testing.T) {
		// Submit credentials with same issuer in unsorted order; verify stored order
		b := pd.DomainSet(alice)
		b.Credential(issuer1, credTypeHex(9)) // issuer1, credType[9]
		b.Credential(issuer2, credTypeHex(2))
		b.Credential(issuer1, credTypeHex(3)) // issuer1, credType[3] — should come before [9]
		b.Credential(issuer2, credTypeHex(4))
		b.Credential(issuer1, credTypeHex(6)) // issuer1, credType[6] — between [3] and [9]
		seq := env.Seq(alice)
		result := env.Submit(b.Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Expected success, got %s", result.Code)
		}
		dk := domainKeylet(alice, seq)
		entry := getDomainEntry(t, env, dk)
		if entry == nil {
			t.Fatal("Expected domain to exist")
		}
		creds, ok := entry["AcceptedCredentials"].([]any)
		if !ok || len(creds) != 5 {
			t.Fatalf("Expected 5 credentials in stored entry, got %v", entry["AcceptedCredentials"])
		}
		env.Submit(pd.DomainDelete(alice, domainIDHex(dk)).Build())
		env.Close()
	})

	// Account deletion blocked when domains exist → tecHAS_OBLIGATIONS;
	// then succeeds after all domains are deleted.
	// Reference: rippled testSet account deletion block at end of function
	t.Run("account deletion blocked then succeeds", func(t *testing.T) {
		carol := jtx.NewAccount("carol")
		master := env.MasterAccount()
		// Fund carol with enough for base reserve + 1 domain + fees
		env.Submit(payment.Pay(master, carol, env.ReserveBase()+env.ReserveIncrement()+100*env.BaseFee()).Build())
		env.Close()

		seq := env.Seq(carol)
		createResult := env.Submit(pd.DomainSet(carol).Credential(issuer1, credTypeHex(5)).Build())
		env.Close()
		if !createResult.Success {
			t.Fatalf("domain create failed: %s", createResult.Code)
		}
		dk := domainKeylet(carol, seq)

		// Advance ledger far enough for account deletion
		env.IncLedgerSeqForAccDel(carol)

		// AccountDelete should fail with tecHAS_OBLIGATIONS
		delTx := acctx.NewAccountDelete(carol.Address, alice.Address)
		delTx.Fee = fmt.Sprintf("%d", 10)
		result := env.Submit(delTx)
		if result.Code != "tecHAS_OBLIGATIONS" {
			t.Errorf("Expected tecHAS_OBLIGATIONS, got %s", result.Code)
		}
		env.Close()

		// Delete the domain
		env.Submit(pd.DomainDelete(carol, domainIDHex(dk)).Build())
		env.Close()

		// Advance ledger again so deletion is eligible
		env.IncLedgerSeqForAccDel(carol)

		// Now account deletion should succeed
		delTx2 := acctx.NewAccountDelete(carol.Address, alice.Address)
		delTx2.Fee = fmt.Sprintf("%d", 10)
		result2 := env.Submit(delTx2)
		if !result2.Success {
			t.Errorf("Expected account deletion to succeed after domains cleared, got %s: %s", result2.Code, result2.Message)
		}
		env.Close()
	})
}

// TestDelete verifies PermissionedDomainDelete transaction behavior.
// Reference: rippled PermissionedDomains_test.cpp testDelete()
func TestDelete(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	issuer := jtx.NewAccount("issuer")
	env.Fund(alice, bob, issuer)
	env.Close()

	// Setup: create a domain
	setupSeq := env.Seq(alice)
	setupResult := env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build())
	env.Close()
	if !setupResult.Success {
		t.Fatalf("setup domain create failed: %s", setupResult.Code)
	}
	dk := domainKeylet(alice, setupSeq)
	domainID := domainIDHex(dk)

	// Delete domain owned by account
	t.Run("delete own domain", func(t *testing.T) {
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Expected OwnerCount 1, got %d", env.OwnerCount(alice))
		}
		result := env.Submit(pd.DomainDelete(alice, domainID).Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		if env.OwnerCount(alice) != 0 {
			t.Errorf("Expected OwnerCount 0, got %d", env.OwnerCount(alice))
		}
		if getDomainEntry(t, env, dk) != nil {
			t.Error("Expected domain to be deleted")
		}
	})

	// Recreate for remaining sub-tests
	seq2 := env.Seq(alice)
	env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build())
	env.Close()
	dk2 := domainKeylet(alice, seq2)
	domainID2 := domainIDHex(dk2)

	// Bad fee → temBAD_FEE
	// Reference: rippled testDelete fee(1, true) → temBAD_FEE
	t.Run("bad fee", func(t *testing.T) {
		fakeDomain := fmt.Sprintf("%062x01", 0)
		result := env.Submit(pd.DomainDelete(alice, fakeDomain).BadFee().Build())
		env.Close()
		if result.Code != "temBAD_FEE" {
			t.Errorf("Expected temBAD_FEE for negative fee, got %s", result.Code)
		}
	})

	// Prevent deletion by non-owner → tecNO_PERMISSION
	t.Run("non-owner delete returns tecNO_PERMISSION", func(t *testing.T) {
		result := env.Submit(pd.DomainDelete(bob, domainID2).Build())
		env.Close()
		if result.Code != "tecNO_PERMISSION" {
			t.Errorf("Expected tecNO_PERMISSION, got %s", result.Code)
		}
	})

	// Prevent deletion of non-existent domain → tecNO_ENTRY
	t.Run("delete non-existent domain returns tecNO_ENTRY", func(t *testing.T) {
		fakeDomain := fmt.Sprintf("%062x01", 0)
		result := env.Submit(pd.DomainDelete(alice, fakeDomain).Build())
		env.Close()
		if result.Code != "tecNO_ENTRY" {
			t.Errorf("Expected tecNO_ENTRY, got %s", result.Code)
		}
	})

	// Invalid flags → temINVALID_FLAG
	t.Run("invalid flags returns temINVALID_FLAG", func(t *testing.T) {
		result := env.Submit(pd.DomainDelete(alice, domainID2).Flags(0x00010000).Build())
		env.Close()
		if result.Code != "temINVALID_FLAG" {
			t.Errorf("Expected temINVALID_FLAG, got %s", result.Code)
		}
	})

	// Delete zero domain → temMALFORMED
	t.Run("delete zero domain returns temMALFORMED", func(t *testing.T) {
		zeroDomain := hex.EncodeToString(make([]byte, 32))
		result := env.Submit(pd.DomainDelete(alice, zeroDomain).Build())
		env.Close()
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED for zero DomainID, got %s", result.Code)
		}
	})

	// Verify OwnerCount decrements on delete
	t.Run("OwnerCount decrements", func(t *testing.T) {
		if env.OwnerCount(alice) != 1 {
			t.Errorf("Expected OwnerCount 1, got %d", env.OwnerCount(alice))
		}
		result := env.Submit(pd.DomainDelete(alice, domainID2).Build())
		env.Close()
		if !result.Success {
			t.Fatalf("Expected success, got %s", result.Code)
		}
		if env.OwnerCount(alice) != 0 {
			t.Errorf("Expected OwnerCount 0 after delete, got %d", env.OwnerCount(alice))
		}
	})
}

// TestAccountReserve verifies reserve requirement for domain creation.
// Reference: rippled PermissionedDomains_test.cpp testAccountReserve()
func TestAccountReserve(t *testing.T) {
	env := jtx.NewTestEnv(t)

	issuer := jtx.NewAccount("issuer")
	alice := jtx.NewAccount("alice")
	env.Fund(issuer)

	acctReserve := env.ReserveBase()
	incReserve := env.ReserveIncrement()
	baseFee := env.BaseFee()
	master := env.MasterAccount()

	// Fund alice with exactly the base reserve (not enough for a domain)
	env.Submit(payment.Pay(master, alice, acctReserve).Build())
	env.Close()

	// alice does not have enough for the domain reserve
	result := env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build())
	if result.Code != "tecINSUFFICIENT_RESERVE" {
		t.Errorf("Expected tecINSUFFICIENT_RESERVE, got %s", result.Code)
	}
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected OwnerCount 0, got %d", env.OwnerCount(alice))
	}

	// Pay alice almost enough — still one drop short
	env.Submit(payment.Pay(master, alice, incReserve+2*baseFee-1).Build())
	env.Close()

	result = env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build())
	if result.Code != "tecINSUFFICIENT_RESERVE" {
		t.Errorf("Expected tecINSUFFICIENT_RESERVE after partial fund, got %s", result.Code)
	}
	env.Close()

	// Pay alice the remaining amount
	env.Submit(payment.Pay(master, alice, baseFee+1).Build())
	env.Close()

	seq := env.Seq(alice)
	result = env.Submit(pd.DomainSet(alice).Credential(issuer, credTypeHex(10)).Build())
	if !result.Success {
		t.Fatalf("Expected success after sufficient reserve, got %s: %s", result.Code, result.Message)
	}
	env.Close()

	dk := domainKeylet(alice, seq)
	if env.OwnerCount(alice) != 1 {
		t.Errorf("Expected OwnerCount 1, got %d", env.OwnerCount(alice))
	}
	if getDomainEntry(t, env, dk) == nil {
		t.Error("Expected domain entry to exist")
	}
}
