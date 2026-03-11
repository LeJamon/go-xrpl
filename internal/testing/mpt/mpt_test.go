// Package mpt_test contains integration tests for MPT (Multi-Purpose Token) transaction behavior.
// Tests ported from rippled's MPToken_test.cpp (rippled/src/test/app/MPToken_test.cpp).
// Each test function maps 1:1 to a rippled test method.
package mpt_test

import (
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/mpt"
)

// --------------------------------------------------------------------------
// TestMPT_CreateValidation
// Reference: rippled MPToken_test.cpp testCreateValidation() (lines 38-158)
// --------------------------------------------------------------------------

func TestMPT_CreateValidation(t *testing.T) {
	t.Run("FeatureDisabled", func(t *testing.T) {
		// Reference: rippled lines 46-53
		// If the MPT amendment is not enabled, you should not be able to create MPTokenIssuances
		env := jtx.NewTestEnv(t)
		env.DisableFeature("MPTokensV1")
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(0),
			Err:        jtx.TemDISABLED,
		})
	})

	t.Run("InvalidFlag", func(t *testing.T) {
		// Reference: rippled line 60
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		mptAlice.Create(mpt.CreateOpts{
			Flags: 0x00000001,
			Err:   jtx.TemINVALID_FLAG,
		})
	})

	t.Run("TransferFeeWithoutCanTransfer", func(t *testing.T) {
		// Reference: rippled lines 63-68
		// tries to set a txfee while not enabling transfer in the flag
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		maxAmt := uint64(100)
		assetScale := uint8(0)
		transferFee := uint16(1)
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString("74657374"), // "test" in hex
			Err:         jtx.TemMALFORMED,
		})
	})

	t.Run("TransferFeeExceedsMax", func(t *testing.T) {
		// Reference: rippled lines 113-119
		// tries to set a txfee greater than max
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		maxAmt := uint64(100)
		assetScale := uint8(0)
		transferFee := uint16(50001) // maxTransferFee + 1
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString("74657374"),
			Flags:       mpt.TfMPTCanTransfer,
			Err:         jtx.TemBAD_TRANSFER_FEE,
		})
	})

	t.Run("TransferFeeWithoutTransferFlag", func(t *testing.T) {
		// Reference: rippled lines 122-127
		// tries to set a txfee while not enabling transfer
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		maxAmt := uint64(100)
		assetScale := uint8(0)
		transferFee := uint16(50000) // maxTransferFee
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString("74657374"),
			Err:         jtx.TemMALFORMED,
		})
	})

	t.Run("EmptyMetadata", func(t *testing.T) {
		// Reference: rippled lines 130-135
		// empty metadata returns error
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		maxAmt := uint64(100)
		assetScale := uint8(0)
		transferFee := uint16(0)
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString(""), // empty metadata
			Err:         jtx.TemMALFORMED,
		})
	})

	t.Run("MaximumAmountZero", func(t *testing.T) {
		// Reference: rippled lines 138-143
		// MaximumAmount of 0 returns error
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		maxAmt := uint64(0)
		assetScale := uint8(1)
		transferFee := uint16(1)
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString("74657374"),
			Err:         jtx.TemMALFORMED,
		})
	})

	t.Run("MaximumAmountTooLarge", func(t *testing.T) {
		// Reference: rippled lines 146-157
		// MaximumAmount larger than 63 bit returns error
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		assetScale := uint8(0)
		transferFee := uint16(0)

		// 0xFFFF'FFFF'FFFF'FFF0 = 18446744073709551600
		maxAmt1 := uint64(0xFFFFFFFFFFFFFFF0)
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt1,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString("74657374"),
			Err:         jtx.TemMALFORMED,
		})

		// maxMPTokenAmount + 1 = 9223372036854775808
		maxAmt2 := mpt.MaxMPTokenAmount + 1
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt2,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString("74657374"),
			Err:         jtx.TemMALFORMED,
		})
	})
}

// --------------------------------------------------------------------------
// TestMPT_CreateEnabled
// Reference: rippled MPToken_test.cpp testCreateEnabled() (lines 161-233)
// --------------------------------------------------------------------------

func TestMPT_CreateEnabled(t *testing.T) {
	t.Run("CreateWithAllFlags", func(t *testing.T) {
		// Reference: rippled lines 169-190
		// If the MPT amendment IS enabled, you should be able to create MPTokenIssuances
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)

		maxAmt := mpt.MaxMPTokenAmount // 9223372036854775807
		assetScale := uint8(1)
		transferFee := uint16(10)
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			AssetScale:  &assetScale,
			TransferFee: &transferFee,
			Metadata:    mpt.PtrString("313233"), // "123" in hex
			OwnerCount:  mpt.PtrUint32(1),
			Flags:       mpt.TfMPTCanLock | mpt.TfMPTRequireAuth | mpt.TfMPTCanEscrow | mpt.TfMPTCanTrade | mpt.TfMPTCanTransfer | mpt.TfMPTCanClawback,
		})
	})
}

// --------------------------------------------------------------------------
// TestMPT_DestroyValidation
// Reference: rippled MPToken_test.cpp testDestroyValidation() (lines 235-283)
// --------------------------------------------------------------------------

