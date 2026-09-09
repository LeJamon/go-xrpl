package credential_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const (
	assetWithdrawalCurrency = "USD"
	assetWithdrawalDeposit  = 100
	assetWithdrawalAmount   = 20
	tecBadCredentials       = "tecBAD_CREDENTIALS"
)

type assetWithdrawalFixture struct {
	env         *jtx.TestEnv
	assetKind   string
	broker      bool
	issuer      *jtx.Account
	owner       *jtx.Account
	destination *jtx.Account
	token       *mpttest.MPTTester
	asset       tx.Asset
	amount      tx.Amount
	object      keylet.Keylet
	objectField string
	withdrawID  string
	validID     string
	lockedID    string
	expiration  uint32
}

func newAssetWithdrawalFixture(t *testing.T, assetKind string, broker bool) *assetWithdrawalFixture {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("fixCleanup3_4_0")
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("LendingProtocol")

	issuer := jtx.NewAccount(assetKind + "-issuer")
	owner := jtx.NewAccount(assetKind + "-owner")
	destination := jtx.NewAccount(assetKind + "-destination")
	env.FundAmount(issuer, 10_000_000_000)
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(destination, 10_000_000_000)
	env.Close()

	fixture := &assetWithdrawalFixture{
		env:         env,
		assetKind:   assetKind,
		broker:      broker,
		issuer:      issuer,
		owner:       owner,
		destination: destination,
		objectField: "AssetsTotal",
	}

	switch assetKind {
	case "IOU":
		fixture.asset = tx.Asset{Currency: assetWithdrawalCurrency, Issuer: issuer.Address}
		limit := tx.NewIssuedAmountFromFloat64(1_000, assetWithdrawalCurrency, issuer.Address)
		env.Trust(owner, limit)
		env.Trust(destination, limit)
		env.PayIOU(issuer, owner, issuer, assetWithdrawalCurrency, assetWithdrawalDeposit)
	case "MPT":
		fixture.token = mpttest.NewMPTTesterNoFund(t, env, issuer)
		fixture.token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer | mpttest.TfMPTCanLock})
		fixture.token.Authorize(mpttest.AuthorizeOpts{Account: owner})
		fixture.token.Authorize(mpttest.AuthorizeOpts{Account: destination})
		fixture.token.Pay(issuer, owner, assetWithdrawalDeposit)
		fixture.asset = tx.Asset{MPTIssuanceID: fixture.token.IssuanceID()}
	default:
		t.Fatalf("unsupported asset kind %q", assetKind)
	}

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, fixture.asset)
	create.Common.Fee = "50000000"
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := keylet.Vault(owner.ID, vaultSeq)
	encodedVaultID := strings.ToUpper(hex.EncodeToString(vaultID.Key[:]))
	depositAmount := fixture.depositAmount()

	if broker {
		brokerSeq := env.Seq(owner)
		jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, encodedVaultID)))
		brokerKey := keylet.LoanBroker(owner.ID, brokerSeq)
		brokerID := strings.ToUpper(hex.EncodeToString(brokerKey.Key[:]))
		jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(owner.Address, brokerID, depositAmount)))
		fixture.object = brokerKey
		fixture.withdrawID = brokerID
		fixture.objectField = "CoverAvailable"
	} else {
		jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, encodedVaultID, depositAmount)))
		fixture.object = vaultID
		fixture.withdrawID = encodedVaultID
	}
	fixture.amount = fixture.withdrawAmount()

	fixture.expiration = env.NowRipple() + 100
	fixture.validID = issueWithdrawalCredential(t, env, issuer, owner, "asset-success", fixture.expiration)
	fixture.lockedID = issueWithdrawalCredential(t, env, issuer, owner, "asset-locked", 0)
	env.EnableDepositAuth(destination)
	for _, kind := range []string{"asset-success", "asset-locked"} {
		jtx.RequireTxSuccess(t, env.Submit(depositpreauth.AuthCredentials(destination, []depositpreauth.AuthorizeCredentials{
			{Issuer: issuer, CredTypeText: kind},
		}).Build()))
	}
	env.Close()

	return fixture
}

