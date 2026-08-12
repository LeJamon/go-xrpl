// Package accountdelete_test contains behavioral tests for AccountDelete.
// Tests ported from rippled's AccountDelete_test.cpp.
//
// Reference: rippled/src/test/app/AccountDelete_test.cpp
package accountdelete_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	acctx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type deleteBalances struct {
	source      uint64
	destination uint64
	fee         uint64
}

func newAccountDelete(env *jtx.TestEnv, from, to *jtx.Account) *acctx.AccountDelete {
	d := acctx.NewAccountDelete(from.Address, to.Address)
	d.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
	return d
}

func captureDeleteBalances(env *jtx.TestEnv, from, to *jtx.Account) deleteBalances {
	return deleteBalances{
		source:      env.Balance(from),
		destination: env.Balance(to),
		fee:         env.ReserveIncrement(),
	}
}

func requireAccountDeleteSuccess(
	t *testing.T,
	env *jtx.TestEnv,
	result jtx.TxResult,
	from, to *jtx.Account,
	before deleteBalances,
	owned ...keylet.Keylet,
) {
	t.Helper()
	jtx.RequireTxSuccess(t, result)
	require.True(t, result.Applied)
	require.Equal(t, before.fee, result.Fee)
	require.GreaterOrEqual(t, before.source, before.fee)
	delivered := before.source - before.fee
	require.Equal(t, before.destination+delivered, env.Balance(to))

	require.NotNil(t, result.Metadata)
	require.NotNil(t, result.Metadata.DeliveredAmount)
	require.True(t, result.Metadata.DeliveredAmount.IsNative())
	require.Equal(t, fmt.Sprintf("%d", delivered), result.Metadata.DeliveredAmount.Value())
	require.NotNil(t, metadata.FindNode(result.Metadata, "DeletedNode", "AccountRoot"))

	jtx.RequireAccountNotExists(t, env, from)
	jtx.RequireLedgerEntryNotExists(t, env, keylet.OwnerDir(from.ID))
	for _, ownedKey := range owned {
		jtx.RequireLedgerEntryNotExists(t, env, ownedKey)
	}
}

func submitAccountDeleteSuccess(
	t *testing.T,
	env *jtx.TestEnv,
	from, to *jtx.Account,
	owned ...keylet.Keylet,
) jtx.TxResult {
	t.Helper()
	before := captureDeleteBalances(env, from, to)
	result := env.Submit(newAccountDelete(env, from, to))
	requireAccountDeleteSuccess(t, env, result, from, to, before, owned...)
	return result
}

// TestAccountDelete_Basics tests fundamental AccountDelete validation and success cases.
// Reference: rippled AccountDelete_test.cpp testBasics
func TestAccountDelete_Basics(t *testing.T) {
	t.Run("SelfDelete", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		becky := jtx.NewAccount("becky")
		env.Fund(alice, becky)
		env.Close()

		d := acctx.NewAccountDelete(alice.Address, alice.Address)
		d.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		jtx.RequireTxFail(t, env.Submit(d), jtx.TemDST_IS_SRC)
	})

	t.Run("TooSoon_SequenceNotFarEnough", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		becky := jtx.NewAccount("becky")
		env.Fund(alice, becky)
		env.Close()

		result := env.Submit(newAccountDelete(env, alice, becky))
		jtx.RequireTxFail(t, result, "tecTOO_SOON")
	})

	t.Run("BasicSuccess", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		becky := jtx.NewAccount("becky")
		env.Fund(alice, becky)
		env.Close()

		env.IncLedgerSeqForAccDel(alice)

		before := captureDeleteBalances(env, alice, becky)
		result := env.Submit(newAccountDelete(env, alice, becky))
		requireAccountDeleteSuccess(t, env, result, alice, becky, before)
	})
}