func TestMPT_DestroyValidation(t *testing.T) {
	t.Run("Preflight", func(t *testing.T) {
		// Reference: rippled lines 244-254
		env := jtx.NewTestEnv(t)
		env.DisableFeature("MPTokensV1")
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)

		// Feature disabled
		mptAlice.Destroy(mpt.DestroyOpts{
			ID:         id,
			OwnerCount: mpt.PtrUint32(0),
			Err:        jtx.TemDISABLED,
		})

		// Enable feature and test invalid flag
		env.EnableFeature("MPTokensV1")

		mptAlice.Destroy(mpt.DestroyOpts{
			ID:    id,
			Flags: 0x00000001,
			Err:   jtx.TemINVALID_FLAG,
		})
	})

	t.Run("Preclaim", func(t *testing.T) {
		// Reference: rippled lines 257-282
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})

		// Object not found - issuance doesn't exist yet
		mptAlice.Destroy(mpt.DestroyOpts{
			ID:         mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address),
			OwnerCount: mpt.PtrUint32(0),
			Err:        jtx.TecOBJECT_NOT_FOUND,
		})

		// Create the issuance
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})

		// Non-issuer tries to destroy
		mptAlice.Destroy(mpt.DestroyOpts{
			Issuer: bob,
			Err:    jtx.TecNO_PERMISSION,
		})

		// Can't destroy when outstanding balance exists
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})
		mptAlice.Pay(alice, bob, 100)
		mptAlice.Destroy(mpt.DestroyOpts{
			Err: jtx.TecHAS_OBLIGATIONS,
		})
	})
}

// --------------------------------------------------------------------------
// TestMPT_DestroyEnabled
// Reference: rippled MPToken_test.cpp testDestroyEnabled() (lines 285-301)
// --------------------------------------------------------------------------

func TestMPT_DestroyEnabled(t *testing.T) {
	// Reference: rippled lines 295-301
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	mptAlice := mpt.NewMPTTester(t, env, alice)

	mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})
	mptAlice.Destroy(mpt.DestroyOpts{OwnerCount: mpt.PtrUint32(0)})
}

// --------------------------------------------------------------------------
// TestMPT_AuthorizeValidation
// Reference: rippled MPToken_test.cpp testAuthorizeValidation() (lines 303-482)
// --------------------------------------------------------------------------

func TestMPT_AuthorizeValidation(t *testing.T) {
	t.Run("Preflight_FeatureDisabled", func(t *testing.T) {
		// Reference: rippled lines 313-321
		env := jtx.NewTestEnv(t)
		env.DisableFeature("MPTokensV1")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})

		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			ID:      mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address),
			Err:     jtx.TemDISABLED,
		})
	})

	t.Run("Preflight_InvalidFields", func(t *testing.T) {
		// Reference: rippled lines 324-339
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})

		// Invalid flag (only tfMPTUnauthorize = 1 is valid)
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Flags:   0x00000002,
			Err:     jtx.TemINVALID_FLAG,
		})

		// Holder field same as account
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Holder:  bob,
			Err:     jtx.TemMALFORMED,
		})

		// Issuer with holder = issuer
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Holder: alice,
			Err:    jtx.TemMALFORMED,
		})
	})

	t.Run("Preclaim_IssuanceDoesNotExist", func(t *testing.T) {
		// Reference: rippled lines 342-353
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		id := mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address)

		// Holder tries to authorize non-existent issuance
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Holder: bob,
			ID:     id,
			Err:    jtx.TecOBJECT_NOT_FOUND,
		})

		// Account tries to authorize non-existent issuance
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			ID:      id,
			Err:     jtx.TecOBJECT_NOT_FOUND,
		})
	})

	t.Run("Preclaim_WithoutAllowlisting", func(t *testing.T) {
		// Reference: rippled lines 356-406
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})

		// bob submits with holder field
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Holder:  alice,
			Err:     jtx.TecNO_PERMISSION,
		})

		// alice tries to hold her own token
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: alice,
			Err:     jtx.TecNO_PERMISSION,
		})

		// the mpt does not enable allowlisting
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Holder: bob,
			Err:    jtx.TecNO_AUTH,
		})

		// bob creates mptoken
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})

		// bob cannot create mptoken a second time
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Err:     jtx.TecDUPLICATE,
		})

		// bob can't delete when balance is non-zero
		mptAlice.Pay(alice, bob, 100)
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Flags:   mpt.TfMPTUnauthorize,
			Err:     jtx.TecHAS_OBLIGATIONS,
		})

		// bob pays back
		mptAlice.Pay(bob, alice, 100)

		// bob deletes mptoken
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Flags:   mpt.TfMPTUnauthorize,
		})

		// bob tries to delete again
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTUnauthorize,
			Err:         jtx.TecOBJECT_NOT_FOUND,
		})
	})

	t.Run("Preclaim_WithAllowlisting", func(t *testing.T) {
		// Reference: rippled lines 408-445
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		cindy := jtx.NewAccount("cindy")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTRequireAuth,
		})

		// alice submits without specifying a holder
		mptAlice.Authorize(mpt.AuthorizeOpts{Err: jtx.TecNO_PERMISSION})

		// alice tries to authorize a holder that hasn't created mptoken yet
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Holder: bob,
			Err:    jtx.TecOBJECT_NOT_FOUND,
		})

		// alice specifies a holder that doesn't exist
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Holder: cindy,
			Err:    jtx.TecNO_DST,
		})

		// bob creates mptoken
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})

		// alice tries to unauthorize bob (successful but no-op since bob isn't authorized yet)
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Holder: bob,
			Flags:  mpt.TfMPTUnauthorize,
		})

		// alice authorizes bob
		mptAlice.Authorize(mpt.AuthorizeOpts{Holder: bob})

		// alice tries to authorize bob again (successful but no-op)
		mptAlice.Authorize(mpt.AuthorizeOpts{Holder: bob})

		// bob deletes his mptoken
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTUnauthorize,
		})
	})

	t.Run("Reserve_FirstTwoFree", func(t *testing.T) {
		// Reference: rippled lines 448-482
		// Test mptoken reserve requirement - first two mpts free
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		acctReserve := uint64(jtx.XRP(200)) // 200 XRP account reserve
		incReserve := uint64(jtx.XRP(50))    // 50 XRP increment reserve

		// Fund bob with just enough for account reserve + almost one increment
		env.FundAmount(alice, uint64(jtx.XRP(10_000)))
		env.FundAmount(bob, acctReserve+incReserve-1)

		mptAlice1 := mpt.NewMPTTesterNoFund(t, env, alice)
		mptAlice1.Create(mpt.CreateOpts{})

		mptAlice2 := mpt.NewMPTTesterNoFund(t, env, alice)
		mptAlice2.Create(mpt.CreateOpts{})

		mptAlice3 := mpt.NewMPTTesterNoFund(t, env, alice)
		mptAlice3.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(3)})

		// first mpt for free
		mptAlice1.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})

		// second mpt free
		mptAlice2.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(2),
		})

		// third mpt fails - insufficient reserve
		mptAlice3.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Err:     jtx.TecINSUFFICIENT_RESERVE,
		})

		// Fund bob with more XRP
		env.FundAmount(bob, incReserve*3)
		env.Close()

		// Now third should succeed
		mptAlice3.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(3),
		})
	})
}

