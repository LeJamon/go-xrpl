// Package escrow_test contains integration tests for MPT (Multi-Purpose Token) Escrow.
// Tests ported from rippled's EscrowToken_test.cpp, testMPT* functions.
package escrow_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// mptAmount creates an MPT tx.Amount with the issuance ID set so IsMPT() returns true.
func mptAmount(value int64, issuerAddr, issuanceID string) state.Amount {
	return state.NewMPTAmountWithIssuanceID(value, issuerAddr, issuanceID)
}

// --------------------------------------------------------------------------
// TestMPTEscrow_Enablement
// Reference: rippled EscrowToken_test.cpp testMPTEnablement (lines 2124-2181)
// --------------------------------------------------------------------------

func TestMPTEscrow_Enablement(t *testing.T) {
	t.Run("WithoutTokenEscrow", func(t *testing.T) {
		// Without FeatureTokenEscrow → temBAD_AMOUNT for create
		env := jtx.NewTestEnv(t)
		env.DisableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(bob, uint64(xrp(5000)))

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		amt := mptAmount(1_000, gw.Address, mptGw.IssuanceID())

		// EscrowCreate should fail with temBAD_AMOUNT
		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TemBAD_AMOUNT)
		env.Close()

		// EscrowFinish on non-existent escrow should fail with tecNO_TARGET
		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq1).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_TARGET)
		env.Close()

		// Second escrow create attempt (with cancel time) → temBAD_AMOUNT
		seq2 := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition2()).
				FinishTime(env.Now().Add(1 * time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TemBAD_AMOUNT)
		env.Close()

		// Cancel on non-existent escrow → tecNO_TARGET
		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq2).Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_TARGET)
		env.Close()
	})

	t.Run("WithTokenEscrow", func(t *testing.T) {
		// With both features → tesSUCCESS
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(bob, uint64(xrp(5000)))

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		amt := mptAmount(1_000, gw.Address, mptGw.IssuanceID())

		// Create + finish with condition
		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq1).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create + cancel with condition and cancel time
		seq2 := env.Seq(alice)
		result = env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition2()).
				FinishTime(env.Now().Add(1 * time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq2).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestMPTEscrow_CreatePreflight
// Reference: rippled EscrowToken_test.cpp testMPTCreatePreflight (lines 2184-2243)
// --------------------------------------------------------------------------

func TestMPTEscrow_CreatePreflight(t *testing.T) {
	t.Run("WithoutMPTokensV1", func(t *testing.T) {
		// Without FeatureMPTokensV1 → temDISABLED
		// Reference: rippled lines 2190-2213
		// Note: use a positive amount so that stateless Validate() passes
		// and the amendment check in escrowCreatePreclaimMPT fires.
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")
		env.DisableFeature("MPTokensV1")
		env.Close()

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(alice, uint64(xrp(1_000)))
		env.FundAmount(bob, uint64(xrp(1_000)))
		env.FundAmount(gw, uint64(xrp(1_000)))

		// Use a fake MPT issuance ID with gw as issuer (not alice, to pass the issuer!=account check)
		fakeID := "00000004A407AF5856CCF3C42619DAA925813FC955C72983"
		amt := mptAmount(1, gw.Address, fakeID)

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TemDISABLED)
		env.Close()
	})

	t.Run("WithMPTokensV1_NegativeAmount", func(t *testing.T) {
		// With MPTokensV1 + negative amount → temBAD_AMOUNT
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Pay(gw, alice, 10_000)
		mptGw.Pay(gw, bob, 10_000)
		env.Close()

		// Negative MPT amount
		amt := mptAmount(-1, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TemBAD_AMOUNT)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestMPTEscrow_CanEscrowFlag
// Reference: rippled EscrowToken_test.cpp testMPTCreatePreclaim (lines 2300-2324)
// --------------------------------------------------------------------------

func TestMPTEscrow_CanEscrowFlag(t *testing.T) {
	// MPTIssuance without lsfMPTCanEscrow → tecNO_PERMISSION
	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")

	mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
		Holders: []*jtx.Account{alice, bob},
	})
	// Create WITHOUT lsfMPTCanEscrow (only CanTransfer)
	mptGw.Create(mpt.CreateOpts{
		OwnerCount: mpt.PtrUint32(1),
		Flags:      mpt.TfMPTCanTransfer,
	})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
	mptGw.Pay(gw, alice, 10_000)
	mptGw.Pay(gw, bob, 10_000)
	env.Close()

	amt := mptAmount(3, gw.Address, mptGw.IssuanceID())

	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			MPTAmount(amt).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxFail(t, result, jtx.TecNO_PERMISSION)
	env.Close()
}

// --------------------------------------------------------------------------
// TestMPTEscrow_CreatePreclaim
// Reference: rippled EscrowToken_test.cpp testMPTCreatePreclaim (lines 2246-2558)
// --------------------------------------------------------------------------

func TestMPTEscrow_CreatePreclaim(t *testing.T) {
	t.Run("IssuerCannotEscrow", func(t *testing.T) {
		// tecNO_PERMISSION: issuer is the same as the account
		// Reference: rippled lines 2252-2275
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		amt := mptAmount(1, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(gw, alice, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_PERMISSION)
		env.Close()
	})

	t.Run("IssuanceDoesNotExist", func(t *testing.T) {
		// tecOBJECT_NOT_FOUND: MPT issuance does not exist
		// Reference: rippled lines 2277-2298
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(alice, uint64(xrp(10_000)))
		env.FundAmount(bob, uint64(xrp(10_000)))
		env.FundAmount(gw, uint64(xrp(10_000)))
		env.Close()

		// Use a fake issuance ID with gw as issuer — issuance doesn't exist on ledger
		fakeID := "00000004A407AF5856CCF3C42619DAA925813FC955C72983"
		amt := mptAmount(2, gw.Address, fakeID)

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecOBJECT_NOT_FOUND)
		env.Close()
	})

	t.Run("NoMPToken", func(t *testing.T) {
		// tecOBJECT_NOT_FOUND: account does not hold the MPT (no MPToken created)
		// Reference: rippled lines 2326-2347
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		// Do NOT authorize alice — she won't have an MPToken
		env.Close()

		amt := mptAmount(4, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecOBJECT_NOT_FOUND)
		env.Close()
	})

	t.Run("AccountNotAuthorized", func(t *testing.T) {
		// tecNO_AUTH: requireAuth set, account not authorized by issuer
		// Reference: rippled lines 2349-2379
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTRequireAuth,
		})
		// Authorize alice and have issuer authorize alice
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		// Now UN-authorize alice
		mptGw.Authorize(mpt.AuthorizeOpts{
			Account: gw,
			Holder:  alice,
			Flags:   mpt.TfMPTUnauthorize,
		})

		amt := mptAmount(5, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_AUTH)
		env.Close()
	})

	t.Run("DestNotAuthorized", func(t *testing.T) {
		// tecNO_AUTH: requireAuth set, dest not authorized by issuer
		// Reference: rippled lines 2381-2414
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTRequireAuth,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: bob})
		mptGw.Pay(gw, alice, 10_000)
		mptGw.Pay(gw, bob, 10_000)
		env.Close()

		// UN-authorize dest (bob)
		mptGw.Authorize(mpt.AuthorizeOpts{
			Account: gw,
			Holder:  bob,
			Flags:   mpt.TfMPTUnauthorize,
		})

		amt := mptAmount(6, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_AUTH)
		env.Close()
	})

	t.Run("FrozenMPT_Account", func(t *testing.T) {
		// tecLOCKED: issuer has locked the account's MPToken
		// Reference: rippled lines 2416-2445
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTCanLock,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Pay(gw, alice, 10_000)
		mptGw.Pay(gw, bob, 10_000)
		env.Close()

		// Lock alice's MPToken
		mptGw.Set(mpt.SetOpts{Account: gw, Holder: alice, Flags: mpt.TfMPTLock})

		amt := mptAmount(7, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecLOCKED)
		env.Close()
	})

	t.Run("FrozenMPT_Dest", func(t *testing.T) {
		// tecLOCKED: issuer has locked the dest's MPToken
		// Reference: rippled lines 2447-2476
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTCanLock,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Pay(gw, alice, 10_000)
		mptGw.Pay(gw, bob, 10_000)
		env.Close()

		// Lock bob's (dest) MPToken
		mptGw.Set(mpt.SetOpts{Account: gw, Holder: bob, Flags: mpt.TfMPTLock})

		amt := mptAmount(8, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecLOCKED)
		env.Close()
	})

	t.Run("CannotTransfer", func(t *testing.T) {
		// tecNO_AUTH: MPT cannot be transferred (tfMPTCanTransfer not set)
		// Reference: rippled lines 2478-2502
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		// Create WITH CanEscrow but WITHOUT CanTransfer
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Pay(gw, alice, 10_000)
		mptGw.Pay(gw, bob, 10_000)
		env.Close()

		amt := mptAmount(9, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_AUTH)
		env.Close()
	})

	t.Run("InsufficientFunds_Zero", func(t *testing.T) {
		// tecINSUFFICIENT_FUNDS: spendable amount is zero
		// Reference: rippled lines 2504-2529
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		// Only give bob some tokens, alice has zero
		mptGw.Pay(gw, bob, 10)
		env.Close()

		amt := mptAmount(11, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecINSUFFICIENT_FUNDS)
		env.Close()
	})

	t.Run("InsufficientFunds_LessThanAmount", func(t *testing.T) {
		// tecINSUFFICIENT_FUNDS: spendable amount is less than escrow amount
		// Reference: rippled lines 2531-2558
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Pay(gw, alice, 10)
		mptGw.Pay(gw, bob, 10)
		env.Close()

		// Escrow 11 but alice only has 10
		amt := mptAmount(11, gw.Address, mptGw.IssuanceID())

		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecINSUFFICIENT_FUNDS)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestMPTEscrow_FinishDoApply
// Reference: rippled EscrowToken_test.cpp testMPTFinishDoApply (lines 2684-2801)
// --------------------------------------------------------------------------

func TestMPTEscrow_FinishDoApply(t *testing.T) {
	t.Run("BobSubmitsFinish", func(t *testing.T) {
		// tesSUCCESS: bob submits finish, MPToken created for bob
		// Reference: rippled lines 2729-2763
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(bob, uint64(xrp(10_000)))
		env.Close()

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		amt := mptAmount(10, gw.Address, mptGw.IssuanceID())

		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq1).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("CarolCannotFinish", func(t *testing.T) {
		// tecNO_PERMISSION: carol (not sender or dest) cannot finish MPT escrow
		// Reference: rippled lines 2765-2800
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		gw := jtx.NewAccount("gw")
		env.FundAmount(bob, uint64(xrp(10_000)))
		env.FundAmount(carol, uint64(xrp(10_000)))
		env.Close()

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		amt := mptAmount(10, gw.Address, mptGw.IssuanceID())

		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Carol tries to finish — should fail
		result = env.Submit(
			escrow.EscrowFinish(carol, alice, seq1).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_PERMISSION)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestMPTEscrow_FinishBasic
// Reference: rippled EscrowToken_test.cpp testMPTBalances (lines 2881-2945)
// --------------------------------------------------------------------------

func TestMPTEscrow_FinishBasic(t *testing.T) {
	for _, fixEnabled := range []bool{false, true} {
		name := "WithoutFixTokenEscrowV1"
		if fixEnabled {
			name = "WithFixTokenEscrowV1"
		}
		t.Run(name, func(t *testing.T) {
			testMPTEscrowFinishBasic(t, fixEnabled)
		})
	}
}

func testMPTEscrowFinishBasic(t *testing.T, fixEnabled bool) {
	t.Helper()
	// Create MPT escrow, advance time, finish
	// Verify: sender's MPT balance decreased, receiver's increased
	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")
	if !fixEnabled {
		env.DisableFeature("fixTokenEscrowV1")
	}

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	env.FundAmount(bob, uint64(xrp(5000)))

	mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
		Holders: []*jtx.Account{alice},
	})
	mptGw.Create(mpt.CreateOpts{
		OwnerCount: mpt.PtrUint32(1),
		Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
	})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
	mptGw.Pay(gw, alice, 10_000)
	env.Close()
	aliceOwnerCount := env.OwnerCount(alice)
	bobOwnerCount := env.OwnerCount(bob)
	gwOwnerCount := env.OwnerCount(gw)
	aliceDirEntries := ownerDirEntryCount(t, env, alice)
	bobDirEntries := ownerDirEntryCount(t, env, bob)
	gwDirEntries := ownerDirEntryCount(t, env, gw)

	amt := mptAmount(1_000, gw.Address, mptGw.IssuanceID())

	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			MPTAmount(amt).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(alice.ID, seq1)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	require.True(t, env.LedgerEntryExists(escrowKey))
	require.Equal(t, aliceOwnerCount+1, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, true)
	require.Equal(t, aliceDirEntries+1, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries+1, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries, ownerDirEntryCount(t, env, gw))
	mptGw.RequireMPTokenAmount(alice, 9_000)
	require.Equal(t, uint64(1_000), mptGw.HolderLockedAmount(alice))
	require.Equal(t, uint64(1_000), mptGw.IssuanceLockedAmount())
	require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
	env.Close()

	// Finish the escrow
	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq1).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
	env.Close()

	require.False(t, env.LedgerEntryExists(escrowKey))
	require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount+1, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, false)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, false)
	require.Equal(t, aliceDirEntries, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries+1, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries, ownerDirEntryCount(t, env, gw))
	mptGw.RequireMPTokenAmount(alice, 9_000)
	mptGw.RequireMPTokenAmount(bob, 1_000)
	require.Zero(t, mptGw.HolderLockedAmount(alice))
	require.Zero(t, mptGw.IssuanceLockedAmount())
	require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
}

