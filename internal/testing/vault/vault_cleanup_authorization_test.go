package vault_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	permissioneddomaintest "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

type zeroAssetWithdrawFixture struct {
	env          *jtx.TestEnv
	owner        *jtx.Account
	holder       *jtx.Account
	assetIssuer  *jtx.Account
	vaultID      string
	vaultKey     keylet.Keylet
	shareID      [24]byte
	assetHolding keylet.Keylet
	assetAmount  tx.Amount
	withdraw     func() *vault.VaultWithdraw
	shareAmount  uint64
}

// newZeroAssetWithdrawFixture creates real vault shares and then represents a
// fully impaired vault with no available assets. The holder's underlying
// holding is removed after deposit, matching the cleanup bug's missing-holding
// path while leaving the share holding available for redemption.
func newZeroAssetWithdrawFixture(t *testing.T, kind string, cleanup bool) *zeroAssetWithdrawFixture {
	t.Helper()
	env := newVaultEnv(t)
	env.DisableFeature("MPTokensV2")
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()

	issuer := jtx.NewAccount(kind + "-zero-issuer")
	owner := jtx.NewAccount(kind + "-zero-owner")
	holder := jtx.NewAccount(kind + "-zero-holder")
	other := jtx.NewAccount(kind + "-zero-other")
	var asset tx.Asset
	var assetAmount tx.Amount
	var removeHolding func()
	var amount string
	var issuanceID [24]byte

	switch kind {
	case "IOU":
		env.Fund(issuer, owner, holder, other)
		jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))
		asset = tx.Asset{Currency: "USD", Issuer: issuer.Address}
		limit := tx.NewIssuedAmountFromFloat64(100, "USD", issuer.Address)
		env.Trust(holder, limit)
		env.Trust(other, limit)
		env.PayIOU(issuer, holder, issuer, "USD", 2)
		env.PayIOU(issuer, other, issuer, "USD", 8)
		amount = "10"
		assetAmount = tx.NewIssuedAmountFromFloat64(1, "USD", issuer.Address)
		removeHolding = func() {
			env.Trust(holder, tx.NewIssuedAmountFromFloat64(0, "USD", issuer.Address))
		}
	case "MPT":
		env.Fund(owner)
		token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{holder, other}})
		token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})
		token.Authorize(mpttest.AuthorizeOpts{Account: holder})
		token.Authorize(mpttest.AuthorizeOpts{Account: other})
		token.Pay(issuer, holder, 2)
		token.Pay(issuer, other, 8)
		asset = tx.Asset{MPTIssuanceID: token.IssuanceID()}
		amount = "10"
		idBytes, err := hex.DecodeString(token.IssuanceID())
		require.NoError(t, err)
		copy(issuanceID[:], idBytes)
		assetAmount = state.NewMPTAmountWithIssuanceID(1, "", strings.ToUpper(hex.EncodeToString(issuanceID[:])))
		removeHolding = func() {
			token.Authorize(mpttest.AuthorizeOpts{Account: holder, Flags: mpttest.TfMPTUnauthorize})
		}
	default:
		t.Fatalf("unsupported asset kind %q", kind)
	}

	vaultSequence := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, asset)
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := vaultID(owner, vaultSequence)

	if kind == "IOU" {
		jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(holder.Address, vaultID, tx.NewIssuedAmountFromFloat64(2, "USD", issuer.Address))))
		jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(other.Address, vaultID, tx.NewIssuedAmountFromFloat64(8, "USD", issuer.Address))))
	} else {
		jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(holder.Address, vaultID, state.NewMPTAmountWithIssuanceID(2, "", strings.ToUpper(hex.EncodeToString(issuanceID[:]))))))
		jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(other.Address, vaultID, state.NewMPTAmountWithIssuanceID(8, "", strings.ToUpper(hex.EncodeToString(issuanceID[:]))))))
	}
	removeHolding()

	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	patchImpairedVault(t, env, vaultKey, amount, "0", amount)
	info, err := vault.ReadVaultLending(env.Ledger(), vaultKey)
	require.NoError(t, err)
	require.NotNil(t, info)
	if kind == "IOU" {
		issuanceID = info.ShareMPTID
	}
	shareID := mptutil.EncodeID(info.ShareMPTID)
	shareAmount := state.NewMPTAmountWithIssuanceID(1, "", shareID)
	return &zeroAssetWithdrawFixture{
		env:          env,
		owner:        owner,
		holder:       holder,
		assetIssuer:  issuer,
		vaultID:      vaultID,
		vaultKey:     vaultKey,
		shareID:      info.ShareMPTID,
		assetHolding: holdingKey(asset, issuanceID, holder.AccountID()),
		assetAmount:  assetAmount,
		withdraw:     func() *vault.VaultWithdraw { return vault.NewVaultWithdraw(holder.Address, vaultID, shareAmount) },
		shareAmount:  1,
	}
}