func issueWithdrawalCredential(t *testing.T, env *jtx.TestEnv, issuer, subject *jtx.Account, kind string, expiration uint32) string {
	t.Helper()
	create := credentialtest.CredentialCreateText(issuer, subject, kind)
	if expiration != 0 {
		create.Expiration(expiration)
	}
	jtx.RequireTxSuccess(t, env.Submit(create.Build()))
	jtx.RequireTxSuccess(t, env.Submit(credentialtest.CredentialAcceptText(subject, issuer, kind).Build()))
	key := keylet.Credential(subject.ID, issuer.ID, []byte(kind))
	return hex.EncodeToString(key.Key[:])
}

func (f *assetWithdrawalFixture) depositAmount() tx.Amount {
	if f.assetKind == "MPT" {
		return f.token.MPTAmount(assetWithdrawalDeposit)
	}
	return tx.NewIssuedAmountFromFloat64(assetWithdrawalDeposit, assetWithdrawalCurrency, f.issuer.Address)
}

func (f *assetWithdrawalFixture) withdrawAmount() tx.Amount {
	if f.assetKind == "MPT" {
		return f.token.MPTAmount(assetWithdrawalAmount)
	}
	return tx.NewIssuedAmountFromFloat64(assetWithdrawalAmount, assetWithdrawalCurrency, f.issuer.Address)
}

func (f *assetWithdrawalFixture) withdrawal(ids []string) tx.Transaction {
	if f.broker {
		withdraw := lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.withdrawID, f.amount)
		withdraw.Destination = f.destination.Address
		withdraw.CredentialIDs = ids
		return withdraw
	}
	withdraw := vault.NewVaultWithdraw(f.owner.Address, f.withdrawID, f.amount)
	withdraw.Destination = f.destination.Address
	withdraw.CredentialIDs = ids
	return withdraw
}

func (f *assetWithdrawalFixture) destinationBalance() float64 {
	if f.assetKind == "MPT" {
		return float64(readMPTBalance(f.env, f.token, f.destination))
	}
	return f.env.BalanceIOU(f.destination, assetWithdrawalCurrency, f.issuer)
}

func readMPTBalance(env *jtx.TestEnv, token *mpttest.MPTTester, holder *jtx.Account) uint64 {
	encoded, err := hex.DecodeString(token.IssuanceID())
	if err != nil || len(encoded) != 24 {
		return 0
	}
	var issuanceID [24]byte
	copy(issuanceID[:], encoded)
	raw, err := env.LedgerEntry(keylet.MPTokenByID(issuanceID, holder.ID))
	if err != nil || raw == nil {
		return 0
	}
	holding, err := state.ParseMPToken(raw)
	if err != nil {
		return 0
	}
	return holding.MPTAmount
}

func (f *assetWithdrawalFixture) assertFailure(
	t *testing.T,
	result jtx.TxResult,
	want string,
	objectBefore []byte,
	destinationBefore float64,
	ownerBalance uint64,
	ownerSequence uint32,
) {
	t.Helper()
	jtx.RequireTxFail(t, result, want)
	require.Equal(t, ownerBalance-f.env.BaseFee(), f.env.Balance(f.owner))
	require.Equal(t, ownerSequence+1, f.env.Seq(f.owner))
	objectAfter, err := f.env.LedgerEntry(f.object)
	require.NoError(t, err)
	require.True(t, bytes.Equal(objectBefore, objectAfter), "withdrawal object changed on %s", want)
	require.InDelta(t, destinationBefore, f.destinationBalance(), 1e-9)
}

