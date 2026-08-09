package vault_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	permissioneddomaintest "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestVaultDeposit_ScaleOverflowReturnsPathDry(t *testing.T) {
	env := newVaultEnv(t)
	issuer := jtx.NewAccount("overflow-issuer")
	owner := jtx.NewAccount("overflow-owner")
	depositor := jtx.NewAccount("overflow-depositor")
	env.Fund(issuer)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))
	env.Fund(owner, depositor)

	const currency = "USD"
	env.Trust(depositor, tx.NewIssuedAmountFromFloat64(1_000, currency, issuer.Address))
	env.PayIOU(issuer, depositor, issuer, currency, 100)

	sequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: currency, Issuer: issuer.Address})
	scale := uint8(18)
	create.Scale = &scale
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))

	deposit := vault.NewVaultDeposit(
		depositor.Address,
		vaultID(owner, sequence),
		tx.NewIssuedAmountFromFloat64(10, currency, issuer.Address),
	)
	jtx.RequireTxClaimed(t, env.Submit(deposit), jtx.TecPATH_DRY)
}

func TestVaultDeposit_IOUFullBalanceIncludesOppositeLimit(t *testing.T) {
	env := newVaultEnv(t)
	issuer := jtx.NewAccount("full-balance-issuer")
	owner := jtx.NewAccount("full-balance-owner")
	depositor := jtx.NewAccount("full-balance-depositor")
	env.Fund(issuer)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))
	env.Fund(owner, depositor)

	const currency = "USD"
	env.Trust(depositor, tx.NewIssuedAmountFromFloat64(1_000, currency, issuer.Address))
	env.PayIOU(issuer, depositor, issuer, currency, 100)
	env.Trust(issuer, tx.NewIssuedAmountFromFloat64(1_000, currency, depositor.Address))

	sequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: currency, Issuer: issuer.Address})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))

	deposit := vault.NewVaultDeposit(
		depositor.Address,
		vaultID(owner, sequence),
		tx.NewIssuedAmountFromFloat64(500, currency, issuer.Address),
	)
	jtx.RequireTxSuccess(t, env.Submit(deposit))
	if balance, ok := iouLineBalance(t, env, depositor.ID, issuer.ID, currency); !ok || !approxEq(balance, -400) {
		t.Fatalf("depositor balance = %v (exists=%v), want -400", balance, ok)
	}
}

func TestVaultDeposit_PrivateDomainAuthorizationAndExpiredCleanup(t *testing.T) {
	env := newVaultEnv(t)
	owner := jtx.NewAccount("vault-owner")
	depositor := jtx.NewAccount("vault-depositor")
	noTokenDepositor := jtx.NewAccount("vault-depositor-without-token")
	domainOwner := jtx.NewAccount("domain-owner")
	validIssuer := jtx.NewAccount("valid-credential-issuer")
	expiredIssuer := jtx.NewAccount("expired-credential-issuer")
	env.Fund(owner, depositor, noTokenDepositor, domainOwner, validIssuer, expiredIssuer)

	const credentialType = "vault-access"
	credentialTypeHex := strings.ToUpper(hex.EncodeToString([]byte(credentialType)))
	domainSequence := env.Seq(domainOwner)
	jtx.RequireTxSuccess(t, env.Submit(
		permissioneddomaintest.DomainSet(domainOwner).
			Credential(validIssuer, credentialTypeHex).
			Credential(expiredIssuer, credentialTypeHex).
			Build(),
	))
	domainKey := keylet.PermissionedDomain(domainOwner.ID, domainSequence)
	domainID := strings.ToUpper(hex.EncodeToString(domainKey.Key[:]))

	vaultSequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	privateFlag := vault.VaultFlagPrivate
	create.Common.Flags = &privateFlag
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := vaultID(owner, vaultSequence)

	set := vault.NewVaultSet(owner.Address, vaultID)
	set.DomainID = domainID
	jtx.RequireTxSuccess(t, env.Submit(set))

	deposit := func() *vault.VaultDeposit {
		return vault.NewVaultDeposit(depositor.Address, vaultID, tx.NewXRPAmount(1_000_000))
	}
	jtx.RequireTxClaimed(t, env.Submit(deposit()), jtx.TecNO_AUTH)

	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialCreateText(validIssuer, depositor, credentialType).Build(),
	))
	jtx.RequireTxClaimed(t, env.Submit(deposit()), jtx.TecNO_AUTH)
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialAcceptText(depositor, validIssuer, credentialType).Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(deposit()))

	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialDeleteText(validIssuer, depositor, validIssuer, credentialType).Build(),
	))

	const expiration uint32 = 2_000_000_000
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialCreateText(expiredIssuer, depositor, credentialType).
			Expiration(expiration).
			Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialAcceptText(depositor, expiredIssuer, credentialType).Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialCreateText(expiredIssuer, noTokenDepositor, credentialType).
			Expiration(expiration).
			Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialAcceptText(noTokenDepositor, expiredIssuer, credentialType).Build(),
	))
	expiredKey := jtx.CredentialKeylet(depositor, expiredIssuer, credentialType)
	noTokenExpiredKey := jtx.CredentialKeylet(noTokenDepositor, expiredIssuer, credentialType)
	if !env.LedgerEntryExists(expiredKey) {
		t.Fatal("accepted credential missing before expiration")
	}

	rawVaultID, err := hex.DecodeString(vaultID)
	require.NoError(t, err)
	var vaultKey [32]byte
	copy(vaultKey[:], rawVaultID)
	vaultInfo, err := vault.ReadVaultInfo(env.Ledger(), keylet.VaultByID(vaultKey))
	require.NoError(t, err)
	require.NotNil(t, vaultInfo)
	noTokenKey := keylet.MPTokenByID(vaultInfo.ShareMPTID, noTokenDepositor.ID)
	jtx.RequireLedgerEntryNotExists(t, env, noTokenKey)

	env.CloseToParentCloseTime(expiration + 1)
	noTokenDeposit := vault.NewVaultDeposit(noTokenDepositor.Address, vaultID, tx.NewXRPAmount(1_000_000))
	jtx.RequireTxClaimed(t, env.Submit(noTokenDeposit), jtx.TecEXPIRED)
	jtx.RequireLedgerEntryNotExists(t, env, noTokenExpiredKey)
	jtx.RequireLedgerEntryNotExists(t, env, noTokenKey)
	jtx.RequireTxClaimed(t, env.Submit(deposit()), jtx.TecEXPIRED)
	if env.LedgerEntryExists(expiredKey) {
		t.Fatal("expired credential was not deleted on the tecEXPIRED path")
	}
}