// --------------------------------------------------------------------------
// TestMPT_AuthorizeEnabled
// Reference: rippled MPToken_test.cpp testAuthorizeEnabled() (lines 485-558)
// --------------------------------------------------------------------------

func TestMPT_AuthorizeEnabled(t *testing.T) {
	t.Run("BasicWithoutAllowlisting", func(t *testing.T) {
		// Reference: rippled lines 494-511
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})

		// bob creates mptoken
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})

		// duplicate
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
			Err:         jtx.TecDUPLICATE,
		})

		// bob deletes
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTUnauthorize,
		})
	})

	t.Run("WithAllowlisting", func(t *testing.T) {
		// Reference: rippled lines 514-537
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTRequireAuth,
		})

		// bob creates mptoken
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})

		// alice authorizes bob
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: alice,
			Holder:  bob,
		})

		// Unauthorize bob
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     alice,
			Holder:      bob,
			HolderCount: mpt.PtrUint32(1),
			Flags:       mpt.TfMPTUnauthorize,
		})

		// bob deletes
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTUnauthorize,
		})
	})

	t.Run("DanglingMPToken", func(t *testing.T) {
		// Reference: rippled lines 539-557
		// Holder can have dangling MPToken even if issuance has been destroyed.
		// Make sure they can still delete/unauthorize the MPToken.
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})

		// bob creates mptoken
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})

		// alice deletes her issuance
		mptAlice.Destroy(mpt.DestroyOpts{OwnerCount: mpt.PtrUint32(0)})

		// bob can delete his mptoken even though issuance is no longer existent
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTUnauthorize,
		})
	})
}

// --------------------------------------------------------------------------
// TestMPT_SetValidation
// Reference: rippled MPToken_test.cpp testSetValidation() (lines 560-775)
// --------------------------------------------------------------------------