// --------------------------------------------------------------------------
// TestMPTEscrow_CancelBasic
// Reference: rippled EscrowToken_test.cpp testMPTBalances (lines 2947-2979)
// --------------------------------------------------------------------------

func TestMPTEscrow_CancelBasic(t *testing.T) {
	for _, fixEnabled := range []bool{false, true} {
		name := "WithoutFixTokenEscrowV1"
		if fixEnabled {
			name = "WithFixTokenEscrowV1"
		}
		t.Run(name, func(t *testing.T) {
			testMPTEscrowCancelBasic(t, fixEnabled)
		})
	}
}

func testMPTEscrowCancelBasic(t *testing.T, fixEnabled bool) {
	t.Helper()
	// Create MPT escrow, cancel it
	// Verify: amount returned to sender
	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")
	if !fixEnabled {
		env.DisableFeature("fixTokenEscrowV1")
	}

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	env.FundAmount(bob, uint64(xrp(5000)))

	mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
		Holders: []*jtx.Account{alice},
	})
	mptGw.Create(mpt.CreateOpts{
		OwnerCount: mpt.PtrUint32(1),
		Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
	})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
	mptGw.Pay(gw, alice, 10_000)
	env.Close()
	aliceOwnerCount := env.OwnerCount(alice)
	bobOwnerCount := env.OwnerCount(bob)
	gwOwnerCount := env.OwnerCount(gw)
	aliceDirEntries := ownerDirEntryCount(t, env, alice)
	bobDirEntries := ownerDirEntryCount(t, env, bob)
	gwDirEntries := ownerDirEntryCount(t, env, gw)

	amt := mptAmount(1_000, gw.Address, mptGw.IssuanceID())

	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			MPTAmount(amt).
			Condition(escrow.TestCondition2()).
			FinishTime(env.Now().Add(1 * time.Second)).
			CancelTime(env.Now().Add(2 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	escrowKey := keylet.Escrow(alice.ID, seq1)
	requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
	require.True(t, env.LedgerEntryExists(escrowKey))
	require.Equal(t, aliceOwnerCount+1, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, true)
	require.Equal(t, aliceDirEntries+1, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries+1, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries, ownerDirEntryCount(t, env, gw))
	mptGw.RequireMPTokenAmount(alice, 9_000)
	require.Equal(t, uint64(1_000), mptGw.HolderLockedAmount(alice))
	require.Equal(t, uint64(1_000), mptGw.IssuanceLockedAmount())
	require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
	env.Close()

	// Cancel the escrow
	result = env.Submit(
		escrow.EscrowCancel(bob, alice, seq1).Build())
	jtx.RequireTxSuccess(t, result)
	requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
	env.Close()

	require.False(t, env.LedgerEntryExists(escrowKey))
	require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
	require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
	require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
	requireOwnerDirContains(t, env, alice, escrowKey.Key, false)
	requireOwnerDirContains(t, env, bob, escrowKey.Key, false)
	require.Equal(t, aliceDirEntries, ownerDirEntryCount(t, env, alice))
	require.Equal(t, bobDirEntries, ownerDirEntryCount(t, env, bob))
	require.Equal(t, gwDirEntries, ownerDirEntryCount(t, env, gw))
	mptGw.RequireMPTokenAmount(alice, 10_000)
	mptGw.RequireMPTokenAmount(bob, 0)
	require.Zero(t, mptGw.HolderLockedAmount(alice))
	require.Zero(t, mptGw.IssuanceLockedAmount())
	require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
}

func requireOwnerDirContains(
	t *testing.T,
	env *jtx.TestEnv,
	owner *jtx.Account,
	target [32]byte,
	want bool,
) {
	t.Helper()
	found := false
	err := state.DirForEach(env.Ledger(), keylet.OwnerDir(owner.ID), func(item [32]byte) error {
		if item == target {
			found = true
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, want, found, "owner directory membership mismatch for %s", owner.Name)
}

func ownerDirEntryCount(t *testing.T, env *jtx.TestEnv, owner *jtx.Account) int {
	t.Helper()
	count := 0
	err := state.DirForEach(env.Ledger(), keylet.OwnerDir(owner.ID), func([32]byte) error {
		count++
		return nil
	})
	require.NoError(t, err)
	return count
}

func requireEscrowMetaNode(t *testing.T, result jtx.TxResult, nodeType string, escrowKey keylet.Keylet) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	nodes := metadata.FindNodes(result.Metadata, nodeType, "Escrow")
	require.Len(t, nodes, 1)
	require.Equal(t, fmt.Sprintf("%X", escrowKey.Key), nodes[0].LedgerIndex)
	if nodeType == "CreatedNode" {
		require.NotNil(t, nodes[0].NewFields)
		require.Nil(t, nodes[0].FinalFields)
		return
	}
	require.NotNil(t, nodes[0].FinalFields)
	require.Nil(t, nodes[0].NewFields)
}

// --------------------------------------------------------------------------
// TestMPTEscrow_SelfEscrow
// Reference: rippled EscrowToken_test.cpp testMPTBalances (lines 2981-3034)
// --------------------------------------------------------------------------

func TestMPTEscrow_SelfEscrow(t *testing.T) {
	t.Run("SelfFinish", func(t *testing.T) {
		// Self escrow create & finish (alice → alice)
		// Reference: rippled lines 2982-3008
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()
		aliceOwnerCount := env.OwnerCount(alice)
		gwOwnerCount := env.OwnerCount(gw)

		amt := mptAmount(1_000, gw.Address, mptGw.IssuanceID())

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, alice, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		escrowKey := keylet.Escrow(alice.ID, seq)
		requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
		require.True(t, env.LedgerEntryExists(escrowKey))
		requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
		require.Equal(t, aliceOwnerCount+1, env.OwnerCount(alice))
		require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
		mptGw.RequireMPTokenAmount(alice, 9_000)
		require.Equal(t, uint64(1_000), mptGw.HolderLockedAmount(alice))
		require.Equal(t, uint64(1_000), mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
		env.Close()

		result = env.Submit(
			escrow.EscrowFinish(alice, alice, seq).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
		env.Close()
		require.False(t, env.LedgerEntryExists(escrowKey))
		requireOwnerDirContains(t, env, alice, escrowKey.Key, false)
		require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
		require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
		mptGw.RequireMPTokenAmount(alice, 10_000)
		require.Zero(t, mptGw.HolderLockedAmount(alice))
		require.Zero(t, mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
	})

	t.Run("SelfCancel", func(t *testing.T) {
		// Self escrow create & cancel (alice → alice)
		// Reference: rippled lines 3010-3034
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()
		aliceOwnerCount := env.OwnerCount(alice)
		gwOwnerCount := env.OwnerCount(gw)

		amt := mptAmount(1_000, gw.Address, mptGw.IssuanceID())

		seq := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, alice, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		escrowKey := keylet.Escrow(alice.ID, seq)
		requireEscrowMetaNode(t, result, "CreatedNode", escrowKey)
		require.True(t, env.LedgerEntryExists(escrowKey))
		requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
		require.Equal(t, aliceOwnerCount+1, env.OwnerCount(alice))
		require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
		mptGw.RequireMPTokenAmount(alice, 9_000)
		require.Equal(t, uint64(1_000), mptGw.HolderLockedAmount(alice))
		require.Equal(t, uint64(1_000), mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
		env.Close()

		result = env.Submit(
			escrow.EscrowCancel(alice, alice, seq).Build())
		jtx.RequireTxSuccess(t, result)
		requireEscrowMetaNode(t, result, "DeletedNode", escrowKey)
		env.Close()
		require.False(t, env.LedgerEntryExists(escrowKey))
		requireOwnerDirContains(t, env, alice, escrowKey.Key, false)
		require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
		require.Equal(t, gwOwnerCount, env.OwnerCount(gw))
		mptGw.RequireMPTokenAmount(alice, 10_000)
		require.Zero(t, mptGw.HolderLockedAmount(alice))
		require.Zero(t, mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
	})
}

// --------------------------------------------------------------------------
// TestMPTEscrow_FinishPreclaim
// Reference: rippled EscrowToken_test.cpp testMPTFinishPreclaim (lines 2561-2680)
// --------------------------------------------------------------------------

func TestMPTEscrow_FinishPreclaim(t *testing.T) {
	t.Run("DestDeauthorizedAfterCreate", func(t *testing.T) {
		// tecNO_AUTH: dest deauthorized after escrow was created
		// Reference: rippled lines 2567-2608
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTRequireAuth,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: bob})
		mptGw.Pay(gw, alice, 10_000)
		mptGw.Pay(gw, bob, 10_000)
		env.Close()

		amt := mptAmount(10, gw.Address, mptGw.IssuanceID())

		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		escrowKey := keylet.Escrow(alice.ID, seq1)
		aliceOwnerCount := env.OwnerCount(alice)
		bobOwnerCount := env.OwnerCount(bob)
		aliceDirEntries := ownerDirEntryCount(t, env, alice)
		bobDirEntries := ownerDirEntryCount(t, env, bob)

		// UN-authorize dest after escrow creation
		mptGw.Authorize(mpt.AuthorizeOpts{
			Account: gw,
			Holder:  bob,
			Flags:   mpt.TfMPTUnauthorize,
		})

		result = env.Submit(
			escrow.EscrowFinish(bob, alice, seq1).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_AUTH)
		env.Close()
		require.True(t, env.LedgerEntryExists(escrowKey))
		requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
		requireOwnerDirContains(t, env, bob, escrowKey.Key, true)
		require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
		require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
		require.Equal(t, aliceDirEntries, ownerDirEntryCount(t, env, alice))
		require.Equal(t, bobDirEntries, ownerDirEntryCount(t, env, bob))
		mptGw.RequireMPTokenAmount(alice, 9_990)
		mptGw.RequireMPTokenAmount(bob, 10_000)
		require.Equal(t, uint64(10), mptGw.HolderLockedAmount(alice))
		require.Equal(t, uint64(10), mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(20_000), mptGw.IssuanceOutstandingAmount())
	})
}

// --------------------------------------------------------------------------
// TestMPTEscrow_CancelPreclaim
// Reference: rippled EscrowToken_test.cpp testMPTCancelPreclaim (lines 2804-2878)
// --------------------------------------------------------------------------

func TestMPTEscrow_CancelPreclaim(t *testing.T) {
	t.Run("AccountDeauthorizedAfterCreate", func(t *testing.T) {
		// tecNO_AUTH: account deauthorized after escrow was created
		// Reference: rippled lines 2810-2847
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
			Holders: []*jtx.Account{alice, bob},
		})
		mptGw.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTRequireAuth,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: bob})
		mptGw.Pay(gw, alice, 10_000)
		mptGw.Pay(gw, bob, 10_000)
		env.Close()

		amt := mptAmount(10, gw.Address, mptGw.IssuanceID())

		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, bob, 0).
				MPTAmount(amt).
				CancelTime(env.Now().Add(2 * time.Second)).
				Condition(escrow.TestCondition1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		escrowKey := keylet.Escrow(alice.ID, seq1)
		aliceOwnerCount := env.OwnerCount(alice)
		bobOwnerCount := env.OwnerCount(bob)
		aliceDirEntries := ownerDirEntryCount(t, env, alice)
		bobDirEntries := ownerDirEntryCount(t, env, bob)

		// UN-authorize account (alice) after escrow creation
		mptGw.Authorize(mpt.AuthorizeOpts{
			Account: gw,
			Holder:  alice,
			Flags:   mpt.TfMPTUnauthorize,
		})

		result = env.Submit(
			escrow.EscrowCancel(bob, alice, seq1).Build())
		jtx.RequireTxFail(t, result, jtx.TecNO_AUTH)
		env.Close()
		require.True(t, env.LedgerEntryExists(escrowKey))
		requireOwnerDirContains(t, env, alice, escrowKey.Key, true)
		requireOwnerDirContains(t, env, bob, escrowKey.Key, true)
		require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
		require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
		require.Equal(t, aliceDirEntries, ownerDirEntryCount(t, env, alice))
		require.Equal(t, bobDirEntries, ownerDirEntryCount(t, env, bob))
		mptGw.RequireMPTokenAmount(alice, 9_990)
		mptGw.RequireMPTokenAmount(bob, 10_000)
		require.Equal(t, uint64(10), mptGw.HolderLockedAmount(alice))
		require.Equal(t, uint64(10), mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(20_000), mptGw.IssuanceOutstandingAmount())
	})
}

func TestMPTEscrow_TransferFeeRounding(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
		Holders: []*jtx.Account{alice, bob},
	})
	mptGw.Create(mpt.CreateOpts{
		TransferFee: mpt.PtrUint16(100),
		OwnerCount:  mpt.PtrUint32(1),
		Flags:       mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
	})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
	mptGw.Pay(gw, alice, 100_000)
	env.Close()

	mptGw.RequireMPTokenAmount(alice, 100_000)
	mptGw.RequireMPTokenAmount(bob, 0)
	require.Equal(t, uint64(100_000), mptGw.IssuanceOutstandingAmount())

	amt := mptAmount(10_000, gw.Address, mptGw.IssuanceID())
	seq := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			MPTAmount(amt).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	mptGw.RequireMPTokenAmount(alice, 90_000)
	mptGw.RequireMPTokenAmount(bob, 0)
	require.Equal(t, uint64(10_000), mptGw.HolderLockedAmount(alice))
	require.Equal(t, uint64(10_000), mptGw.IssuanceLockedAmount())
	require.Equal(t, uint64(100_000), mptGw.IssuanceOutstandingAmount())

	result = env.Submit(
		escrow.EscrowFinish(bob, alice, seq).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	mptGw.RequireMPTokenAmount(alice, 90_000)
	mptGw.RequireMPTokenAmount(bob, 9_990)
	finalOutstanding := mptGw.IssuanceOutstandingAmount()
	require.Equal(t, uint64(99_990), finalOutstanding)
	require.Equal(t, uint64(10), uint64(100_000)-finalOutstanding, "transfer fee burn")

	mptID, err := mptutil.DecodeID(mptGw.IssuanceID())
	require.NoError(t, err)
	issuanceData, err := env.Ledger().Read(keylet.MPTIssuance(mptID))
	require.NoError(t, err)
	issuance, err := state.ParseMPTokenIssuance(issuanceData)
	require.NoError(t, err)
	require.Nil(t, issuance.LockedAmount, "issuance LockedAmount must be removed")

	tokenData, err := env.Ledger().Read(keylet.MPTokenByID(mptID, alice.ID))
	require.NoError(t, err)
	token, err := state.ParseMPToken(tokenData)
	require.NoError(t, err)
	require.Nil(t, token.LockedAmount, "holder LockedAmount must be removed")
}