func holdingKey(asset tx.Asset, issuanceID [24]byte, accountID [20]byte) keylet.Keylet {
	if asset.IsMPT() {
		return keylet.MPTokenByID(issuanceID, accountID)
	}
	issuerID, _ := state.DecodeAccountID(asset.Issuer)
	return keylet.Line(accountID, issuerID, asset.Currency)
}

func patchImpairedVault(t *testing.T, env *jtx.TestEnv, key keylet.Keylet, total, available, loss string) {
	t.Helper()
	raw, err := env.LedgerEntry(key)
	require.NoError(t, err)
	var wire entry.Vault
	require.NoError(t, wire.Decode(raw))
	wire.SetAssetsTotal(total)
	wire.SetAssetsAvailable(available)
	if loss == "" {
		wire.SetLossUnrealized(nil)
	} else {
		wire.SetLossUnrealized(loss)
	}
	updated, err := wire.Encode()
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(key, updated))
}

func TestVaultWithdrawZeroAssetMissingHoldingCleanup(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		for _, cleanup := range []bool{false, true} {
			name := kind + "/without_fixCleanup3_4_0"
			if cleanup {
				name = kind + "/with_fixCleanup3_4_0"
			}
			t.Run(name, func(t *testing.T) {
				f := newZeroAssetWithdrawFixture(t, kind, cleanup)
				beforeBalance := f.env.Balance(f.holder)
				beforeSequence := f.env.Seq(f.holder)
				beforeOwnerCount := f.env.OwnerCount(f.holder)
				beforeShares := vaultShareBalance(t, f.env, f.shareID, f.holder)
				beforeVault, err := f.env.LedgerEntry(f.vaultKey)
				require.NoError(t, err)
				result := f.env.Submit(f.withdraw())
				require.NotNil(t, result.Metadata)
				require.Equal(t, result.Code, result.Metadata.TransactionResult.String())
				if cleanup {
					require.Equal(t, ter.TesSUCCESS.String(), result.Code)
					require.True(t, result.Success)
					require.Equal(t, f.env.BaseFee(), result.Fee)
					require.Equal(t, beforeShares-f.shareAmount, vaultShareBalance(t, f.env, f.shareID, f.holder))
				} else {
					require.Equal(t, ter.TecINVARIANT_FAILED.String(), result.Code)
					require.True(t, result.Applied)
					require.Equal(t, f.env.BaseFee(), result.Fee)
					require.Equal(t, beforeShares, vaultShareBalance(t, f.env, f.shareID, f.holder))
				}
				afterVault, err := f.env.LedgerEntry(f.vaultKey)
				require.NoError(t, err)
				require.Equal(t, beforeVault, afterVault, "zero payout changed vault totals")
				require.False(t, f.env.LedgerEntryExists(f.assetHolding), "underlying holding was recreated")
				require.Equal(t, beforeOwnerCount, f.env.OwnerCount(f.holder))
				require.Equal(t, beforeBalance-f.env.BaseFee(), f.env.Balance(f.holder))
				require.Equal(t, beforeSequence+1, f.env.Seq(f.holder))
			})
		}
	}
}

