package credential_test

// Credentials_test.go - Tests for Credential transactions
// Reference: rippled/src/test/app/Credentials_test.cpp

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/credential"
	acctx "github.com/LeJamon/goXRPLd/internal/tx/account"
	credtx "github.com/LeJamon/goXRPLd/internal/tx/credential"
	"github.com/LeJamon/goXRPLd/keylet"
)

const rippleEpoch = 946684800

// xrpAccount is the XRPL zero account address (20 bytes of zero).
// This matches rippled's xrpAccount() / noAccount().
const xrpAccount = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// credentialKeylet computes the keylet for a credential given subject, issuer, and raw credential type.
func credentialKeylet(subject, issuer *jtx.Account, credType string) keylet.Keylet {
	return keylet.Credential(subject.ID, issuer.ID, []byte(credType))
}

// rippleTime returns the current Ripple epoch time from the test environment.
func rippleTime(env *jtx.TestEnv) uint32 {
	return uint32(env.Now().Unix() - rippleEpoch)
}

// TestSuccessful tests the basic credential lifecycle: create, accept, delete.
// Reference: rippled Credentials_test.cpp testSuccessful
func TestSuccessful(t *testing.T) {
	credType := "abcde"
	uri := "uri"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	credKey := credentialKeylet(subject, issuer, credType)

	// Test: Create credential for subject
	t.Run("CreateForSubject", func(t *testing.T) {
		tx := credential.CredentialCreate(issuer, subject, credType).URI(uri).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Verify credential exists in ledger
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential ledger entry to exist")
		}

		// Verify issuer owner count increased, subject still 0
		if env.OwnerCount(issuer) != 1 {
			t.Errorf("Expected issuer owner count 1, got %d", env.OwnerCount(issuer))
		}
		if env.OwnerCount(subject) != 0 {
			t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
		}
	})

	// Test: Accept credential
	t.Run("AcceptCredential", func(t *testing.T) {
		tx := credential.CredentialAccept(subject, issuer, credType).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Credential should still exist
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential ledger entry to exist after accept")
		}

		// After accept, ownership transfers from issuer to subject
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}
		if env.OwnerCount(subject) != 1 {
			t.Errorf("Expected subject owner count 1, got %d", env.OwnerCount(subject))
		}
	})

	// Test: Delete credential by subject
	t.Run("DeleteCredential", func(t *testing.T) {
		tx := credential.CredentialDelete(subject, subject, issuer, credType).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Credential should no longer exist
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential ledger entry to NOT exist after delete")
		}

		// Verify owner counts reset to 0
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}
		if env.OwnerCount(subject) != 0 {
			t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
		}
	})
}

// TestCreateForSelf tests issuing a credential to yourself.
// Reference: rippled Credentials_test.cpp testSuccessful (Create for themself)
func TestCreateForSelf(t *testing.T) {
	credType := "abcde"
	uri := "uri"

	issuer := jtx.NewAccount("issuer")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer)
	env.Close()

	credKey := credentialKeylet(issuer, issuer, credType)

	// Test: Create credential for self (issuer == subject)
	t.Run("CreateForSelf", func(t *testing.T) {
		tx := credential.CredentialCreate(issuer, issuer, credType).URI(uri).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Credential should exist
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential ledger entry to exist")
		}

		// Self-issued credentials are auto-accepted, owner count = 1
		if env.OwnerCount(issuer) != 1 {
			t.Errorf("Expected issuer owner count 1, got %d", env.OwnerCount(issuer))
		}
	})

	// Test: Delete self-issued credential
	t.Run("DeleteSelfCredential", func(t *testing.T) {
		tx := credential.CredentialDelete(issuer, issuer, issuer, credType).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Credential should no longer exist
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential ledger entry to NOT exist after delete")
		}

		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}
	})
}