// TestAccountDelete_TrustLineBlocks tests that the RippleState object itself is
// an obligation, regardless of whether its balance is zero.
func TestAccountDelete_TrustLineBlocks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		balance float64
	}{
		{name: "zero balance"},
		{name: "non-zero balance", balance: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			becky := jtx.NewAccount("becky")
			gw := jtx.NewAccount("gw")
			env.Fund(alice, becky, gw)
			env.Close()

			jtx.RequireTxSuccess(t, env.Submit(trustset.TrustLine(becky, "USD", gw, "1000").Build()))
			env.Close()
			if tc.balance != 0 {
				env.PayIOU(gw, becky, gw, "USD", tc.balance)
				env.Close()
			}
			lineKey := keylet.Line(becky.ID, gw.ID, "USD")
			jtx.RequireLedgerEntryExists(t, env, lineKey)

			env.IncLedgerSeqForAccDel(becky)
			before := captureDeleteBalances(env, becky, alice)
			result := env.Submit(newAccountDelete(env, becky, alice))
			jtx.RequireTxFail(t, result, jtx.TecHAS_OBLIGATIONS)
			require.True(t, result.Applied)
			require.Equal(t, before.fee, result.Fee)
			jtx.RequireAccountExists(t, env, becky)
			jtx.RequireLedgerEntryExists(t, env, lineKey)
		})
	}
}

func TestAccountDelete_CascadeDeletesOffer(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	gw := jtx.NewAccount("gw")
	env.Fund(alice, becky, gw)
	env.Close()

	offerSequence := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCreate(alice, gw.IOU("USD", 1), jtx.XRPTxAmount(jtx.XRP(1))).Build()))
	env.Close()
	offerKey := keylet.Offer(alice.ID, offerSequence)
	offerData, err := env.LedgerEntry(offerKey)
	require.NoError(t, err)
	offer, err := state.ParseLedgerOffer(offerData)
	require.NoError(t, err)
	bookDirectory := keylet.Keylet{Key: offer.BookDirectory}
	jtx.RequireLedgerEntryExists(t, env, offerKey)
	jtx.RequireLedgerEntryExists(t, env, bookDirectory)
	jtx.RequireOwnerDirectoryContains(t, env, alice, offerKey.Key, true)

	env.IncLedgerSeqForAccDel(alice)
	submitAccountDeleteSuccess(t, env, alice, becky, offerKey)
	jtx.RequireLedgerEntryNotExists(t, env, bookDirectory)
}

// TestAccountDelete_EscrowBlocks tests that escrows block deletion.
// Reference: rippled AccountDelete_test.cpp testBasics (escrow section)
func TestAccountDelete_EscrowBlocks(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()

	finishTime := env.Now().Add(100 * time.Second)
	escrowKey := keylet.Escrow(alice.ID, env.Seq(alice))

	jtx.RequireTxSuccess(t, env.Submit(
		escrow.EscrowCreate(alice, becky, jtx.XRP(100)).
			FinishTime(finishTime).Build()))
	env.Close()
	jtx.RequireOwnerDirectoryContains(t, env, alice, escrowKey.Key, true)
	jtx.RequireOwnerDirectoryContains(t, env, becky, escrowKey.Key, true)

	env.IncLedgerSeqForAccDel(alice)
	requireAccountDeleteObligation(t, env, alice, becky, escrowKey)

	env.IncLedgerSeqForAccDel(becky)
	requireAccountDeleteObligation(t, env, becky, alice, escrowKey)
}

// TestAccountDelete_DestinationConstraints tests destination requirements.
// Reference: rippled AccountDelete_test.cpp testBasics (destination checks)
func TestAccountDelete_DestinationConstraints(t *testing.T) {
	t.Run("DestNotExist", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		nonExistent := jtx.NewAccount("nobody")
		env.Fund(alice)
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		d := acctx.NewAccountDelete(alice.Address, nonExistent.Address)
		d.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result := env.Submit(d)
		jtx.RequireTxFail(t, result, jtx.TecNO_DST)
	})

	t.Run("DestRequiresDstTag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		becky := jtx.NewAccount("becky")
		env.Fund(alice, becky)
		env.EnableRequireDest(becky)
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		d := acctx.NewAccountDelete(alice.Address, becky.Address)
		d.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result := env.Submit(d)
		jtx.RequireTxFail(t, result, jtx.TecDST_TAG_NEEDED)
	})

	t.Run("WithDestinationTag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		becky := jtx.NewAccount("becky")
		env.Fund(alice, becky)
		env.EnableRequireDest(becky)
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		d := acctx.NewAccountDelete(alice.Address, becky.Address)
		d.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		tag := uint32(42)
		d.DestinationTag = &tag
		before := captureDeleteBalances(env, alice, becky)
		result := env.Submit(d)
		requireAccountDeleteSuccess(t, env, result, alice, becky, before)
	})
}