func TestVaultWithdrawNonzeroCreatesMissingHolding(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind, func(t *testing.T) {
			f := newZeroAssetWithdrawFixture(t, kind, true)
			patchImpairedVault(t, f.env, f.vaultKey, "10", "10", "")
			beforeBalance := f.env.Balance(f.holder)
			beforeSequence := f.env.Seq(f.holder)
			beforeOwnerCount := f.env.OwnerCount(f.holder)
			result := f.env.Submit(vault.NewVaultWithdraw(f.holder.Address, f.vaultID, f.assetAmount))
			jtx.RequireTxSuccess(t, result)
			require.Equal(t, f.env.BaseFee(), result.Fee)
			require.Equal(t, beforeBalance-f.env.BaseFee(), f.env.Balance(f.holder))
			require.Equal(t, beforeSequence+1, f.env.Seq(f.holder))
			require.Equal(t, beforeOwnerCount+1, f.env.OwnerCount(f.holder))
			require.True(t, f.env.LedgerEntryExists(f.assetHolding), "nonzero withdrawal did not create %s holding", kind)
			holdingData, err := f.env.LedgerEntry(f.assetHolding)
			require.NoError(t, err)
			if kind == "IOU" {
				line, err := state.ParseRippleState(holdingData)
				require.NoError(t, err)
				if keylet.IsLowAccount(f.holder.AccountID(), f.assetIssuer.AccountID()) {
					require.Equal(t, "1", line.Balance.Value())
				} else {
					require.Equal(t, "-1", line.Balance.Value())
				}
				return
			}
			token, err := state.ParseMPToken(holdingData)
			require.NoError(t, err)
			require.Equal(t, uint64(1), token.MPTAmount)
		})
	}
}

func cleanupZeroAssetAddReserveHolding(t *testing.T, f *zeroAssetWithdrawFixture, kind string) {
	t.Helper()
	reserveIssuer := jtx.NewAccount(kind + "-reserve-issuer")
	f.env.Fund(reserveIssuer)
	switch kind {
	case "IOU":
		f.env.Trust(f.holder, tx.NewIssuedAmountFromFloat64(100, "EUR", reserveIssuer.Address))
	case "MPT":
		token := mpttest.NewMPTTesterNoFund(t, f.env, reserveIssuer)
		token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})
		token.Authorize(mpttest.AuthorizeOpts{Account: f.holder})
		token.Pay(reserveIssuer, f.holder, 1)
	default:
		t.Fatalf("unsupported asset kind %q", kind)
	}
	require.GreaterOrEqual(t, f.env.OwnerCount(f.holder), uint32(2), "fixture needs a second holder object")
}

func cleanupZeroAssetDrainToReserve(t *testing.T, f *zeroAssetWithdrawFixture) {
	t.Helper()
	reserve := f.env.ReserveBase() + uint64(f.env.OwnerCount(f.holder))*f.env.ReserveIncrement()
	fee := f.env.BaseFee()
	before := f.env.Balance(f.holder)
	require.Greater(t, before, reserve+fee, "fixture holder must have funds to drain")
	amount := before - reserve - fee
	payment := paymenttx.NewPayment(f.holder.Address, f.owner.Address, tx.NewXRPAmount(int64(amount)))
	jtx.RequireTxSuccess(t, f.env.Submit(payment))
	require.Equal(t, reserve, f.env.Balance(f.holder))
}