func TestMPT_SetValidation(t *testing.T) {
	t.Run("Preflight", func(t *testing.T) {
		// Reference: rippled lines 570-658
		env := jtx.NewTestEnv(t)
		env.DisableFeature("MPTokensV1")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})

		// Feature disabled
		mptAlice.Set(mpt.SetOpts{
			Account: bob,
			ID:      mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address),
			Err:     jtx.TemDISABLED,
		})

		// Enable feature
		env.EnableFeature("MPTokensV1")

		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob, HolderCount: mpt.PtrUint32(1)})

		// Invalid flag (only tfMPTLock=1 and tfMPTUnlock=2 are valid)
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Flags:   0x00000008,
			Err:     jtx.TemINVALID_FLAG,
		})

		// nothing is being changed
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Flags:   0x00000000,
			Err:     jtx.TecNO_PERMISSION,
		})

		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Holder:  bob,
			Flags:   0x00000000,
			Err:     jtx.TecNO_PERMISSION,
		})

		// both lock and unlock at same time
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Flags:   mpt.TfMPTLock | mpt.TfMPTUnlock,
			Err:     jtx.TemINVALID_FLAG,
		})

		// holder same as account
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Holder:  alice,
			Flags:   mpt.TfMPTLock,
			Err:     jtx.TemMALFORMED,
		})
	})

	t.Run("Preclaim_LockingDisabled", func(t *testing.T) {
		// Reference: rippled lines 660-696
		// test when mptokenissuance has disabled locking
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1)})

		// alice tries to lock - disabled
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Flags:   mpt.TfMPTLock,
			Err:     jtx.TecNO_PERMISSION,
		})

		// alice tries to unlock - disabled
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Flags:   mpt.TfMPTUnlock,
			Err:     jtx.TecNO_PERMISSION,
		})

		// issuer tries to lock bob - disabled
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Holder:  bob,
			Flags:   mpt.TfMPTLock,
			Err:     jtx.TecNO_PERMISSION,
		})

		// issuer tries to unlock bob - disabled
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Holder:  bob,
			Flags:   mpt.TfMPTUnlock,
			Err:     jtx.TecNO_PERMISSION,
		})
	})

	t.Run("Preclaim_LockingEnabled", func(t *testing.T) {
		// Reference: rippled lines 698-727
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		cindy := jtx.NewAccount("cindy")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})

		// issuance doesn't exist yet
		mptAlice.Set(mpt.SetOpts{
			ID:    mpt.MakeMPTIDHexFromAddr(env.Seq(alice), alice.Address),
			Flags: mpt.TfMPTLock,
			Err:   jtx.TecOBJECT_NOT_FOUND,
		})

		// create with locking
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanLock,
		})

		// non-issuer tries to set
		mptAlice.Set(mpt.SetOpts{
			Account: bob,
			Flags:   mpt.TfMPTLock,
			Err:     jtx.TecNO_PERMISSION,
		})

		// trying to set a holder who doesn't have a mptoken
		mptAlice.Set(mpt.SetOpts{
			Holder: bob,
			Flags:  mpt.TfMPTLock,
			Err:    jtx.TecOBJECT_NOT_FOUND,
		})

		// trying to set a holder who doesn't exist
		mptAlice.Set(mpt.SetOpts{
			Holder: cindy,
			Flags:  mpt.TfMPTLock,
			Err:    jtx.TecNO_DST,
		})
	})
}

// --------------------------------------------------------------------------
// TestMPT_SetEnabled
// Reference: rippled MPToken_test.cpp testSetEnabled() (lines 777-912)
// --------------------------------------------------------------------------

func TestMPT_SetEnabled(t *testing.T) {
	t.Run("LockUnlock", func(t *testing.T) {
		// Reference: rippled lines 787-851
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanLock,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account:     bob,
			HolderCount: mpt.PtrUint32(1),
		})

		// locks bob's mptoken
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})

		// lock again (no-op but succeeds)
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})

		// alice locks the issuance
		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})

		// lock again (no-op)
		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})

		// alice unlocks bob's mptoken
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTUnlock})

		// locks bob again
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})

		// Delete bob's mptoken even though it is locked
		// (without featureSingleAssetVault, this succeeds)
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: bob,
			Flags:   mpt.TfMPTUnauthorize,
		})

		// Now bob's mptoken doesn't exist
		mptAlice.Set(mpt.SetOpts{
			Account: alice,
			Holder:  bob,
			Flags:   mpt.TfMPTUnlock,
			Err:     jtx.TecOBJECT_NOT_FOUND,
		})
	})
}

// --------------------------------------------------------------------------
// TestMPT_Payment
// Reference: rippled MPToken_test.cpp testPayment() (lines 915-1788)
// --------------------------------------------------------------------------