func TestVaultDeposit_ExpiredCleanupReplaysOnlyErasedCredentials(t *testing.T) {
	env := newVaultEnv(t)
	env.DisableFeature("fixCleanup3_1_3")
	env.Close()

	owner := jtx.NewAccount("vault-owner")
	depositor := jtx.NewAccount("vault-depositor")
	domainOwner := jtx.NewAccount("domain-owner")
	cleanIssuer := jtx.NewAccount("clean-issuer")
	corruptIssuer := jtx.NewAccount("corrupt-issuer")
	env.Fund(owner, depositor, domainOwner, cleanIssuer, corruptIssuer)

	const credentialType = "vault-access"
	credentialTypeHex := strings.ToUpper(hex.EncodeToString([]byte(credentialType)))
	domainSequence := env.Seq(domainOwner)
	jtx.RequireTxSuccess(t, env.Submit(
		permissioneddomaintest.DomainSet(domainOwner).
			Credential(cleanIssuer, credentialTypeHex).
			Credential(corruptIssuer, credentialTypeHex).
			Build(),
	))
	domainKey := keylet.PermissionedDomain(domainOwner.ID, domainSequence)
	domainID := strings.ToUpper(hex.EncodeToString(domainKey.Key[:]))

	vaultSequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	privateFlag := vault.VaultFlagPrivate
	create.Common.Flags = &privateFlag
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := vaultID(owner, vaultSequence)
	set := vault.NewVaultSet(owner.Address, vaultID)
	set.DomainID = domainID
	jtx.RequireTxSuccess(t, env.Submit(set))

	const expiration uint32 = 2_000_000_000
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialCreateText(cleanIssuer, depositor, credentialType).
			Expiration(expiration).
			Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialAcceptText(depositor, cleanIssuer, credentialType).Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialCreateText(corruptIssuer, depositor, credentialType).
			Expiration(expiration).
			Build(),
	))

	cleanKey := jtx.CredentialKeylet(depositor, cleanIssuer, credentialType)
	corruptKey := jtx.CredentialKeylet(depositor, corruptIssuer, credentialType)
	corruptData, err := env.LedgerEntry(corruptKey)
	require.NoError(t, err)
	var corruptCredential entry.Credential
	require.NoError(t, corruptCredential.Decode(corruptData))
	corruptCredential.SetSubjectNode("1")
	corruptData, err = corruptCredential.Encode()
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(corruptKey, corruptData))

	corruptIssuerAccountBefore, err := env.LedgerEntry(keylet.Account(corruptIssuer.ID))
	require.NoError(t, err)
	corruptIssuerDirectoryBefore, err := env.LedgerEntry(keylet.OwnerDir(corruptIssuer.ID))
	require.NoError(t, err)

	env.CloseToParentCloseTime(expiration + 1)
	deposit := vault.NewVaultDeposit(depositor.Address, vaultID, tx.NewXRPAmount(1_000_000))
	jtx.RequireTxClaimed(t, env.Submit(deposit), jtx.TecEXPIRED)

	jtx.RequireLedgerEntryNotExists(t, env, cleanKey)
	jtx.RequireLedgerEntryExists(t, env, corruptKey)
	jtx.RequireOwnerDirectoryContains(t, env, depositor, cleanKey.Key, false)
	jtx.RequireOwnerDirectoryContains(t, env, depositor, corruptKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, corruptIssuer, corruptKey.Key, true)
	jtx.RequireOwnerCount(t, env, depositor, 0)
	jtx.RequireOwnerCount(t, env, corruptIssuer, 1)

	corruptDataAfter, err := env.LedgerEntry(corruptKey)
	require.NoError(t, err)
	require.Equal(t, corruptData, corruptDataAfter)
	corruptIssuerAccountAfter, err := env.LedgerEntry(keylet.Account(corruptIssuer.ID))
	require.NoError(t, err)
	require.Equal(t, corruptIssuerAccountBefore, corruptIssuerAccountAfter)
	corruptIssuerDirectoryAfter, err := env.LedgerEntry(keylet.OwnerDir(corruptIssuer.ID))
	require.NoError(t, err)
	require.Equal(t, corruptIssuerDirectoryBefore, corruptIssuerDirectoryAfter)
}