func TestVaultWithdrawNonzeroMissingHoldingInsufficientReserve(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind, func(t *testing.T) {
			f := newZeroAssetWithdrawFixture(t, kind, true)
			patchImpairedVault(t, f.env, f.vaultKey, "10", "10", "")
			cleanupZeroAssetAddReserveHolding(t, f, kind)
			cleanupZeroAssetDrainToReserve(t, f)

			beforeBalance := f.env.Balance(f.holder)
			beforeSequence := f.env.Seq(f.holder)
			beforeOwnerCount := f.env.OwnerCount(f.holder)
			beforeShares := vaultShareBalance(t, f.env, f.shareID, f.holder)
			beforeVault, err := f.env.LedgerEntry(f.vaultKey)
			require.NoError(t, err)
			result := f.env.Submit(vault.NewVaultWithdraw(f.holder.Address, f.vaultID, f.assetAmount))
			wantCode := jtx.TecINSUFFICIENT_RESERVE
			if kind == "IOU" {
				wantCode = jtx.TecNO_LINE_INSUF_RESERVE
			}
			require.Equal(t, wantCode, result.Code)
			require.True(t, result.Applied)
			require.Equal(t, f.env.BaseFee(), result.Fee)
			require.NotNil(t, result.Metadata)
			require.Equal(t, result.Code, result.Metadata.TransactionResult.String())
			require.Equal(t, beforeBalance-f.env.BaseFee(), f.env.Balance(f.holder))
			require.Equal(t, beforeSequence+1, f.env.Seq(f.holder))
			require.Equal(t, beforeOwnerCount, f.env.OwnerCount(f.holder))
			require.Equal(t, beforeShares, vaultShareBalance(t, f.env, f.shareID, f.holder))
			require.False(t, f.env.LedgerEntryExists(f.assetHolding))
			afterVault, err := f.env.LedgerEntry(f.vaultKey)
			require.NoError(t, err)
			require.Equal(t, beforeVault, afterVault, "rejected withdrawal changed vault state")
		})
	}
}

func TestPrivateVaultWithdrawValidatesDestinationDomain(t *testing.T) {
	env := newVaultEnv(t)
	env.EnableFeature("fixCleanup3_4_0")
	env.Close()

	owner := jtx.NewAccount("private-withdraw-owner")
	holder := jtx.NewAccount("private-withdraw-holder")
	destination := jtx.NewAccount("private-withdraw-destination")
	domainOwner := jtx.NewAccount("private-withdraw-domain-owner")
	credentialIssuer := jtx.NewAccount("private-withdraw-credential-issuer")
	env.Fund(owner, holder, destination, domainOwner, credentialIssuer)

	const credentialType = "vault-withdraw"
	credentialTypeHex := strings.ToUpper(hex.EncodeToString([]byte(credentialType)))
	domainSequence := env.Seq(domainOwner)
	jtx.RequireTxSuccess(t, env.Submit(
		permissioneddomaintest.DomainSet(domainOwner).
			Credential(credentialIssuer, credentialTypeHex).
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

	for _, account := range []*jtx.Account{holder, destination} {
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialCreateText(credentialIssuer, account, credentialType).Build(),
		))
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialAcceptText(account, credentialIssuer, credentialType).Build(),
		))
	}
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(holder.Address, vaultID, tx.NewXRPAmount(1_000_000))))

	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	info, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
	require.NoError(t, err)
	require.NotNil(t, info)
	shareID := info.ShareMPTID
	beforeDestination := env.Balance(destination)
	beforeHolderShares := vaultShareBalance(t, env, shareID, holder)
	withdraw := func() *vault.VaultWithdraw {
		w := vault.NewVaultWithdraw(holder.Address, vaultID, tx.NewXRPAmount(100_000))
		w.Destination = destination.Address
		return w
	}
	jtx.RequireTxSuccess(t, env.Submit(withdraw()))
	require.Equal(t, beforeDestination+100_000, env.Balance(destination))
	require.Less(t, vaultShareBalance(t, env, shareID, holder), beforeHolderShares)

	jtx.RequireTxSuccess(t, env.Submit(
		credentialtest.CredentialDeleteText(credentialIssuer, destination, credentialIssuer, credentialType).Build(),
	))
	beforeBalance := env.Balance(holder)
	beforeSequence := env.Seq(holder)
	beforeDestination = env.Balance(destination)
	beforeShares := vaultShareBalance(t, env, shareID, holder)
	beforeVault, err := env.LedgerEntry(vaultKey)
	require.NoError(t, err)
	result := env.Submit(withdraw())
	require.Equal(t, jtx.TecNO_AUTH, result.Code)
	require.True(t, result.Applied)
	require.Equal(t, env.BaseFee(), result.Fee)
	require.NotNil(t, result.Metadata)
	require.Equal(t, result.Code, result.Metadata.TransactionResult.String())
	require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(holder))
	require.Equal(t, beforeSequence+1, env.Seq(holder))
	require.Equal(t, beforeDestination, env.Balance(destination))
	require.Equal(t, beforeShares, vaultShareBalance(t, env, shareID, holder))
	afterVault, err := env.LedgerEntry(vaultKey)
	require.NoError(t, err)
	require.Equal(t, beforeVault, afterVault, "unauthorized destination changed vault state")
}