func TestMPT_Payment(t *testing.T) {
	t.Run("MPTDisabled", func(t *testing.T) {
		// Reference: rippled lines 928-937
		env := jtx.NewTestEnv(t)
		env.DisableFeature("MPTokensV1")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(1_000)))
		env.FundAmount(bob, uint64(jtx.XRP(1_000)))

		mptAlice := mpt.NewMPTTesterNoFund(t, env, alice)
		mptAlice.Pay(alice, bob, 100, jtx.TemDISABLED)
	})

	t.Run("InvalidFlag", func(t *testing.T) {
		// Reference: rippled lines 959-973
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		// tfNoRippleDirect
		mptAlice.PayWithFlags(alice, bob, 10, mpt.TfNoRippleDirect, jtx.TemINVALID_FLAG)

		// tfLimitQuality
		mptAlice.PayWithFlags(alice, bob, 10, mpt.TfLimitQuality, jtx.TemINVALID_FLAG)
	})

	t.Run("NegativeAmount", func(t *testing.T) {
		// Reference: rippled lines 1046-1064
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})

		mptAlice.Pay(alice, bob, -1, jtx.TemBAD_AMOUNT)
		mptAlice.Pay(bob, carol, -1, jtx.TemBAD_AMOUNT)
		mptAlice.Pay(bob, alice, -1, jtx.TemBAD_AMOUNT)
	})

	t.Run("PayToSelf", func(t *testing.T) {
		// Reference: rippled lines 1067-1078
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		mptAlice.Pay(bob, bob, 10, jtx.TemREDUNDANT)
	})

	t.Run("DestinationDoesNotExist", func(t *testing.T) {
		// Reference: rippled lines 1082-1096
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		bad := jtx.NewAccount("bad")
		env.Fund(alice)
		env.Fund(bob)
		// bad is NOT funded (doesn't exist)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		mptAlice.Pay(bob, bad, 10, jtx.TecNO_DST)
	})

	t.Run("RequireAuth_ReceiverNotAuthorized", func(t *testing.T) {
		// Reference: rippled lines 1100-1115
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTRequireAuth | mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		// Payment fails because bob is not authorized
		mptAlice.Pay(alice, bob, 100, jtx.TecNO_AUTH)
	})

	t.Run("RequireAuth_SenderUnauthorized", func(t *testing.T) {
		// Reference: rippled lines 1117-1145
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTRequireAuth | mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: alice, Holder: bob})

		mptAlice.Pay(alice, bob, 100)

		// alice UNAUTHORIZES bob
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: alice,
			Holder:  bob,
			Flags:   mpt.TfMPTUnauthorize,
		})

		// bob fails to send because he is no longer authorized
		mptAlice.Pay(bob, alice, 100, jtx.TecNO_AUTH)
	})

	t.Run("CanTransferDisabled", func(t *testing.T) {
		// Reference: rippled lines 1341-1368
		// Non-issuer cannot send to each other if MPTCanTransfer isn't set
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		cindy := jtx.NewAccount("cindy")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(cindy)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, cindy}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: cindy})

		mptAlice.Pay(alice, bob, 100)

		// bob tries to send to cindy, fails because canTransfer is off
		mptAlice.Pay(bob, cindy, 10, jtx.TecNO_AUTH)

		// bob can send back to issuer
		mptAlice.Pay(bob, alice, 10)
	})

	t.Run("HolderNotAuthorized", func(t *testing.T) {
		// Reference: rippled lines 1370-1387
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})

		// issuer to holder (no MPToken created by holder)
		mptAlice.Pay(alice, bob, 100, jtx.TecNO_AUTH)

		// holder to issuer (no MPToken)
		mptAlice.Pay(bob, alice, 100, jtx.TecNO_AUTH)

		// holder to holder (no MPTokens)
		mptAlice.Pay(bob, carol, 50, jtx.TecNO_AUTH)
	})

	t.Run("InsufficientFunds", func(t *testing.T) {
		// Reference: rippled lines 1389-1407
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})

		mptAlice.Pay(alice, bob, 100)

		// Pay more than balance to another holder
		mptAlice.Pay(bob, carol, 101, jtx.TecPATH_PARTIAL)

		// Pay more than balance to the issuer
		mptAlice.Pay(bob, alice, 101, jtx.TecPATH_PARTIAL)
	})

	t.Run("Locked", func(t *testing.T) {
		// Reference: rippled lines 1409-1445
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanLock | mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})

		mptAlice.Pay(alice, bob, 100)
		mptAlice.Pay(alice, carol, 100)

		// Global lock
		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})
		// Can't send between holders
		mptAlice.Pay(bob, carol, 1, jtx.TecLOCKED)
		mptAlice.Pay(carol, bob, 2, jtx.TecLOCKED)
		// Issuer can still send
		mptAlice.Pay(alice, bob, 3)
		// Holder can send back to issuer
		mptAlice.Pay(bob, alice, 4)

		// Global unlock
		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTUnlock})
		// Individual lock on bob
		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})
		// Can't send from/to locked holder
		mptAlice.Pay(bob, carol, 5, jtx.TecLOCKED)
		mptAlice.Pay(carol, bob, 6, jtx.TecLOCKED)
		// Issuer can still send to locked
		mptAlice.Pay(alice, bob, 7)
		// Locked holder can send to issuer
		mptAlice.Pay(bob, alice, 8)
	})

	t.Run("TransferFee", func(t *testing.T) {
		// Reference: rippled lines 1447-1501
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		transferFee := uint16(10_000) // 10%
		mptAlice.Create(mpt.CreateOpts{
			TransferFee: &transferFee,
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})

		// Payment between issuer and holder, no transfer fee
		mptAlice.Pay(alice, bob, 2_000)

		// Payment between holder and issuer, no transfer fee
		mptAlice.Pay(bob, alice, 1_000)
		mptAlice.RequireMPTokenAmount(bob, 1_000)

		// Holder to holder: sender doesn't have enough for transfer fee
		mptAlice.Pay(bob, carol, 1_000, jtx.TecPATH_PARTIAL)

		// Holder to holder: has funds but no SendMax
		mptAlice.Pay(bob, carol, 100, jtx.TecPATH_PARTIAL)

		// SendMax doesn't cover the fee
		mptAlice.PayWithSendMax(bob, carol, 100, 109, jtx.TecPATH_PARTIAL)

		// Success with sufficient SendMax (100 to carol, 10 to issuer)
		mptAlice.PayWithSendMax(bob, carol, 100, 110)
		// 100 to carol, 10 to issuer (115 SendMax, only 110 used)
		mptAlice.PayWithSendMax(bob, carol, 100, 115)
		mptAlice.RequireMPTokenAmount(bob, 780)
		mptAlice.RequireMPTokenAmount(carol, 200)

		// Partial payment with SendMax less than deliver amount
		mptAlice.PayFull(bob, carol, 100, 90, 0, mpt.TfPartialPayment)
		// 82 to carol, 8 to issuer (90 / 1.1 ~ 81.81 rounded = 82)
		mptAlice.RequireMPTokenAmount(bob, 690)
		mptAlice.RequireMPTokenAmount(carol, 282)
	})

	t.Run("InsufficientSendMax_NoTransferFee", func(t *testing.T) {
		// Reference: rippled lines 1503-1534
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})
		mptAlice.Pay(alice, bob, 1_000)

		// SendMax is less than the amount
		mptAlice.PayWithSendMax(bob, carol, 100, 99, jtx.TecPATH_PARTIAL)
		mptAlice.PayWithSendMax(bob, alice, 100, 99, jtx.TecPATH_PARTIAL)

		// Sufficient SendMax
		mptAlice.PayWithSendMax(bob, carol, 100, 100)
		mptAlice.RequireMPTokenAmount(carol, 100)

		// Partial payment with insufficient SendMax
		mptAlice.PayFull(bob, carol, 100, 99, 0, mpt.TfPartialPayment)
		mptAlice.RequireMPTokenAmount(carol, 199)
	})

	t.Run("DeliverMin", func(t *testing.T) {
		// Reference: rippled lines 1536-1563
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})
		mptAlice.Pay(alice, bob, 1_000)

		// Fails: deliver amount < deliverMin
		mptAlice.PayFull(bob, alice, 100, 99, 100, mpt.TfPartialPayment, jtx.TecPATH_PARTIAL)

		// Succeeds: deliver amount >= deliverMin
		mptAlice.PayFull(bob, alice, 100, 99, 99, mpt.TfPartialPayment)
	})

	t.Run("ExceedMaxAmount", func(t *testing.T) {
		// Reference: rippled lines 1565-1584
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		maxAmt := uint64(100)
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		// issuer sends max amount
		mptAlice.Pay(alice, bob, 100)

		// issuer tries to exceed max amount
		mptAlice.Pay(alice, bob, 1, jtx.TecPATH_PARTIAL)
	})

	t.Run("ExceedDefaultMaxAmount", func(t *testing.T) {
		// Reference: rippled lines 1586-1602
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		// issuer sends default max amount
		mptAlice.Pay(alice, bob, int64(mpt.MaxMPTokenAmount))

		// issuer tries to exceed
		mptAlice.Pay(alice, bob, 1, jtx.TecPATH_PARTIAL)
	})

	t.Run("PayAfterDestroy", func(t *testing.T) {
		// Reference: rippled lines 1700-1716
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		// alice destroys issuance
		mptAlice.Destroy(mpt.DestroyOpts{OwnerCount: mpt.PtrUint32(0)})

		// alice tries to send after destroy
		mptAlice.Pay(alice, bob, 100, jtx.TecOBJECT_NOT_FOUND)
	})

	t.Run("MaxAmountTransfer", func(t *testing.T) {
		// Reference: rippled lines 1670-1698
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		maxAmt := mpt.MaxMPTokenAmount
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:      &maxAmt,
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})

		// Send max amount
		mptAlice.Pay(alice, bob, int64(mpt.MaxMPTokenAmount))
		mptAlice.CheckMPTokenOutstandingAmount(int64(mpt.MaxMPTokenAmount))

		// Transfer between holders
		mptAlice.Pay(bob, carol, int64(mpt.MaxMPTokenAmount))
		mptAlice.CheckMPTokenOutstandingAmount(int64(mpt.MaxMPTokenAmount))

		// Pay back to issuer
		mptAlice.Pay(carol, alice, int64(mpt.MaxMPTokenAmount))
		mptAlice.CheckMPTokenOutstandingAmount(0)
	})

	t.Run("MaxAmountHolderTransfer", func(t *testing.T) {
		// Reference: rippled lines 1745-1765
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		maxAmt := uint64(100)
		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			MaxAmt:     &maxAmt,
			OwnerCount: mpt.PtrUint32(1),
			Flags:      mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})

		mptAlice.Pay(alice, bob, 100)

		// transfer max amount to another holder
		mptAlice.Pay(bob, carol, 100)
	})

	t.Run("SimplePayment", func(t *testing.T) {
		// Reference: rippled lines 1767-1788
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.Fund(carol)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob, carol}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanTransfer,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: carol})

		// issuer to holder
		mptAlice.Pay(alice, bob, 100)

		// holder to issuer
		mptAlice.Pay(bob, alice, 100)

		// holder to holder
		mptAlice.Pay(alice, bob, 100)
		mptAlice.Pay(bob, carol, 50)
	})
}