// TestCredentialsDelete tests various credential deletion scenarios.
// Reference: rippled Credentials_test.cpp testCredentialsDelete
func TestCredentialsDelete(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	other := jtx.NewAccount("other")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject, other)
	env.Close()

	// Reference: rippled testCredentialsDelete "Delete by other"
	// Third party can delete expired credentials.
	t.Run("DeleteByOther", func(t *testing.T) {
		ct := "delother"
		// Create credential with near-future expiration
		now := rippleTime(env)
		tx := credential.CredentialCreate(issuer, subject, ct).
			Expiration(now + 20).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s: %s", result.Code, result.Message)
		}

		// Advance time well past expiration
		env.AdvanceTime(60 * time.Second)
		env.Close()
		env.Close()
		env.Close()

		// Other account can delete expired credentials
		deleteTx := credential.CredentialDelete(other, subject, issuer, ct).Build()
		result = env.Submit(deleteTx)
		if !result.Success {
			t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Verify credential deleted and owner counts reset
		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to be deleted")
		}
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}
		if env.OwnerCount(subject) != 0 {
			t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
		}
	})

	// Reference: rippled testCredentialsDelete "Delete by subject"
	t.Run("DeleteBySubject", func(t *testing.T) {
		ct := "delsubj"
		createTx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		deleteTx := credential.CredentialDelete(subject, subject, issuer, ct).Build()
		result = env.Submit(deleteTx)
		if !result.Success {
			t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to be deleted")
		}
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}
		if env.OwnerCount(subject) != 0 {
			t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
		}
	})

	// Reference: rippled testCredentialsDelete "Delete by issuer"
	t.Run("DeleteByIssuer", func(t *testing.T) {
		ct := "deliss"
		createTx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		deleteTx := credential.CredentialDelete(issuer, subject, issuer, ct).Build()
		result = env.Submit(deleteTx)
		if !result.Success {
			t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to be deleted")
		}
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}
		if env.OwnerCount(subject) != 0 {
			t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
		}
	})

	// Reference: rippled testCredentialsDelete "Delete issuer before accept"
	// AccountDelete must cascade-delete owned objects (credentials).
	// In rippled, deleting the issuer before accept removes the credential and
	// resets owner counts. Go's AccountDelete does not cascade-delete directory entries.
	t.Run("DeleteIssuerBeforeAccept", func(t *testing.T) {
		ct := "delibacc"
		createTx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		// Delete issuer account — rippled cascade-deletes the credential
		env.IncLedgerSeqForAccDel(issuer)
		acctDel := acctx.NewAccountDelete(issuer.Address, other.Address)
		acctDel.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result = env.Submit(acctDel)
		if !result.Success {
			t.Fatalf("AccountDelete expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Credential should be cleaned up by cascade delete
		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to be deleted when issuer account is deleted")
		}
		if env.OwnerCount(subject) != 0 {
			t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
		}

		// Re-fund issuer for subsequent tests
		env.Fund(issuer)
		env.Close()
	})

	// Reference: rippled testCredentialsDelete "Delete issuer after accept"
	// Create credential, accept it, then delete the issuer account.
	// Rippled cascade-deletes the credential (now owned by subject) and resets subject's owner count.
	t.Run("DeleteIssuerAfterAccept", func(t *testing.T) {
		ct := "deliaaft"
		createTx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		acceptTx := credential.CredentialAccept(subject, issuer, ct).Build()
		result = env.Submit(acceptTx)
		if !result.Success {
			t.Fatalf("Accept expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Delete issuer account — rippled cascade-deletes the credential
		env.IncLedgerSeqForAccDel(issuer)
		acctDel := acctx.NewAccountDelete(issuer.Address, other.Address)
		acctDel.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result = env.Submit(acctDel)
		if !result.Success {
			t.Fatalf("AccountDelete expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to be deleted when issuer account is deleted")
		}
		if env.OwnerCount(subject) != 0 {
			t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
		}

		// Re-fund issuer for subsequent tests
		env.Fund(issuer)
		env.Close()
	})

	// Reference: rippled testCredentialsDelete "Delete subject before accept"
	// Create credential, then delete the subject account before accepting.
	// Rippled cascade-deletes the credential and resets issuer's owner count.
	t.Run("DeleteSubjectBeforeAccept", func(t *testing.T) {
		ct := "delsbfr"
		createTx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		// Delete subject account — rippled cascade-deletes the credential
		env.IncLedgerSeqForAccDel(subject)
		acctDel := acctx.NewAccountDelete(subject.Address, other.Address)
		acctDel.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result = env.Submit(acctDel)
		if !result.Success {
			t.Fatalf("AccountDelete expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to be deleted when subject account is deleted")
		}
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}

		// Re-fund subject for subsequent tests
		env.Fund(subject)
		env.Close()
	})

	// Reference: rippled testCredentialsDelete "Delete subject after accept"
	// Create credential, accept it, then delete the subject account.
	// Rippled cascade-deletes the credential (now owned by subject) and resets issuer's owner count.
	t.Run("DeleteSubjectAfterAccept", func(t *testing.T) {
		ct := "delsaft"
		createTx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		acceptTx := credential.CredentialAccept(subject, issuer, ct).Build()
		result = env.Submit(acceptTx)
		if !result.Success {
			t.Fatalf("Accept expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Delete subject account — rippled cascade-deletes the credential
		env.IncLedgerSeqForAccDel(subject)
		acctDel := acctx.NewAccountDelete(subject.Address, other.Address)
		acctDel.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result = env.Submit(acctDel)
		if !result.Success {
			t.Fatalf("AccountDelete expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to be deleted when subject account is deleted")
		}
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}

		// Re-fund subject for subsequent tests
		env.Fund(subject)
		env.Close()
	})
}

// TestCreateFailed tests CredentialCreate validation failures.
// Reference: rippled Credentials_test.cpp testCreateFailed
func TestCreateFailed(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	// Reference: rippled "Credentials fail, no subject param."
	// Removing Subject field maps to empty string in Go.
	t.Run("MissingSubject", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		cc := credtx.NewCredentialCreate(issuer.Address, "", credTypeHex)
		cc.Fee = "10"
		result := env.Submit(cc)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
	})

	// Reference: rippled "Subject set to xrpAccount()"
	// In rippled this returns temMALFORMED from preflight.
	t.Run("InvalidSubjectZeroAccount", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		cc := credtx.NewCredentialCreate(issuer.Address, xrpAccount, credTypeHex)
		cc.Fee = "10"
		result := env.Submit(cc)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s (rippled rejects zero account in preflight)", result.Code)
		}
	})

	// Reference: rippled "Credentials fail, no credentialType param."
	t.Run("MissingCredentialType", func(t *testing.T) {
		cc := credtx.NewCredentialCreate(issuer.Address, subject.Address, "")
		cc.Fee = "10"
		result := env.Submit(cc)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
	})

	// Reference: rippled "Credentials fail, empty credentialType param."
	t.Run("EmptyCredentialType", func(t *testing.T) {
		tx := credential.CredentialCreate(issuer, subject, "").Build()
		result := env.Submit(tx)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
	})

	// Reference: rippled "Credentials fail, credentialType length > maxCredentialTypeLength."
	t.Run("CredentialTypeTooLong", func(t *testing.T) {
		longCredType := strings.Repeat("a", 65)
		tx := credential.CredentialCreate(issuer, subject, longCredType).Build()
		result := env.Submit(tx)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
	})

	// Reference: rippled "Credentials fail, URI length > 256."
	t.Run("URITooLong", func(t *testing.T) {
		longURI := strings.Repeat("a", 257)
		tx := credential.CredentialCreate(issuer, subject, credType).URI(longURI).Build()
		result := env.Submit(tx)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
	})

	// Reference: rippled "Credentials fail, URI empty."
	// An explicitly present but zero-length URI should fail with temMALFORMED.
	t.Run("EmptyURI", func(t *testing.T) {
		// Construct directly to set a present-but-empty URI field
		credTypeHex := hex.EncodeToString([]byte(credType))
		cc := credtx.NewCredentialCreate(issuer.Address, subject.Address, credTypeHex)
		cc.Fee = "10"
		// Set URI to a valid hex string that decodes to empty bytes
		// The builder's URIHex("") results in no URI field; we need an explicitly empty VL.
		// In rippled, credentials::uri("") sets the field to an empty blob.
		// In Go, an empty string URI is treated as absent. We need to test the
		// case where the URI hex is set but decodes to zero-length.
		// Setting URI to "" won't trigger the check since omitempty skips it.
		// This matches the Go behavior where absent URI is valid.
		// When URI field IS present but empty, it should fail.
		cc.URI = "00" // A 1-byte URI is valid, but truly empty ("") is absent
		// For the actual empty-URI case, we need the hex to decode to empty
		// The Go validation checks: len(decoded) == 0 → ErrCredentialURIEmpty
		// But cc.URI = "" would skip the check due to `if c.URI != ""`
		// This is a difference from rippled where the field can be present-but-empty.
		// Test what we can: a present but 0-decoded URI
		t.Log("Go treats empty URI string as absent (not present), which is valid. " +
			"Rippled can have a present-but-empty VL field which fails with temMALFORMED.")
	})

	// Reference: rippled "Credentials fail, expiration in the past."
	t.Run("ExpirationInPast", func(t *testing.T) {
		now := rippleTime(env)
		tx := credential.CredentialCreate(issuer, subject, credType).
			Expiration(now - 1).Build()
		result := env.Submit(tx)
		if result.Code != "tecEXPIRED" {
			t.Errorf("Expected tecEXPIRED, got %s", result.Code)
		}
		env.Close()
	})

	// Reference: rippled "Credentials fail, invalid fee."
	// In rippled, fee=-1 triggers temBAD_FEE. Go uses string fee field;
	// a negative fee string should be rejected during validation.
	t.Run("InvalidFee", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		cc := credtx.NewCredentialCreate(issuer.Address, subject.Address, credTypeHex)
		cc.Fee = "-1"
		result := env.Submit(cc)
		if result.Code != "temBAD_FEE" {
			t.Errorf("Expected temBAD_FEE for negative fee, got %s", result.Code)
		}
	})

	// Reference: rippled "Credentials fail, duplicate."
	t.Run("Duplicate", func(t *testing.T) {
		// First create should succeed
		tx1 := credential.CredentialCreate(issuer, subject, credType).Build()
		result1 := env.Submit(tx1)
		if !result1.Success {
			t.Fatalf("First create expected success, got %s", result1.Code)
		}
		env.Close()

		// Second create should fail with tecDUPLICATE
		tx2 := credential.CredentialCreate(issuer, subject, credType).Build()
		result2 := env.Submit(tx2)
		if result2.Code != "tecDUPLICATE" {
			t.Errorf("Expected tecDUPLICATE, got %s", result2.Code)
		}
		env.Close()

		// Verify credential still present after failed duplicate attempt
		credKey := credentialKeylet(subject, issuer, credType)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist after failed duplicate create")
		}

		// Cleanup
		deleteTx := credential.CredentialDelete(issuer, subject, issuer, credType).Build()
		env.Submit(deleteTx)
		env.Close()
	})

	// Reference: rippled "Credentials fail, subject doesn't exist."
	t.Run("SubjectDoesNotExist", func(t *testing.T) {
		nonExistent := jtx.NewAccount("nonexistent")
		// Do NOT fund nonExistent — it should not exist in the ledger
		tx := credential.CredentialCreate(issuer, nonExistent, credType).Build()
		result := env.Submit(tx)
		if result.Code != "tecNO_TARGET" {
			t.Errorf("Expected tecNO_TARGET, got %s", result.Code)
		}
		env.Close()
	})

	// Test: Invalid flags
	// Reference: rippled testFlags with fixInvalidTxFlags enabled
	t.Run("InvalidFlags", func(t *testing.T) {
		tx := credential.CredentialCreate(issuer, subject, credType).Flags(0x00010000).Build()
		result := env.Submit(tx)
		if result.Code != "temINVALID_FLAG" {
			t.Errorf("Expected temINVALID_FLAG, got %s", result.Code)
		}
	})
}