// --------------------------------------------------------------------------
// TestMPTEscrow_LockedRate
// Reference: rippled EscrowToken_test.cpp testMPTLockedRate (lines 3458-3625)
//
// Exercises the transfer-fee accounting when unlocking escrowed MPT.
// fixTokenEscrowV1 clears the full (gross) locked amount from both the sender's
// MPToken and the issuance, and burns the fee from OutstandingAmount. Without the
// fix the fee stays stuck as a leftover LockedAmount and supply is never reduced.
// --------------------------------------------------------------------------

func lockedRateSetup(t *testing.T, env *jtx.TestEnv, alice, bob, gw *jtx.Account) *mpt.MPTTester {
	t.Helper()
	mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{
		Holders: []*jtx.Account{alice, bob},
	})
	mptGw.Create(mpt.CreateOpts{
		TransferFee: mpt.PtrUint16(25000),
		OwnerCount:  mpt.PtrUint32(1),
		Flags:       mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
	})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
	mptGw.Authorize(mpt.AuthorizeOpts{Account: bob})
	mptGw.Pay(gw, alice, 10_000)
	mptGw.Pay(gw, bob, 10_000)
	env.Close()
	return mptGw
}

func TestMPTEscrow_LockedRate(t *testing.T) {
	t.Run("Finish", func(t *testing.T) {
		for _, fixEnabled := range []bool{true, false} {
			name := "WithFix"
			if !fixEnabled {
				name = "WithoutFix"
			}
			t.Run(name, func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				env.EnableFeature("TokenEscrow")
				if !fixEnabled {
					env.DisableFeature("fixTokenEscrowV1")
				}

				alice := jtx.NewAccount("alice")
				bob := jtx.NewAccount("bob")
				gw := jtx.NewAccount("gw")
				mptGw := lockedRateSetup(t, env, alice, bob, gw)

				amt := mptAmount(125, gw.Address, mptGw.IssuanceID())
				seq1 := env.Seq(alice)
				result := env.Submit(
					escrow.EscrowCreate(alice, bob, 0).
						MPTAmount(amt).
						Condition(escrow.TestCondition1()).
						FinishTime(env.Now().Add(1 * time.Second)).
						Fee(env.BaseFee() * 150).
						Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// After create: the full gross amount is locked; supply unchanged.
				require.Equal(t, uint64(125), mptGw.HolderLockedAmount(alice))
				require.Equal(t, uint64(125), mptGw.IssuanceLockedAmount())
				require.Equal(t, uint64(20_000), mptGw.IssuanceOutstandingAmount())
				mptGw.RequireMPTokenAmount(alice, 9_875)

				result = env.Submit(
					escrow.EscrowFinish(bob, alice, seq1).
						Condition(escrow.TestCondition1()).
						Fulfillment(escrow.TestFulfillment1()).
						Fee(env.BaseFee() * 150).
						Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// Receiver is credited the net-of-fee amount (100) in both cases.
				mptGw.RequireMPTokenAmount(alice, 9_875)
				mptGw.RequireMPTokenAmount(bob, 10_100)

				wantEscrowed, wantOutstanding := uint64(0), uint64(19_975)
				if !fixEnabled {
					wantEscrowed, wantOutstanding = 25, 20_000
				}
				require.Equal(t, wantEscrowed, mptGw.HolderLockedAmount(alice),
					"sender locked amount")
				require.Equal(t, wantEscrowed, mptGw.IssuanceLockedAmount(),
					"issuance locked amount")
				require.Equal(t, wantOutstanding, mptGw.IssuanceOutstandingAmount(),
					"issuance outstanding amount")
			})
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		for _, fixEnabled := range []bool{false, true} {
			name := "WithoutFix"
			if fixEnabled {
				name = "WithFix"
			}
			t.Run(name, func(t *testing.T) {
				testMPTEscrowLockedRateCancel(t, fixEnabled)
			})
		}
	})

	t.Run("IssuerIsDestination", func(t *testing.T) {
		// When the issuer is the receiver no transfer fee applies: the full amount
		// is redeemed and removed from OutstandingAmount.
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		mptGw := lockedRateSetup(t, env, alice, bob, gw)

		amt := mptAmount(125, gw.Address, mptGw.IssuanceID())
		seq1 := env.Seq(alice)
		result := env.Submit(
			escrow.EscrowCreate(alice, gw, 0).
				MPTAmount(amt).
				Condition(escrow.TestCondition1()).
				FinishTime(env.Now().Add(1 * time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(125), mptGw.HolderLockedAmount(alice))
		require.Equal(t, uint64(125), mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(20_000), mptGw.IssuanceOutstandingAmount())

		result = env.Submit(
			escrow.EscrowFinish(gw, alice, seq1).
				Condition(escrow.TestCondition1()).
				Fulfillment(escrow.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		mptGw.RequireMPTokenAmount(alice, 9_875)
		require.Equal(t, uint64(0), mptGw.HolderLockedAmount(alice))
		require.Equal(t, uint64(0), mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(19_875), mptGw.IssuanceOutstandingAmount())
	})
}

func testMPTEscrowLockedRateCancel(t *testing.T, fixEnabled bool) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")
	if !fixEnabled {
		env.DisableFeature("fixTokenEscrowV1")
	}

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	mptGw := lockedRateSetup(t, env, alice, bob, gw)

	amt := mptAmount(125, gw.Address, mptGw.IssuanceID())
	seq1 := env.Seq(alice)
	result := env.Submit(
		escrow.EscrowCreate(alice, bob, 0).
			MPTAmount(amt).
			Condition(escrow.TestCondition1()).
			FinishTime(env.Now().Add(1 * time.Second)).
			CancelTime(env.Now().Add(2 * time.Second)).
			Fee(env.BaseFee() * 150).
			Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	result = env.Submit(escrow.EscrowCancel(alice, alice, seq1).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	mptGw.RequireMPTokenAmount(alice, 10_000)
	mptGw.RequireMPTokenAmount(bob, 10_000)
	require.Zero(t, mptGw.HolderLockedAmount(alice))
	require.Zero(t, mptGw.IssuanceLockedAmount())
	require.Equal(t, uint64(20_000), mptGw.IssuanceOutstandingAmount())
}