// --------------------------------------------------------------------------
// TestMPT_ClawbackValidation
// Reference: rippled MPToken_test.cpp testClawbackValidation() (lines 2353-2500)
// --------------------------------------------------------------------------

func TestMPT_ClawbackValidation(t *testing.T) {
	t.Run("FeatureDisabled", func(t *testing.T) {
		// Reference: rippled lines 2360-2380
		env := jtx.NewTestEnv(t)
		env.DisableFeature("MPTokensV1")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(1_000)))
		env.FundAmount(bob, uint64(jtx.XRP(1_000)))

		mptAlice := mpt.NewMPTTesterNoFund(t, env, alice)
		mptAlice.Claw(alice, bob, 5, jtx.TemDISABLED)
	})

	t.Run("Preflight", func(t *testing.T) {
		// Reference: rippled lines 2382-2414
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(1_000)))
		env.FundAmount(bob, uint64(jtx.XRP(1_000)))

		mptAlice := mpt.NewMPTTesterNoFund(t, env, alice)

		// clawback zero amount fails
		mptAlice.Claw(alice, bob, 0, jtx.TemBAD_AMOUNT)

		// alice can't clawback from herself
		mptAlice.Claw(alice, alice, 5, jtx.TemMALFORMED)

		// can't clawback negative amount
		mptAlice.Claw(alice, bob, -1, jtx.TemBAD_AMOUNT)
	})

	t.Run("Preclaim_ClawbackDisabled", func(t *testing.T) {
		// Reference: rippled lines 2416-2439
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})

		// Create without clawback flag
		mptAlice.Create(mpt.CreateOpts{OwnerCount: mpt.PtrUint32(1), HolderCount: mpt.PtrUint32(0)})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Pay(alice, bob, 100)

		// alice cannot clawback because flag not enabled
		mptAlice.Claw(alice, bob, 1, jtx.TecNO_PERMISSION)
	})

	t.Run("Preclaim_Various", func(t *testing.T) {
		// Reference: rippled lines 2441-2477
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		env.Fund(alice)
		env.Fund(bob)
		env.FundAmount(carol, uint64(jtx.XRP(1_000)))

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})

		// issuance doesn't exist
		mptAlice.Claw(alice, bob, 5, jtx.TecOBJECT_NOT_FOUND)

		// create with clawback
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanClawback,
		})

		// bob doesn't have MPToken
		mptAlice.Claw(alice, bob, 1, jtx.TecOBJECT_NOT_FOUND)

		// bob creates MPToken
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})

		// clawback fails because bob has zero balance
		mptAlice.Claw(alice, bob, 1, jtx.TecINSUFFICIENT_FUNDS)

		// alice pays bob
		mptAlice.Pay(alice, bob, 100)

		// carol can't clawback because she's not the issuer
		mptAlice.Claw(carol, bob, 1, jtx.TecNO_PERMISSION)
	})
}