func TestVaultClawbackPseudoHolderPrecedence(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "without_fixCleanup3_4_0"
		if cleanup {
			name = "with_fixCleanup3_4_0"
		}
		t.Run(name, func(t *testing.T) {
			for _, explicit := range []bool{false, true} {
				t.Run(map[bool]string{false: "implicit", true: "explicit"}[explicit], func(t *testing.T) {
					env := newVaultEnv(t)
					if cleanup {
						env.EnableFeature("fixCleanup3_4_0")
					} else {
						env.DisableFeature("fixCleanup3_4_0")
					}
					env.Close()

					issuer := jtx.NewAccount("pseudo-clawback-issuer")
					owner := jtx.NewAccount("pseudo-clawback-owner")
					holder := jtx.NewAccount("pseudo-clawback-holder")
					env.Fund(issuer)
					jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).AllowClawback().Build()))
					env.Fund(owner, holder)
					const currency = "USD"
					env.Trust(owner, tx.NewIssuedAmountFromFloat64(1_000, currency, issuer.Address))
					env.Trust(holder, tx.NewIssuedAmountFromFloat64(100, currency, issuer.Address))
					env.PayIOU(issuer, holder, issuer, currency, 100)

					vaultSequence := env.Seq(owner)
					create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: currency, Issuer: issuer.Address})
					create.Common.Fee = createFee
					jtx.RequireTxSuccess(t, env.Submit(create))
					vaultID := vaultID(owner, vaultSequence)
					jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(
						holder.Address,
						vaultID,
						tx.NewIssuedAmountFromFloat64(100, currency, issuer.Address),
					)))
					info, err := vault.ReadVaultInfo(env.Ledger(), keylet.Vault(owner.AccountID(), vaultSequence))
					require.NoError(t, err)
					require.NotNil(t, info)
					pseudoAddress, err := state.EncodeAccountID(info.Account)
					require.NoError(t, err)

					claw := vault.NewVaultClawback(issuer.Address, vaultID, pseudoAddress)
					if explicit {
						amount := tx.NewIssuedAmountFromFloat64(1, currency, issuer.Address)
						claw.Amount = &amount
					}
					vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
					beforeVault, err := env.LedgerEntry(vaultKey)
					require.NoError(t, err)
					beforeBalance := env.Balance(issuer)
					beforeSequence := env.Seq(issuer)
					result := env.Submit(claw)
					want := jtx.TecINVARIANT_FAILED
					if !explicit {
						want = ter.TecPRECISION_LOSS.String()
					}
					if cleanup {
						want = ter.TecPSEUDO_ACCOUNT.String()
					}
					require.Equal(t, want, result.Code)
					require.True(t, result.Applied)
					require.Equal(t, env.BaseFee(), result.Fee)
					require.NotNil(t, result.Metadata)
					require.Equal(t, result.Code, result.Metadata.TransactionResult.String())
					require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(issuer))
					require.Equal(t, beforeSequence+1, env.Seq(issuer))
					require.True(t, env.VaultExists(vaultID))
					afterVault, err := env.LedgerEntry(vaultKey)
					require.NoError(t, err)
					require.Equal(t, beforeVault, afterVault, "pseudo-holder clawback changed vault state")
				})
			}
		})
	}
}