// TestAccountDelete_DepositAuth tests deposit authorization requirements.
// Reference: rippled AccountDelete_test.cpp testBasics (deposit auth)
func TestAccountDelete_DepositAuth(t *testing.T) {
	t.Run("NotPreauthorized", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, carol)
		env.Close()

		env.EnableDepositAuth(carol)
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		result := env.Submit(newAccountDelete(env, alice, carol))
		jtx.RequireTxFail(t, result, jtx.TecNO_PERMISSION)
	})

	t.Run("Preauthorized", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		carol := jtx.NewAccount("carol")
		env.Fund(alice, carol)
		env.Close()

		env.EnableDepositAuth(carol)
		env.Preauthorize(carol, alice)
		env.Close()

		env.IncLedgerSeqForAccDel(alice)
		preauthKey := keylet.DepositPreauth(carol.ID, alice.ID)
		before := captureDeleteBalances(env, alice, carol)
		result := env.Submit(newAccountDelete(env, alice, carol))
		requireAccountDeleteSuccess(t, env, result, alice, carol, before)
		jtx.RequireLedgerEntryExists(t, env, preauthKey)
	})
}

// TestAccountDelete_SequenceDistanceEnforced verifies the 256-ledger sequence gap requirement.
// Reference: rippled AccountDelete_test.cpp testBasics (TooSoon/sequence)
func TestAccountDelete_SequenceDistanceEnforced(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()

	sequence := env.Seq(alice)
	for env.LedgerSeq() < sequence+254 {
		env.Close()
	}
	require.Equal(t, sequence+254, env.LedgerSeq())
	beforeFailure := captureDeleteBalances(env, alice, becky)
	result := env.Submit(newAccountDelete(env, alice, becky))
	jtx.RequireTxFail(t, result, "tecTOO_SOON")
	require.True(t, result.Applied)
	require.Equal(t, beforeFailure.fee, result.Fee)
	require.Equal(t, sequence+1, env.Seq(alice))
	require.Equal(t, beforeFailure.source-beforeFailure.fee, env.Balance(alice))
	require.Equal(t, beforeFailure.destination, env.Balance(becky))

	env.Close()
	env.Close()
	require.Equal(t, env.Seq(alice)+255, env.LedgerSeq())
	beforeSuccess := captureDeleteBalances(env, alice, becky)
	result = env.Submit(newAccountDelete(env, alice, becky))
	requireAccountDeleteSuccess(t, env, result, alice, becky, beforeSuccess)
}

// TestAccountDelete_MultiSign tests that account deletion works with multisig.
// Reference: rippled AccountDelete_test.cpp testBasics (msig section)
func TestAccountDelete_MultiSign(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, becky, carol)
	env.Close()

	env.SetSignerList(carol, 1, []jtx.TestSigner{{Account: alice, Weight: 1}, {Account: becky, Weight: 1}})
	env.Close()

	env.IncLedgerSeqForAccDel(carol)

	d := newAccountDelete(env, carol, becky)
	before := captureDeleteBalances(env, carol, becky)
	result := env.SubmitMultiSigned(d, []*jtx.Account{alice})
	requireAccountDeleteSuccess(t, env, result, carol, becky, before, keylet.SignerList(carol.ID))
}

// TestAccountDelete_Resurrection tests that a deleted account address can be reused.
// Reference: rippled AccountDelete_test.cpp testAccountDeleteResuraction
func TestAccountDelete_Resurrection(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	env.Fund(alice, becky)
	env.Close()

	env.IncLedgerSeqForAccDel(alice)
	submitAccountDeleteSuccess(t, env, alice, becky)

	reserveBase := env.ReserveBase()
	expectedSequence := env.LedgerSeq()
	jtx.RequireTxSuccess(t, env.Submit(payment.Pay(becky, alice, reserveBase).Build()))
	env.Close()

	accountData, err := env.LedgerEntry(keylet.Account(alice.ID))
	require.NoError(t, err)
	account, err := state.ParseAccountRoot(accountData)
	require.NoError(t, err)
	require.Equal(t, alice.Address, account.Account)
	require.Equal(t, expectedSequence, account.Sequence)
	require.Equal(t, reserveBase, account.Balance)
	require.Zero(t, account.OwnerCount)
}

