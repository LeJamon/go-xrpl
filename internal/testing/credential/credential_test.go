package credential_test

// Credentials_test.go - Tests for Credential transactions
// Reference: rippled/src/test/app/Credentials_test.cpp

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	acctx "github.com/LeJamon/go-xrpl/internal/tx/account"
	credtx "github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TestSuccessful tests the basic credential lifecycle: create, accept, delete.
// Reference: rippled Credentials_test.cpp testSuccessful
func TestSuccessful(t *testing.T) {
	credType := "abcde"
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	env := jtx.NewTestEnv(t)
	env.Fund(issuer, subject)
	env.Close()
	credKey := jtx.CredentialKeylet(subject, issuer, credType)

	result := env.Submit(credential.CredentialCreateText(issuer, subject, credType).URI("uri").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireLedgerEntryExists(t, env, credKey)
	jtx.RequireOwnerCount(t, env, issuer, 1)
	jtx.RequireOwnerCount(t, env, subject, 0)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, true)

	result = env.Submit(credential.CredentialAcceptText(subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireLedgerEntryExists(t, env, credKey)
	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireOwnerCount(t, env, subject, 1)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, true)

	result = env.Submit(credential.CredentialDeleteText(subject, subject, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireLedgerEntryNotExists(t, env, credKey)
	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireOwnerCount(t, env, subject, 0)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, false)
}

// TestCreateForSelf tests issuing a credential to yourself.
// Reference: rippled Credentials_test.cpp testSuccessful (Create for themself)
func TestCreateForSelf(t *testing.T) {
	credType := "abcde"
	issuer := jtx.NewAccount("issuer")
	env := jtx.NewTestEnv(t)
	env.Fund(issuer)
	env.Close()
	credKey := jtx.CredentialKeylet(issuer, issuer, credType)

	result := env.Submit(credential.CredentialCreateText(issuer, issuer, credType).URI("uri").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireLedgerEntryExists(t, env, credKey)
	jtx.RequireOwnerCount(t, env, issuer, 1)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, true)

	result = env.Submit(credential.CredentialDeleteText(issuer, issuer, issuer, credType).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireLedgerEntryNotExists(t, env, credKey)
	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, false)
}

// TestCredentialsDelete tests various credential deletion scenarios.
// Reference: rippled Credentials_test.cpp testCredentialsDelete
func TestCredentialsDelete(t *testing.T) {
	newEnv := func(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account) {
		t.Helper()
		issuer := jtx.NewAccount("issuer")
		subject := jtx.NewAccount("subject")
		other := jtx.NewAccount("other")
		env := jtx.NewTestEnv(t)
		env.Fund(issuer, subject, other)
		env.Close()
		return env, issuer, subject, other
	}

	// Reference: rippled testCredentialsDelete "Delete by other"
	// Third party can delete expired credentials.
	t.Run("DeleteByOther", func(t *testing.T) {
		env, issuer, subject, other := newEnv(t)
		ct := "delother"
		// Create credential with near-future expiration
		now := env.NowRipple()
		tx := credential.CredentialCreateText(issuer, subject, ct).
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
		deleteTx := credential.CredentialDeleteText(other, subject, issuer, ct).Build()
		result = env.Submit(deleteTx)
		if !result.Success {
			t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Verify credential deleted and owner counts reset
		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
		env, issuer, subject, _ := newEnv(t)
		ct := "delsubj"
		createTx := credential.CredentialCreateText(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		deleteTx := credential.CredentialDeleteText(subject, subject, issuer, ct).Build()
		result = env.Submit(deleteTx)
		if !result.Success {
			t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
		env, issuer, subject, _ := newEnv(t)
		ct := "deliss"
		createTx := credential.CredentialCreateText(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		deleteTx := credential.CredentialDeleteText(issuer, subject, issuer, ct).Build()
		result = env.Submit(deleteTx)
		if !result.Success {
			t.Errorf("Expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
	// AccountDelete cascade-deletes the issuer's credential.
	t.Run("DeleteIssuerBeforeAccept", func(t *testing.T) {
		env, issuer, subject, other := newEnv(t)
		ct := "delibacc"
		createTx := credential.CredentialCreateText(issuer, subject, ct).Build()
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
		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
		env, issuer, subject, other := newEnv(t)
		ct := "deliaaft"
		createTx := credential.CredentialCreateText(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		acceptTx := credential.CredentialAcceptText(subject, issuer, ct).Build()
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

		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
		env, issuer, subject, other := newEnv(t)
		ct := "delsbfr"
		createTx := credential.CredentialCreateText(issuer, subject, ct).Build()
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

		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
		env, issuer, subject, other := newEnv(t)
		ct := "delsaft"
		createTx := credential.CredentialCreateText(issuer, subject, ct).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s", result.Code)
		}
		env.Close()

		acceptTx := credential.CredentialAcceptText(subject, issuer, ct).Build()
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

		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
	newEnv := func(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account) {
		t.Helper()
		issuer := jtx.NewAccount("issuer")
		subject := jtx.NewAccount("subject")
		env := jtx.NewTestEnv(t)
		env.Fund(issuer, subject)
		env.Close()
		return env, issuer, subject
	}

	// Reference: rippled "Credentials fail, no subject param."
	// Removing Subject field maps to empty string in Go.
	t.Run("MissingSubject", func(t *testing.T) {
		env, issuer, _ := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		cc := credtx.NewCredentialCreate(issuer.Address, "", credTypeHex)
		cc.Fee = "10"
		result := env.Submit(cc)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Reference: rippled "Subject set to xrpAccount()"
	// In rippled this returns temMALFORMED from preflight.
	t.Run("InvalidSubjectZeroAccount", func(t *testing.T) {
		env, issuer, _ := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		cc := credtx.NewCredentialCreate(issuer.Address, protocol.ZeroAccount, credTypeHex)
		cc.Fee = "10"
		result := env.Submit(cc)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Reference: rippled "Credentials fail, no credentialType param."
	t.Run("MissingCredentialType", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		cc := credtx.NewCredentialCreate(issuer.Address, subject.Address, "")
		cc.Fee = "10"
		result := env.Submit(cc)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Reference: rippled "Credentials fail, empty credentialType param."
	t.Run("EmptyCredentialType", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		tx := credential.CredentialCreateText(issuer, subject, "").Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Reference: rippled "Credentials fail, credentialType length > maxCredentialTypeLength."
	t.Run("CredentialTypeTooLong", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		longCredType := strings.Repeat("a", 65)
		tx := credential.CredentialCreateText(issuer, subject, longCredType).Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Reference: rippled "Credentials fail, URI length > 256."
	t.Run("URITooLong", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		longURI := strings.Repeat("a", 257)
		tx := credential.CredentialCreateText(issuer, subject, credType).URI(longURI).Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Reference: rippled "Credentials fail, URI empty."
	t.Run("EmptyURI", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		tx := credential.CredentialCreateText(issuer, subject, credType).URI("").Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Reference: rippled "Credentials fail, expiration in the past."
	t.Run("ExpirationInPast", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		now := env.NowRipple()
		tx := credential.CredentialCreateText(issuer, subject, credType).
			Expiration(now - 1).Build()
		result := env.Submit(tx)
		jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
		env.Close()
	})

	// Reference: rippled "Credentials fail, invalid fee."
	// In rippled, fee=-1 triggers temBAD_FEE. Go uses string fee field;
	// a negative fee string should be rejected during validation.
	t.Run("InvalidFee", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		cc := credtx.NewCredentialCreate(issuer.Address, subject.Address, credTypeHex)
		cc.Fee = "-1"
		result := env.Submit(cc)
		jtx.RequireTxFail(t, result, jtx.TemBAD_FEE)
	})

	// Reference: rippled "Credentials fail, duplicate."
	t.Run("Duplicate", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		// First create should succeed
		tx1 := credential.CredentialCreateText(issuer, subject, credType).Build()
		result1 := env.Submit(tx1)
		if !result1.Success {
			t.Fatalf("First create expected success, got %s", result1.Code)
		}
		env.Close()

		// Second create should fail with tecDUPLICATE
		tx2 := credential.CredentialCreateText(issuer, subject, credType).Build()
		result2 := env.Submit(tx2)
		jtx.RequireTxClaimed(t, result2, jtx.TecDUPLICATE)
		env.Close()

		// Verify credential still present after failed duplicate attempt
		credKey := jtx.CredentialKeylet(subject, issuer, credType)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist after failed duplicate create")
		}

		// Cleanup
		deleteTx := credential.CredentialDeleteText(issuer, subject, issuer, credType).Build()
		jtx.RequireTxSuccess(t, env.Submit(deleteTx))
		env.Close()
	})

	// Reference: rippled "Credentials fail, subject doesn't exist."
	t.Run("SubjectDoesNotExist", func(t *testing.T) {
		env, issuer, _ := newEnv(t)
		nonExistent := jtx.NewAccount("nonexistent")
		// Do NOT fund nonExistent — it should not exist in the ledger
		tx := credential.CredentialCreateText(issuer, nonExistent, credType).Build()
		result := env.Submit(tx)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_TARGET)
		env.Close()
	})

	// Test: Invalid flags
	// Reference: rippled testFlags with fixInvalidTxFlags enabled
	t.Run("InvalidFlags", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		tx := credential.CredentialCreateText(issuer, subject, credType).Flags(0x00010000).Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
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
	issuerBalance := env.Balance(issuer)
	issuerSequence := env.Seq(issuer)
	subjectBalance := env.Balance(subject)
	subjectSequence := env.Seq(subject)
	credentialKey := jtx.CredentialKeylet(subject, issuer, credType)

	// Create should fail with insufficient reserve
	tx := credential.CredentialCreateText(issuer, subject, credType).Build()
	result := env.Submit(tx)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()
	jtx.RequireLedgerEntryNotExists(t, env, credentialKey)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credentialKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credentialKey.Key, false)
	jtx.RequireOwnerCount(t, env, issuer, 0)
	jtx.RequireOwnerCount(t, env, subject, 0)
	jtx.RequireBalance(t, env, issuer, issuerBalance-env.BaseFee())
	jtx.RequireBalance(t, env, subject, subjectBalance)
	jtx.RequireSequence(t, env, issuer, issuerSequence+1)
	jtx.RequireSequence(t, env, subject, subjectSequence)
}

// TestAcceptFailed tests CredentialAccept validation failures.
// Reference: rippled Credentials_test.cpp testAcceptFailed
func TestAcceptFailed(t *testing.T) {
	credType := "abcde"
	newEnv := func(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account) {
		t.Helper()
		issuer := jtx.NewAccount("issuer")
		subject := jtx.NewAccount("subject")
		env := jtx.NewTestEnv(t)
		env.Fund(issuer, subject)
		env.Close()
		return env, issuer, subject
	}

	// Reference: rippled "CredentialsAccept fail, Credential doesn't exist."
	t.Run("CredentialNotExist", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		tx := credential.CredentialAcceptText(subject, issuer, credType).Build()
		result := env.Submit(tx)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_ENTRY)
		env.Close()
	})

	// Reference: rippled "CredentialsAccept fail, invalid Issuer account."
	t.Run("InvalidIssuerZeroAccount", func(t *testing.T) {
		env, _, subject := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		ca := credtx.NewCredentialAccept(subject.Address, protocol.ZeroAccount, credTypeHex)
		ca.Fee = "10"
		result := env.Submit(ca)
		jtx.RequireTxFail(t, result, jtx.TemINVALID_ACCOUNT_ID)
	})

	// Reference: rippled "CredentialsAccept fail, invalid credentialType param."
	t.Run("EmptyCredentialType", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		tx := credential.CredentialAcceptText(subject, issuer, "").Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Test: Invalid flags
	t.Run("InvalidFlags", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		tx := credential.CredentialAcceptText(subject, issuer, credType).Flags(0x00010000).Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
	})

	// Reference: rippled "CredentialsAccept fail, invalid fee."
	// In rippled, fee=-1 triggers temBAD_FEE.
	t.Run("InvalidFee", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		ca := credtx.NewCredentialAccept(subject.Address, issuer.Address, credTypeHex)
		ca.Fee = "-1"
		result := env.Submit(ca)
		jtx.RequireTxFail(t, result, jtx.TemBAD_FEE)
	})

	// Reference: rippled "CredentialsAccept fail, lsfAccepted already set."
	t.Run("AlreadyAccepted", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		// Create and accept
		createTx := credential.CredentialCreateText(issuer, subject, credType).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		acceptTx := credential.CredentialAcceptText(subject, issuer, credType).Build()
		result1 := env.Submit(acceptTx)
		if !result1.Success {
			t.Fatalf("First accept expected success, got %s", result1.Code)
		}
		env.Close()

		// Try to accept again - should fail with tecDUPLICATE
		acceptTx2 := credential.CredentialAcceptText(subject, issuer, credType).Build()
		result2 := env.Submit(acceptTx2)
		jtx.RequireTxClaimed(t, result2, jtx.TecDUPLICATE)
		env.Close()

		// Verify credential still present after failed re-accept
		credKey := jtx.CredentialKeylet(subject, issuer, credType)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist after failed re-accept")
		}

		// Cleanup
		deleteTx := credential.CredentialDeleteText(subject, subject, issuer, credType).Build()
		jtx.RequireTxSuccess(t, env.Submit(deleteTx))
		env.Close()
	})

	// Reference: rippled "CredentialsAccept fail, expired credentials."
	// When accepting expired credentials, the credential is auto-deleted and tecEXPIRED returned.
	t.Run("ExpiredCredentials", func(t *testing.T) {
		env, issuer, subject := newEnv(t)
		credType2 := "efghi"

		// Create credential with expiration at current time.
		// In rippled, setting expiration to parentCloseTime and then closing one ledger
		// makes the credential expired on the next operation.
		now := env.NowRipple()
		tx := credential.CredentialCreateText(issuer, subject, credType2).
			Expiration(now).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s: %s", result.Code, result.Message)
		}
		// Close advances clock by 10s, making parentCloseTime > expiration
		env.Close()

		// Credentials are now expired
		acceptTx := credential.CredentialAcceptText(subject, issuer, credType2).Build()
		result = env.Submit(acceptTx)
		jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
		env.Close()

		// Verify that expired credentials were auto-deleted
		credKey := jtx.CredentialKeylet(subject, issuer, credType2)
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
		env, issuer, subject := newEnv(t)
		ct := "noiss"
		createTx := credential.CredentialCreateText(issuer, subject, ct).Build()
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
		acceptTx := credential.CredentialAcceptText(subject, issuer, ct).Build()
		result = env.Submit(acceptTx)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_ISSUER)
		env.Close()

		// Credential should have been cleaned up when issuer was deleted
		credKey := jtx.CredentialKeylet(subject, issuer, ct)
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
	createTx := credential.CredentialCreateText(issuer, subject, credType).Build()
	result := env.Submit(createTx)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Accept should fail - subject doesn't have reserve
	issuerBalance := env.Balance(issuer)
	issuerSequence := env.Seq(issuer)
	subjectBalance := env.Balance(subject)
	subjectSequence := env.Seq(subject)
	acceptTx := credential.CredentialAcceptText(subject, issuer, credType).Build()
	result = env.Submit(acceptTx)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()

	credKey := jtx.CredentialKeylet(subject, issuer, credType)
	jtx.RequireLedgerEntryExists(t, env, credKey)
	jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, subject, credKey.Key, true)
	jtx.RequireOwnerCount(t, env, issuer, 1)
	jtx.RequireOwnerCount(t, env, subject, 0)
	jtx.RequireBalance(t, env, issuer, issuerBalance)
	jtx.RequireBalance(t, env, subject, subjectBalance-env.BaseFee())
	jtx.RequireSequence(t, env, issuer, issuerSequence)
	jtx.RequireSequence(t, env, subject, subjectSequence+1)

	// Cleanup by issuer
	deleteTx := credential.CredentialDeleteText(issuer, subject, issuer, credType).Build()
	jtx.RequireTxSuccess(t, env.Submit(deleteTx))
	env.Close()
}

// TestDeleteFailed tests CredentialDelete validation failures.
// Reference: rippled Credentials_test.cpp testDeleteFailed
func TestDeleteFailed(t *testing.T) {
	credType := "abcde"
	newEnv := func(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account) {
		t.Helper()
		issuer := jtx.NewAccount("issuer")
		subject := jtx.NewAccount("subject")
		other := jtx.NewAccount("other")
		env := jtx.NewTestEnv(t)
		env.Fund(issuer, subject, other)
		env.Close()
		return env, issuer, subject, other
	}

	// Reference: rippled "CredentialsDelete fail, no Credentials."
	t.Run("CredentialNotExist", func(t *testing.T) {
		env, issuer, subject, _ := newEnv(t)
		tx := credential.CredentialDeleteText(subject, subject, issuer, credType).Build()
		result := env.Submit(tx)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_ENTRY)
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid Subject account."
	t.Run("InvalidSubjectZeroAccount", func(t *testing.T) {
		env, issuer, subject, _ := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		cd.Subject = protocol.ZeroAccount
		cd.Issuer = issuer.Address
		cd.Fee = "10"
		result := env.Submit(cd)
		jtx.RequireTxFail(t, result, jtx.TemINVALID_ACCOUNT_ID)
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid Issuer account."
	t.Run("InvalidIssuerZeroAccount", func(t *testing.T) {
		env, _, subject, _ := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		cd.Subject = subject.Address
		cd.Issuer = protocol.ZeroAccount
		cd.Fee = "10"
		result := env.Submit(cd)
		jtx.RequireTxFail(t, result, jtx.TemINVALID_ACCOUNT_ID)
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid credentialType param."
	t.Run("EmptyCredentialType", func(t *testing.T) {
		env, issuer, subject, _ := newEnv(t)
		tx := credential.CredentialDeleteText(subject, subject, issuer, "").Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
	})

	// Test: Invalid flags
	t.Run("InvalidFlags", func(t *testing.T) {
		env, issuer, subject, _ := newEnv(t)
		tx := credential.CredentialDeleteText(subject, subject, issuer, credType).Flags(0x00010000).Build()
		result := env.Submit(tx)
		jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
	})

	// Reference: rippled "Other account can't delete credentials without expiration"
	t.Run("NoPermission", func(t *testing.T) {
		env, issuer, subject, other := newEnv(t)
		credType2 := "fghij"

		// Create credential without expiration
		createTx := credential.CredentialCreateText(issuer, subject, credType2).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Other account tries to delete - should fail
		deleteTx := credential.CredentialDeleteText(other, subject, issuer, credType2).Build()
		result := env.Submit(deleteTx)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_PERMISSION)
		env.Close()

		// Verify credential still present after failed delete
		credKey := jtx.CredentialKeylet(subject, issuer, credType2)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist after failed delete by other")
		}

		// Cleanup by issuer
		cleanupTx := credential.CredentialDeleteText(issuer, subject, issuer, credType2).Build()
		jtx.RequireTxSuccess(t, env.Submit(cleanupTx))
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, time not expired yet."
	// Credential has an expiration but it hasn't passed yet — other can't delete.
	t.Run("TimeNotExpiredYet", func(t *testing.T) {
		env, issuer, subject, other := newEnv(t)
		now := env.NowRipple()
		// Create credential with expiration far in the future
		tx := credential.CredentialCreateText(issuer, subject, credType).
			Expiration(now + 1000).Build()
		result := env.Submit(tx)
		if !result.Success {
			t.Fatalf("Create expected success, got %s: %s", result.Code, result.Message)
		}
		env.Close()

		// Other account can't delete credentials that are not yet expired
		deleteTx := credential.CredentialDeleteText(other, subject, issuer, credType).Build()
		result = env.Submit(deleteTx)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_PERMISSION)
		env.Close()

		// Verify credential still present
		credKey := jtx.CredentialKeylet(subject, issuer, credType)
		if !env.LedgerEntryExists(credKey) {
			t.Error("Expected credential to still exist (not yet expired)")
		}

		// Cleanup by issuer
		cleanupTx := credential.CredentialDeleteText(issuer, subject, issuer, credType).Build()
		jtx.RequireTxSuccess(t, env.Submit(cleanupTx))
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, no Issuer and Subject."
	t.Run("MissingBothSubjectAndIssuer", func(t *testing.T) {
		env, _, subject, _ := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		// Leave both Subject and Issuer empty
		cd.Subject = ""
		cd.Issuer = ""
		cd.Fee = "10"
		result := env.Submit(cd)
		jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
		env.Close()
	})

	// Reference: rippled "CredentialsDelete fail, invalid fee."
	// In rippled, fee=-1 triggers temBAD_FEE.
	t.Run("InvalidFee", func(t *testing.T) {
		env, issuer, subject, _ := newEnv(t)
		credTypeHex := hex.EncodeToString([]byte(credType))
		cd := credtx.NewCredentialDelete(subject.Address, credTypeHex)
		cd.Subject = subject.Address
		cd.Issuer = issuer.Address
		cd.Fee = "-1"
		result := env.Submit(cd)
		jtx.RequireTxFail(t, result, jtx.TemBAD_FEE)
	})
}

// TestEnabled tests that credential transactions are disabled without the amendment.
// Reference: rippled Credentials_test.cpp testFeatureFailed
func TestEnabled(t *testing.T) {
	for _, operation := range []string{"create", "accept", "delete"} {
		operation := operation
		t.Run(operation, func(t *testing.T) {
			issuer := jtx.NewAccount("issuer")
			subject := jtx.NewAccount("subject")
			env := jtx.NewTestEnv(t)
			env.Fund(issuer, subject)
			env.Close()
			env.DisableFeature("Credentials")
			env.Close()

			var result jtx.TxResult
			switch operation {
			case "create":
				result = env.Submit(credential.CredentialCreateText(issuer, subject, "abcde").Build())
			case "accept":
				result = env.Submit(credential.CredentialAcceptText(subject, issuer, "abcde").Build())
			case "delete":
				result = env.Submit(credential.CredentialDeleteText(subject, subject, issuer, "abcde").Build())
			}
			jtx.RequireTxFail(t, result, jtx.TemDISABLED)
		})
	}
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
		tx := credential.CredentialCreateText(issuer, subject, ct).Build()
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
		tx := credential.CredentialAcceptText(subject, issuer, ct).Build()
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
		tx := credential.CredentialDeleteText(subject, subject, issuer, ct).Build()
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
