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
	domainOwner := jtx.NewAccount("domain-owner")
	validIssuer := jtx.NewAccount("valid-credential-issuer")
	expiredIssuer := jtx.NewAccount("expired-credential-issuer")
	env.Fund(owner, depositor, domainOwner, validIssuer, expiredIssuer)

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
		credentialtest.CredentialCreate(validIssuer, depositor, credentialType).Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialAccept(depositor, validIssuer, credentialType).Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(deposit()))

	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialDelete(validIssuer, depositor, validIssuer, credentialType).Build(),
	))

	const expiration uint32 = 2_000_000_000
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialCreate(expiredIssuer, depositor, credentialType).
			Expiration(expiration).
			Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialAccept(depositor, expiredIssuer, credentialType).Build(),
	))
	expiredKey := jtx.CredentialKeylet(depositor, expiredIssuer, credentialType)
	if !env.LedgerEntryExists(expiredKey) {
		t.Fatal("accepted credential missing before expiration")
	}

	env.CloseToParentCloseTime(expiration + 1)
	jtx.RequireTxClaimed(t, env.Submit(deposit()), jtx.TecEXPIRED)
	if env.LedgerEntryExists(expiredKey) {
		t.Fatal("expired credential was not deleted on the tecEXPIRED path")
	}
}