// TestCreateReserve tests that creating credentials requires reserve.
// Reference: rippled Credentials_test.cpp testCreateFailed (not enough reserve)
func TestCreateReserve(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)

	// Fund accounts at exactly account reserve (not enough for credential)
	acctReserve := env.ReserveBase()
	env.FundAmount(issuer, acctReserve)
	env.FundAmount(subject, acctReserve)
	env.Close()

	// Create should fail with insufficient reserve
	tx := credential.CredentialCreate(issuer, subject, credType).Build()
	result := env.Submit(tx)
	if result.Code != "tecINSUFFICIENT_RESERVE" {
		t.Errorf("Expected tecINSUFFICIENT_RESERVE, got %s", result.Code)
	}
	env.Close()
}

// TestAcceptFailed tests CredentialAccept validation failures.
// Reference: rippled Credentials_test.cpp testAcceptFailed
func TestAcceptFailed(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	// Reference: rippled "CredentialsAccept fail, Credential doesn't exist."
	t.Run("CredentialNotExist", func(t *testing.T) {
		tx := credential.CredentialAccept(subject, issuer, credType).Build()
		result := env.Submit(tx)
		if result.Code != "tecNO_ENTRY" {
			t.Errorf("Expected tecNO_ENTRY, got %s", result.Code)
		}
		env.Close()
	})

	// Reference: rippled "CredentialsAccept fail, invalid Issuer account."
	t.Run("InvalidIssuerZeroAccount", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		ca := credtx.NewCredentialAccept(subject.Address, xrpAccount, credTypeHex)
		ca.Fee = "10"
		result := env.Submit(ca)
		if result.Code != "temINVALID_ACCOUNT_ID" {
			t.Errorf("Expected temINVALID_ACCOUNT_ID, got %s (rippled rejects zero account in preflight)", result.Code)
		}
	})

	// Reference: rippled "CredentialsAccept fail, invalid credentialType param."
	t.Run("EmptyCredentialType", func(t *testing.T) {
		tx := credential.CredentialAccept(subject, issuer, "").Build()
		result := env.Submit(tx)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
	})

	// Test: Invalid flags
	t.Run("InvalidFlags", func(t *testing.T) {
		tx := credential.CredentialAccept(subject, issuer, credType).Flags(0x00010000).Build()
		result := env.Submit(tx)
		if result.Code != "temINVALID_FLAG" {
			t.Errorf("Expected temINVALID_FLAG, got %s", result.Code)
		}
	})

	// Reference: rippled "CredentialsAccept fail, invalid fee."
	// In rippled, fee=-1 triggers temBAD_FEE.
	t.Run("InvalidFee", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		ca := credtx.NewCredentialAccept(subject.Address, issuer.Address, credTypeHex)
		ca.Fee = "-1"
		result := env.Submit(ca)
		if result.Code != "temBAD_FEE" {
			t.Errorf("Expected temBAD_FEE for negative fee, got %s", result.Code)
		}
	})

	// Reference: rippled "CredentialsAccept fail, lsfAccepted already set."
	t.Run("AlreadyAccepted", func(t *testing.T) {
		// Create and accept
		createTx := credential.CredentialCreate(issuer, subject, credType).Build()
		env.Submit(createTx)
		env.Close()

		acceptTx := credential.CredentialAccept(subject, issuer, credType).Build()
		result1 := env.Submit(acceptTx)
		if !result1.Success {
			t.Fatalf("First accept expected success, got %s", result1.Code)
		}
		env.Close()

		// Try to accept again - should fail with tecDUPLICATE
		acceptTx2 := credential.CredentialAccept(subject, issuer, credType).Build()
		result2 := env.Submit(acceptTx2)
		if result2.Code != "tecDUPLICATE" {
			t.Errorf("Expected tecDUPLICATE, got %s", result2.Code)
		}
		env.Close()

		// Verify credential still present after failed re-accept
		credKey := credentialKeylet(subject, issuer, credType)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist after failed re-accept")
		}

		// Cleanup
		deleteTx := credential.CredentialDelete(subject, subject, issuer, credType).Build()
		env.Submit(deleteTx)
		env.Close()
	})

	// Reference: rippled "CredentialsAccept fail, expired credentials."
	// When accepting expired credentials, the credential is auto-deleted and tecEXPIRED returned.
	t.Run("ExpiredCredentials", func(t *testing.T) {
		credType2 := "efghi"

		// Create credential with expiration at current time.
		// In rippled, setting expiration to parentCloseTime and then closing one ledger
		// makes the credential expired on the next operation.
		now := rippleTime(env)
		tx := credential.CredentialCreate(issuer, subject, credType2).
			Expiration(now).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s: %s", result.Code, result.Message)
		}
		// Close advances clock by 10s, making parentCloseTime > expiration
		env.Close()

		// Credentials are now expired
		acceptTx := credential.CredentialAccept(subject, issuer, credType2).Build()
		result = env.Submit(acceptTx)
		if result.Code != "tecEXPIRED" {
			t.Errorf("Expected tecEXPIRED, got %s", result.Code)
		}
		env.Close()

		// Verify that expired credentials were auto-deleted
		credKey := credentialKeylet(subject, issuer, credType2)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected expired credential to be auto-deleted on failed accept")
		}

		// Issuer owner count should be 0 (expired credential was cleaned up)
		if env.OwnerCount(issuer) != 0 {
			t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
		}
	})

	// Reference: rippled "CredentialsAccept fail, issuer doesn't exist."
	// Create credential, delete the issuer account, then try to accept.
	// Rippled cascade-deletes the credential on account deletion, so the accept
	// should fail. In rippled this returns tecNO_ISSUER.
	t.Run("IssuerDoesNotExist", func(t *testing.T) {
		ct := "noiss"
		createTx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		// Delete issuer account
		env.IncLedgerSeqForAccDel(issuer)
		acctDel := acctx.NewAccountDelete(issuer.Address, subject.Address)
		acctDel.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result = env.Submit(acctDel)
		if !result.Success {
			t.Fatalf("AccountDelete expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Try to accept — issuer no longer exists
		acceptTx := credential.CredentialAccept(subject, issuer, ct).Build()
		result = env.Submit(acceptTx)
		if result.Code != "tecNO_ISSUER" {
			t.Errorf("Expected tecNO_ISSUER, got %s", result.Code)
		}
		env.Close()

		// Credential should have been cleaned up when issuer was deleted
		credKey := credentialKeylet(subject, issuer, ct)
		if env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to not exist after issuer account deletion")
		}

		// Re-fund issuer for other tests
		env.Fund(issuer)
		env.Close()
	})
}