// --------------------------------------------------------------------------
// TestMPT_Clawback
// Reference: rippled MPToken_test.cpp testClawback() (lines 2503-2613)
// --------------------------------------------------------------------------

func TestMPT_Clawback(t *testing.T) {
	t.Run("BasicClawback", func(t *testing.T) {
		// Reference: rippled lines 2509-2532
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanClawback,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Pay(alice, bob, 100)

		mptAlice.Claw(alice, bob, 1)
		mptAlice.Claw(alice, bob, 1000) // clawback more than balance - should take remaining

		// Now balance is zero, fails
		mptAlice.Claw(alice, bob, 1, jtx.TecINSUFFICIENT_FUNDS)
	})

	t.Run("ClawbackGlobalLocked", func(t *testing.T) {
		// Reference: rippled lines 2534-2557
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanLock | mpt.TfMPTCanClawback,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Pay(alice, bob, 100)

		mptAlice.Set(mpt.SetOpts{Account: alice, Flags: mpt.TfMPTLock})
		mptAlice.Claw(alice, bob, 100)
	})

	t.Run("ClawbackIndividualLocked", func(t *testing.T) {
		// Reference: rippled lines 2559-2582
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanLock | mpt.TfMPTCanClawback,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Pay(alice, bob, 100)

		mptAlice.Set(mpt.SetOpts{Account: alice, Holder: bob, Flags: mpt.TfMPTLock})
		mptAlice.Claw(alice, bob, 100)
	})

	t.Run("ClawbackUnauthorized", func(t *testing.T) {
		// Reference: rippled lines 2584-2612
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTCanClawback | mpt.TfMPTRequireAuth,
		})

		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: alice, Holder: bob})
		mptAlice.Pay(alice, bob, 100)

		// alice unauthorizes bob
		mptAlice.Authorize(mpt.AuthorizeOpts{
			Account: alice,
			Holder:  bob,
			Flags:   mpt.TfMPTUnauthorize,
		})

		// alice can still clawback even though bob is unauthorized
		mptAlice.Claw(alice, bob, 100)
	})
}

// --------------------------------------------------------------------------
// TestMPT_DepositPreauth
// Reference: rippled MPToken_test.cpp testDepositPreauth() (lines 1791-1940)
// --------------------------------------------------------------------------