func assertWithdrawalObjectTransition(t *testing.T, result jtx.TxResult, object keylet.Keylet, field string) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	wantIndex := strings.ToUpper(hex.EncodeToString(object.Key[:]))
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerIndex != wantIndex || node.LedgerEntryType != objectType(field) || node.NodeType != "ModifiedNode" {
			continue
		}
		require.Equal(t, fmt.Sprint(assetWithdrawalDeposit), fmt.Sprint(node.PreviousFields[field]))
		require.Equal(t, fmt.Sprint(assetWithdrawalDeposit-assetWithdrawalAmount), fmt.Sprint(node.FinalFields[field]))
		return
	}
	t.Fatalf("modified %s node %s missing from withdrawal metadata", field, wantIndex)
}

func objectType(field string) string {
	if field == "CoverAvailable" {
		return "LoanBroker"
	}
	return "Vault"
}

func TestCredentialWithdrawalAssetAuthorizationAndRollback(t *testing.T) {
	for _, assetKind := range []string{"IOU", "MPT"} {
		for _, broker := range []bool{false, true} {
			name := fmt.Sprintf("asset=%s/broker=%v", assetKind, broker)
			t.Run(name, func(t *testing.T) {
				f := newAssetWithdrawalFixture(t, assetKind, broker)

				objectBefore, err := f.env.LedgerEntry(f.object)
				require.NoError(t, err)
				destinationBefore := f.destinationBalance()
				ownerBalance, ownerSequence := f.env.Balance(f.owner), f.env.Seq(f.owner)
				missing := strings.Repeat("F", 64)
				f.assertFailure(t, f.env.Submit(f.withdrawal([]string{missing})), tecBadCredentials,
					objectBefore, destinationBefore, ownerBalance, ownerSequence)

				destinationBefore = f.destinationBalance()
				ownerBalance, ownerSequence = f.env.Balance(f.owner), f.env.Seq(f.owner)
				result := f.env.Submit(f.withdrawal([]string{f.validID}))
				jtx.RequireTxSuccess(t, result)
				require.Equal(t, ownerBalance-f.env.BaseFee(), f.env.Balance(f.owner))
				require.Equal(t, ownerSequence+1, f.env.Seq(f.owner))
				require.InDelta(t, destinationBefore+assetWithdrawalAmount, f.destinationBalance(), 1e-9)
				assertWithdrawalObjectTransition(t, result, f.object, f.objectField)

				env := f.env
				env.CloseToParentCloseTime(f.expiration + 1)
				objectBefore, err = env.LedgerEntry(f.object)
				require.NoError(t, err)
				destinationBefore = f.destinationBalance()
				ownerBalance, ownerSequence = env.Balance(f.owner), env.Seq(f.owner)
				f.assertFailure(t, env.Submit(f.withdrawal([]string{f.validID})), jtx.TecEXPIRED,
					objectBefore, destinationBefore, ownerBalance, ownerSequence)
				credentialKey := keylet.Credential(f.owner.ID, f.issuer.ID, []byte("asset-success"))
				require.False(t, env.LedgerEntryExists(credentialKey))

				if assetKind == "IOU" {
					env.EnableGlobalFreeze(f.issuer)
				} else {
					f.token.Set(mpttest.SetOpts{Account: f.issuer, Flags: mpttest.TfMPTLock})
				}
				env.Close()

				objectBefore, err = env.LedgerEntry(f.object)
				require.NoError(t, err)
				destinationBefore = f.destinationBalance()
				ownerBalance, ownerSequence = env.Balance(f.owner), env.Seq(f.owner)
				freezeCode := jtx.TecFROZEN
				if assetKind == "MPT" {
					freezeCode = jtx.TecLOCKED
				}
				f.assertFailure(t, env.Submit(f.withdrawal([]string{f.lockedID})), freezeCode,
					objectBefore, destinationBefore, ownerBalance, ownerSequence)

				objectBefore, err = env.LedgerEntry(f.object)
				require.NoError(t, err)
				destinationBefore = f.destinationBalance()
				ownerBalance, ownerSequence = env.Balance(f.owner), env.Seq(f.owner)
				// Credential validity is checked before the freeze/lock checks.
				f.assertFailure(t, env.Submit(f.withdrawal([]string{missing})), tecBadCredentials,
					objectBefore, destinationBefore, ownerBalance, ownerSequence)
			})
		}
	}
}