// TestAccountDelete_RegularKey tests deletion when regular key is set.
// Reference: rippled AccountDelete_test.cpp testBasics (regkey section)
func TestAccountDelete_RegularKey(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	rk := jtx.NewAccount("rk")
	env.Fund(alice, becky, rk)
	env.Close()

	env.SetRegularKey(becky, rk)
	env.Close()

	env.IncLedgerSeqForAccDel(becky)

	// Becky deletes her account signed by regular key
	d := newAccountDelete(env, becky, alice)
	before := captureDeleteBalances(env, becky, alice)
	result := env.SubmitSignedWith(d, rk)
	requireAccountDeleteSuccess(t, env, result, becky, alice, before)
}

func TestAccountDelete_ClearsDestinationPasswordSpent(t *testing.T) {
	env := jtx.NewTestEnv(t)
	carol := jtx.NewAccount("carol")
	becky := jtx.NewAccount("becky")
	firstKey := jtx.NewAccount("first-key")
	secondKey := jtx.NewAccount("second-key")
	env.Fund(carol, becky, firstKey, secondKey)
	env.Close()

	setFirst := jtx.NewSetRegularKeyTx(becky, firstKey)
	setFirst.GetCommon().Fee = "0"
	jtx.RequireTxSuccess(t, env.Submit(setFirst))
	env.Close()
	require.NotZero(t, env.AccountInfo(becky).Flags&state.LsfPasswordSpent)

	env.IncLedgerSeqForAccDel(carol)
	submitAccountDeleteSuccess(t, env, carol, becky)
	require.Zero(t, env.AccountInfo(becky).Flags&state.LsfPasswordSpent)

	setSecond := jtx.NewSetRegularKeyTx(becky, secondKey)
	setSecond.GetCommon().Fee = "0"
	jtx.RequireTxSuccess(t, env.Submit(setSecond))
}

func TestAccountDelete_FeeAndFlags(t *testing.T) {
	tests := []struct {
		name string
		code jtx.TxResultCode
		edit func(*acctx.AccountDelete, *jtx.TestEnv)
	}{
		{
			name: "negative fee",
			code: jtx.TemBAD_FEE,
			edit: func(d *acctx.AccountDelete, _ *jtx.TestEnv) { d.Fee = "-1" },
		},
		{
			name: "invalid flags",
			code: jtx.TemINVALID_FLAG,
			edit: func(d *acctx.AccountDelete, _ *jtx.TestEnv) { d.SetFlags(0x00020000) },
		},
		{
			name: "base fee",
			code: "telINSUF_FEE_P",
			edit: func(d *acctx.AccountDelete, env *jtx.TestEnv) {
				d.Fee = fmt.Sprintf("%d", env.BaseFee())
			},
		},
		{
			name: "one drop below reserve increment",
			code: "telINSUF_FEE_P",
			edit: func(d *acctx.AccountDelete, env *jtx.TestEnv) {
				d.Fee = fmt.Sprintf("%d", env.ReserveIncrement()-1)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			becky := jtx.NewAccount("becky")
			env.Fund(alice, becky)
			env.Close()

			d := acctx.NewAccountDelete(alice.Address, becky.Address)
			if tc.edit != nil {
				tc.edit(d, env)
			}
			before := captureDeleteBalances(env, alice, becky)
			sequence := env.Seq(alice)
			result := env.Submit(d)
			jtx.RequireTxFail(t, result, tc.code)
			require.False(t, result.Applied)
			require.Zero(t, result.Fee)
			require.Equal(t, before.source, env.Balance(alice))
			require.Equal(t, before.destination, env.Balance(becky))
			require.Equal(t, sequence, env.Seq(alice))
		})
	}
}