func TestMPT_DepositPreauth(t *testing.T) {
	t.Run("WithCredentials", func(t *testing.T) {
		// Reference: rippled lines 1802-1877
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		diana := jtx.NewAccount("diana")
		env.Fund(alice)
		env.Fund(bob)
		env.FundAmount(diana, uint64(jtx.XRP(50_000)))
		env.Close()

		mptAlice := mpt.NewMPTTester(t, env, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
		mptAlice.Create(mpt.CreateOpts{
			OwnerCount:  mpt.PtrUint32(1),
			HolderCount: mpt.PtrUint32(0),
			Flags:       mpt.TfMPTRequireAuth | mpt.TfMPTCanTransfer,
		})

		// bob creates MPToken
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: bob})
		// alice authorizes bob
		mptAlice.Authorize(mpt.AuthorizeOpts{Account: alice, Holder: bob})

		// Bob requires preauthorization
		env.EnableDepositAuth(bob)
		env.Close()

		// alice tries to send - not authorized via deposit preauth
		mptAlice.Pay(alice, bob, 100, jtx.TecNO_PERMISSION)
		env.Close()

		// Bob authorizes alice
		env.Preauthorize(bob, alice)
		env.Close()

		// Now alice can send
		mptAlice.Pay(alice, bob, 100)
		env.Close()

		// Bob revokes authorization
		env.Unauthorize(bob, alice)
		env.Close()

		// alice can't send anymore
		mptAlice.Pay(alice, bob, 100, jtx.TecNO_PERMISSION)
		env.Close()
	})
}

// --------------------------------------------------------------------------
// TestMPT_TxJsonMetaFields
// Reference: rippled MPToken_test.cpp testTxJsonMetaFields() (lines 2320-2351)
// --------------------------------------------------------------------------

func TestMPT_TxJsonMetaFields(t *testing.T) {
	// Reference: rippled lines 2320-2351
	// This test checks that mpt_issuance_id is synthetically injected in tx response.
	// This is RPC-level behavior. In Go, we verify the issuance ID is computed correctly.

	t.Run("IssuanceIDComputed", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)

		mptAlice := mpt.NewMPTTester(t, env, alice)
		mptAlice.Create(mpt.CreateOpts{})

		// Verify the issuance ID was computed
		id := mptAlice.IssuanceID()
		if id == "" {
			t.Fatal("Expected non-empty issuance ID")
		}

		// The ID should be 48 hex chars (24 bytes)
		if len(id) != 48 {
			t.Fatalf("Expected 48 hex chars for issuance ID, got %d: %s", len(id), id)
		}
	})
}

// --------------------------------------------------------------------------
// TestMPT_TokensEquality
// Reference: rippled MPToken_test.cpp testTokensEquality() (lines 2615-2656)
// --------------------------------------------------------------------------

func TestMPT_TokensEquality(t *testing.T) {
	// Reference: rippled lines 2615-2656
	// This tests Asset/MPTIssue comparison which is internal protocol logic.
	// In Go, we test the MPTID computation and comparison.

	t.Run("SameSequenceAndIssuer", func(t *testing.T) {
		alice := jtx.NewAccount("alice")
		id1 := mpt.MakeMPTIDHexFromAddr(1, alice.Address)
		id1a := mpt.MakeMPTIDHexFromAddr(1, alice.Address)
		if id1 != id1a {
			t.Fatalf("Expected same MPTID for same sequence+issuer: %s != %s", id1, id1a)
		}
	})

	t.Run("DifferentIssuer", func(t *testing.T) {
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		id1 := mpt.MakeMPTIDHexFromAddr(1, alice.Address)
		id2 := mpt.MakeMPTIDHexFromAddr(1, bob.Address)
		if id1 == id2 {
			t.Fatal("Expected different MPTID for different issuers")
		}
	})

	t.Run("DifferentSequence", func(t *testing.T) {
		alice := jtx.NewAccount("alice")
		id1 := mpt.MakeMPTIDHexFromAddr(1, alice.Address)
		id2 := mpt.MakeMPTIDHexFromAddr(2, alice.Address)
		if id1 == id2 {
			t.Fatal("Expected different MPTID for different sequences")
		}
	})
}

// --------------------------------------------------------------------------
// TestMPT_HelperFunctions
// Reference: rippled MPToken_test.cpp testHelperFunctions() (lines 2658-2738)
// --------------------------------------------------------------------------

func TestMPT_HelperFunctions(t *testing.T) {
	// Reference: rippled lines 2658-2738
	// Tests MPT arithmetic and JSON serialization.
	// These are internal helper tests specific to rippled's type system.

	t.Run("MPTIDCreation", func(t *testing.T) {
		// Verify MakeMPTID produces correct format
		alice := jtx.NewAccount("alice")
		id := mpt.MakeMPTIDHexFromAddr(4, alice.Address)

		// ID should be 48 hex chars
		if len(id) != 48 {
			t.Fatalf("Expected 48 hex chars, got %d: %s", len(id), id)
		}

		// First 8 chars should be sequence 4 in big-endian hex
		if id[:8] != "00000004" {
			t.Fatalf("Expected sequence prefix 00000004, got %s", id[:8])
		}
	})
}