// TestAcceptReserve tests that accepting credentials requires reserve for subject.
// Reference: rippled Credentials_test.cpp testAcceptFailed (not enough reserve)
func TestAcceptReserve(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)

	// Fund issuer with enough for 1 object, subject at just account reserve
	acctReserve := env.ReserveBase()
	incReserve := env.ReserveIncrement()
	env.FundAmount(issuer, acctReserve+incReserve)
	env.FundAmount(subject, acctReserve)
	env.Close()

	// Create credential should succeed (issuer has reserve)
	createTx := credential.CredentialCreate(issuer, subject, credType).Build()
	result := env.Submit(createTx)
	if !result.Success {
		t.Fatalf("Create expected success, got %s", result.Code)
	}
	env.Close()

	// Accept should fail - subject doesn't have reserve
	acceptTx := credential.CredentialAccept(subject, issuer, credType).Build()
	result = env.Submit(acceptTx)
	if result.Code != "tecINSUFFICIENT_RESERVE" {
		t.Errorf("Expected tecINSUFFICIENT_RESERVE, got %s", result.Code)
	}
	env.Close()

	// Verify credential still present after failed accept
	credKey := credentialKeylet(subject, issuer, credType)
	if !env.LedgerEntryExists(credKey) {
		t.Error("Expected credential to still exist after failed accept")
	}

	// Cleanup by issuer
	deleteTx := credential.CredentialDelete(issuer, subject, issuer, credType).Build()
	env.Submit(deleteTx)
	env.Close()
}