func TestAccountDelete_BalanceBelowFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	destination := env.MasterAccount()
	env.FundAmount(alice, env.ReserveBase())
	env.Close()

	remaining := uint64(jtx.XRP(1))
	env.NoopWithFee(alice, env.Balance(alice)-remaining)
	env.Close()
	require.Equal(t, remaining, env.Balance(alice))
	require.Greater(t, env.ReserveIncrement(), remaining)

	before := captureDeleteBalances(env, alice, destination)
	result := env.Submit(newAccountDelete(env, alice, destination))
	jtx.RequireTxFail(t, result, jtx.TerINSUF_FEE_B)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Equal(t, before.source, env.Balance(alice))
	require.Equal(t, before.destination, env.Balance(destination))

	d := acctx.NewAccountDelete(alice.Address, destination.Address)
	d.Fee = fmt.Sprintf("%d", remaining)
	result = env.Submit(d)
	jtx.RequireTxFail(t, result, "telINSUF_FEE_P")
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Equal(t, before.source, env.Balance(alice))
	require.Equal(t, before.destination, env.Balance(destination))
}

// TestAccountDelete_CredentialExpiry verifies rippled's credential expiry
// semantics for AccountDelete: credentials::valid() in preclaim never checks
// expiry; only removeExpired inside verifyDepositPreauth does, with a strict
// closeTime > expiration comparison. Past the expiration the delete fails with
// tecEXPIRED and the expired credential SLE is deleted; at the boundary
// ParentCloseTime == Expiration the credential is still valid and the delete
// succeeds.
// Reference: rippled CredentialHelpers.cpp checkExpired()/removeExpired(),
// verifyDepositPreauth(); AccountDelete_test.cpp "Expired credentials".
func TestAccountDelete_CredentialExpiry(t *testing.T) {
	credType := "abcde"

	// Past expiration: tecEXPIRED, the account survives, and the expired
	// credential is deleted even though the transaction failed.
	t.Run("ExpiredCredential", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		carol := jtx.NewAccount("carol")
		john := jtx.NewAccount("john")
		env.Fund(alice, carol, john)
		env.Close()

		expiration := env.NowRipple() + 20
		jtx.RequireTxSuccess(t, env.Submit(
			credential.CredentialCreateText(carol, john, credType).Expiration(expiration).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(credential.CredentialAcceptText(john, carol, credType).Build()))
		env.Close()

		credIdx := depositpreauth.CredentialIndexHex(john, carol, credType)
		credK := keylet.Credential(john.ID, carol.ID, []byte(credType))

		// Advancing 256 ledgers also moves time far past the expiration.
		env.IncLedgerSeqForAccDel(john)

		d := newAccountDelete(env, john, alice)
		d.CredentialIDs = []string{credIdx}
		jtx.RequireTxFail(t, env.Submit(d), "tecEXPIRED")
		env.Close()

		jtx.RequireAccountExists(t, env, john)
		require.False(t, env.LedgerEntryExists(credK),
			"expired credential must be deleted on the tecEXPIRED path")
	})

	// Boundary: a credential expiring exactly at the parent close time is
	// still valid (removeExpired uses strict ">"), so the delete succeeds.
	t.Run("ExpirationAtParentCloseTime", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		carol := jtx.NewAccount("carol")
		john := jtx.NewAccount("john")
		env.Fund(alice, carol, john)
		env.Close()

		// Far enough out to stay in the future across the 256-ledger advance.
		expiration := env.NowRipple() + 20000
		jtx.RequireTxSuccess(t, env.Submit(
			credential.CredentialCreateText(carol, john, credType).Expiration(expiration).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(credential.CredentialAcceptText(john, carol, credType).Build()))
		env.Close()

		credIdx := depositpreauth.CredentialIndexHex(john, carol, credType)
		credK := keylet.Credential(john.ID, carol.ID, []byte(credType))

		// Alice requires deposit authorization, satisfied via the credential,
		// so the credential is load-bearing for the delete.
		env.EnableDepositAuth(alice)
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(depositpreauth.AuthCredentials(alice, []depositpreauth.AuthorizeCredentials{
			{Issuer: carol, CredTypeText: credType},
		}).Build()))
		env.Close()

		env.IncLedgerSeqForAccDel(john)
		env.CloseToParentCloseTime(expiration)

		d := newAccountDelete(env, john, alice)
		d.CredentialIDs = []string{credIdx}
		before := captureDeleteBalances(env, john, alice)
		result := env.Submit(d)
		requireAccountDeleteSuccess(t, env, result, john, alice, before, credK)
		env.Close()

		require.False(t, env.LedgerEntryExists(credK),
			"credential must be cascade-deleted with the account")
	})
}