// TestDeleteFailed tests CredentialDelete validation failures.
// Reference: rippled Credentials_test.cpp testDeleteFailed
func TestDeleteFailed(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	other := jtx.NewAccount("other")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject, other)
	env.Close()

	// Reference: rippled "CredentialsDelete fail, no Credentials."
	t.Run("CredentialNotExist", func(t *testing.T) {
		tx := credential.CredentialDelete(subject, subject, issuer, credType).Build()
		result := env.Submit(tx)
		if result.Code != "tecNO_ENTRY" {
			t.Errorf("Expected tecNO_ENTRY, got %s", result.Code)
		}
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid Subject account."
	t.Run("InvalidSubjectZeroAccount", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		cd.Subject = xrpAccount
		cd.Issuer = issuer.Address
		cd.Fee = "10"
		result := env.Submit(cd)
		if result.Code != "temINVALID_ACCOUNT_ID" {
			t.Errorf("Expected temINVALID_ACCOUNT_ID, got %s (rippled rejects zero account in preflight)", result.Code)
		}
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid Issuer account."
	t.Run("InvalidIssuerZeroAccount", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		cd.Subject = subject.Address
		cd.Issuer = xrpAccount
		cd.Fee = "10"
		result := env.Submit(cd)
		if result.Code != "temINVALID_ACCOUNT_ID" {
			t.Errorf("Expected temINVALID_ACCOUNT_ID, got %s (rippled rejects zero account in preflight)", result.Code)
		}
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid credentialType param."
	t.Run("EmptyCredentialType", func(t *testing.T) {
		tx := credential.CredentialDelete(subject, subject, issuer, "").Build()
		result := env.Submit(tx)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
	})

	// Test: Invalid flags
	t.Run("InvalidFlags", func(t *testing.T) {
		tx := credential.CredentialDelete(subject, subject, issuer, credType).Flags(0x00010000).Build()
		result := env.Submit(tx)
		if result.Code != "temINVALID_FLAG" {
			t.Errorf("Expected temINVALID_FLAG, got %s", result.Code)
		}
	})

	// Reference: rippled "Other account can't delete credentials without expiration"
	t.Run("NoPermission", func(t *testing.T) {
		credType2 := "fghij"

		// Create credential without expiration
		createTx := credential.CredentialCreate(issuer, subject, credType2).Build()
		env.Submit(createTx)
		env.Close()

		// Other account tries to delete - should fail
		deleteTx := credential.CredentialDelete(other, subject, issuer, credType2).Build()
		result := env.Submit(deleteTx)
		if result.Code != "tecNO_PERMISSION" {
			t.Errorf("Expected tecNO_PERMISSION, got %s", result.Code)
		}
		env.Close()

		// Verify credential still present after failed delete
		credKey := credentialKeylet(subject, issuer, credType2)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist after failed delete by other")
		}

		// Cleanup by issuer
		cleanupTx := credential.CredentialDelete(issuer, subject, issuer, credType2).Build()
		env.Submit(cleanupTx)
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, time not expired yet."
	// Credential has an expiration but it hasn't passed yet — other can't delete.
	t.Run("TimeNotExpiredYet", func(t *testing.T) {
		now := rippleTime(env)
		// Create credential with expiration far in the future
		tx := credential.CredentialCreate(issuer, subject, credType).
			Expiration(now + 1000).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Other account can't delete credentials that are not yet expired
		deleteTx := credential.CredentialDelete(other, subject, issuer, credType).Build()
		result = env.Submit(deleteTx)
		if result.Code != "tecNO_PERMISSION" {
			t.Errorf("Expected tecNO_PERMISSION, got %s", result.Code)
		}
		env.Close()

		// Verify credential still present
		credKey := credentialKeylet(subject, issuer, credType)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist (not yet expired)")
		}

		// Cleanup by issuer
		cleanupTx := credential.CredentialDelete(issuer, subject, issuer, credType).Build()
		env.Submit(cleanupTx)
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, no Issuer and Subject."
	t.Run("MissingBothSubjectAndIssuer", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		// Leave both Subject and Issuer empty
		cd.Subject = ""
		cd.Issuer = ""
		cd.Fee = "10"
		result := env.Submit(cd)
		if result.Code != "temMALFORMED" {
			t.Errorf("Expected temMALFORMED, got %s", result.Code)
		}
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid fee."
	// In rippled, fee=-1 triggers temBAD_FEE.
	t.Run("InvalidFee", func(t *testing.T) {
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		cd.Subject = subject.Address
		cd.Issuer = issuer.Address
		cd.Fee = "-1"
		result := env.Submit(cd)
		if result.Code != "temBAD_FEE" {
			t.Errorf("Expected temBAD_FEE for negative fee, got %s", result.Code)
		}
	})
}

// TestDeleteBySubject tests that the subject can delete a credential.
// Reference: rippled Credentials_test.cpp testCredentialsDelete (Delete by subject)
func TestDeleteBySubject(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	// Create credential
	createTx := credential.CredentialCreate(issuer, subject, credType).Build()
	result := env.Submit(createTx)
	if !result.Success {
		t.Fatalf("Create expected success, got %s", result.Code)
	}
	env.Close()

	// Subject can delete (even before accepting)
	deleteTx := credential.CredentialDelete(subject, subject, issuer, credType).Build()
	result = env.Submit(deleteTx)
	if !result.Success {
		t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
	}
	env.Close()

	// Verify credential gone and owner counts zero
	credKey := credentialKeylet(subject, issuer, credType)
	if env.LedgerEntryExists(credKey) {
		t.Error("Expected credential to be deleted")
	}
	if env.OwnerCount(issuer) != 0 {
		t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
	}
	if env.OwnerCount(subject) != 0 {
		t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
	}
}

// TestDeleteByIssuer tests that the issuer can delete a credential.
// Reference: rippled Credentials_test.cpp testCredentialsDelete (Delete by issuer)
func TestDeleteByIssuer(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	// Create credential
	createTx := credential.CredentialCreate(issuer, subject, credType).Build()
	result := env.Submit(createTx)
	if !result.Success {
		t.Fatalf("Create expected success, got %s", result.Code)
	}
	env.Close()

	// Issuer can delete
	deleteTx := credential.CredentialDelete(issuer, subject, issuer, credType).Build()
	result = env.Submit(deleteTx)
	if !result.Success {
		t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
	}
	env.Close()

	// Verify credential gone and owner counts zero
	credKey := credentialKeylet(subject, issuer, credType)
	if env.LedgerEntryExists(credKey) {
		t.Error("Expected credential to be deleted")
	}
	if env.OwnerCount(issuer) != 0 {
		t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
	}
	if env.OwnerCount(subject) != 0 {
		t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
	}
}

// TestCredentialTypeLimits tests the exact limits of CredentialType.
func TestCredentialTypeLimits(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	// Test: CredentialType at exactly 64 bytes should succeed
	t.Run("CredentialTypeAtLimit", func(t *testing.T) {
		credType64 := strings.Repeat("a", 64)
		tx := credential.CredentialCreate(issuer, subject, credType64).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Errorf("Expected success with 64-byte CredentialType, got %s", result.Code)
		}
		env.Close()

		// Cleanup
		deleteTx := credential.CredentialDelete(issuer, subject, issuer, credType64).Build()
		env.Submit(deleteTx)
		env.Close()
	})

	// Test: URI at exactly 256 bytes should succeed
	t.Run("URIAtLimit", func(t *testing.T) {
		credType := "testcred"
		uri256 := strings.Repeat("b", 256)
		tx := credential.CredentialCreate(issuer, subject, credType).URI(uri256).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Errorf("Expected success with 256-byte URI, got %s", result.Code)
		}
		env.Close()

		// Cleanup
		deleteTx := credential.CredentialDelete(issuer, subject, issuer, credType).Build()
		env.Submit(deleteTx)
		env.Close()
	})
}

// TestEnabled tests that credential transactions are disabled without the amendment.
// Reference: rippled Credentials_test.cpp testFeatureFailed
func TestEnabled(t *testing.T) {
	credType := "abcde"

	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	// Disable the Credentials amendment
	env.DisableFeature("Credentials")

	// Without the featureCredentials amendment, all credential transactions should fail
	t.Run("CreateDisabled", func(t *testing.T) {
		tx := credential.CredentialCreate(issuer, subject, credType).Build()
		result := env.Submit(tx)
		if result.Code != "temDISABLED" {
			t.Errorf("Expected temDISABLED, got %s", result.Code)
		}
	})

	t.Run("AcceptDisabled", func(t *testing.T) {
		tx := credential.CredentialAccept(subject, issuer, credType).Build()
		result := env.Submit(tx)
		if result.Code != "temDISABLED" {
			t.Errorf("Expected temDISABLED, got %s", result.Code)
		}
	})

	t.Run("DeleteDisabled", func(t *testing.T) {
		tx := credential.CredentialDelete(subject, subject, issuer, credType).Build()
		result := env.Submit(tx)
		if result.Code != "temDISABLED" {
			t.Errorf("Expected temDISABLED, got %s", result.Code)
		}
	})
}

// TestMultipleCredentials tests that accounts can have multiple credentials.
func TestMultipleCredentials(t *testing.T) {
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")

	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()

	credTypes := []string{"type1", "type2", "type3"}

	// Create multiple credentials
	for _, ct := range credTypes {
		tx := credential.CredentialCreate(issuer, subject, ct).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Create %s expected success, got %s", ct, result.Code)
		}
	}
	env.Close()

	// Verify issuer has 3 objects
	if env.OwnerCount(issuer) != 3 {
		t.Errorf("Expected issuer owner count 3, got %d", env.OwnerCount(issuer))
	}

	// Accept all
	for _, ct := range credTypes {
		tx := credential.CredentialAccept(subject, issuer, ct).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Accept %s expected success, got %s", ct, result.Code)
		}
	}
	env.Close()

	// Verify subject now has all 3, issuer has 0
	if env.OwnerCount(subject) != 3 {
		t.Errorf("Expected subject owner count 3, got %d", env.OwnerCount(subject))
	}
	if env.OwnerCount(issuer) != 0 {
		t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
	}

	// Delete all
	for _, ct := range credTypes {
		tx := credential.CredentialDelete(subject, subject, issuer, ct).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Delete %s expected success, got %s", ct, result.Code)
		}
	}
	env.Close()

	// Verify both have 0
	if env.OwnerCount(subject) != 0 {
		t.Errorf("Expected subject owner count 0, got %d", env.OwnerCount(subject))
	}
	if env.OwnerCount(issuer) != 0 {
		t.Errorf("Expected issuer owner count 0, got %d", env.OwnerCount(issuer))
	}
}
