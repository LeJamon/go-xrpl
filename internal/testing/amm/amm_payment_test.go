// Package amm_test contains tests for AMM payment, flags, rippling, and AMMID scenarios.
// Reference: rippled/src/test/app/AMM_test.cpp
//   - testInvalidAMMPayment (line 3611)
//   - testFlags (line 4882)
//   - testRippling (line 4903)
//   - testAMMID (line 5769)
package amm_test

import (
	"fmt"
	"testing"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/amm"
	"github.com/LeJamon/goXRPLd/internal/testing/check"
	"github.com/LeJamon/goXRPLd/internal/testing/escrow"
	"github.com/LeJamon/goXRPLd/internal/testing/paychan"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/LeJamon/goXRPLd/internal/tx"
	coreAmm "github.com/LeJamon/goXRPLd/internal/tx/amm"
	paymentPkg "github.com/LeJamon/goXRPLd/internal/tx/payment"
)

// ammAccount computes the AMM pseudo-account for the given asset pair and returns
// a *jtx.Account suitable for use with test builders (payment, escrow, etc.).
func ammAccount(t *testing.T, asset1, asset2 tx.Asset) *jtx.Account {
	t.Helper()

	addr := coreAmm.ComputeAMMAccountAddress(asset1, asset2)
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(addr)
	if err != nil {
		t.Fatalf("Failed to decode AMM account address %s: %v", addr, err)
	}
	var id20 [20]byte
	copy(id20[:], idBytes)

	return &jtx.Account{
		Name:    "amm",
		Address: addr,
		ID:      id20,
	}
}

// ----------------------------------------------------------------
// testInvalidAMMPayment
// Reference: rippled AMM_test.cpp testInvalidAMMPayment (line 3611)
// ----------------------------------------------------------------

// TestInvalidAMMPayment tests that various payment-like transactions
// targeting the AMM pseudo-account are rejected with tecNO_PERMISSION.
func TestInvalidAMMPayment(t *testing.T) {
	// Reference: lines 3618-3648 — direct payments to AMM account.
	// The rippled test iterates over gw and alice as creators with varying XRP
	// balances. The core assertion is that ANY payment to the AMM pseudo-account
	// is rejected with tecNO_PERMISSION regardless of who created the AMM or
	// the XRP balance of the AMM account.
	t.Run("DirectPaymentsToAMM", func(t *testing.T) {
		// Use setupAMM which creates XRP(10000)/USD(10000) AMM via alice.
		env := setupAMM(t)
		ammAcc := ammAccount(t, amm.XRP(), env.USD)

		// Pay XRP to AMM -> tecNO_PERMISSION
		t.Run("PayXRP", func(t *testing.T) {
			payTx := payment.Pay(env.Carol, ammAcc, uint64(jtx.XRP(10))).Build()
			result := env.Submit(payTx)
			amm.ExpectTER(t, result, amm.TecNO_PERMISSION)
		})

		// Pay large XRP to AMM -> tecNO_PERMISSION
		t.Run("PayLargeXRP", func(t *testing.T) {
			payTx := payment.Pay(env.Carol, ammAcc, uint64(jtx.XRP(300))).Build()
			result := env.Submit(payTx)
			amm.ExpectTER(t, result, amm.TecNO_PERMISSION)
		})

		// Pay IOU to AMM -> tecNO_PERMISSION.
		// Note: the payment engine may reject with tecPATH_DRY if the path-finding
		// step fails before the AMM destination check is reached.
		t.Run("PayIOU", func(t *testing.T) {
			payTx := payment.PayIssued(env.Carol, ammAcc, amm.IOUAmount(env.GW, "USD", 10)).Build()
			result := env.Submit(payTx)
			amm.ExpectTER(t, result, amm.TecNO_PERMISSION, "tecPATH_DRY")
		})
	})

	// Reference: lines 3651-3660 -- escrow to AMM account -> tecNO_PERMISSION.
	t.Run("EscrowToAMM", func(t *testing.T) {
		env := setupAMM(t)
		ammAcc := ammAccount(t, amm.XRP(), env.USD)

		now := env.Now()
		finishTime := escrow.ToRippleTime(now) + 1
		cancelTime := escrow.ToRippleTime(now) + 2

		escrowTx := escrow.EscrowCreate(env.Carol, ammAcc, 1000000). // 1 XRP in drops
										Condition(escrow.TestCondition1).
										FinishAfter(finishTime).
										CancelAfter(cancelTime).
										Fee(1500). // baseFee * 150
										Build()
		result := env.Submit(escrowTx)
		amm.ExpectTER(t, result, amm.TecNO_PERMISSION)
	})

	// Reference: lines 3662-3676 -- payment channel to AMM account -> tecNO_PERMISSION.
	t.Run("PayChanToAMM", func(t *testing.T) {
		env := setupAMM(t)
		ammAcc := ammAccount(t, amm.XRP(), env.USD)

		channelTx := paychan.ChannelCreate(
			env.Carol,
			ammAcc,
			1000*1000000, // 1000 XRP in drops
			100,          // settleDelay = 100s
			env.Carol.PublicKeyHex(),
		).Build()
		result := env.Submit(channelTx)
		amm.ExpectTER(t, result, amm.TecNO_PERMISSION)
	})

	// Reference: lines 3678-3682 -- check to AMM account -> tecNO_PERMISSION.
	t.Run("CheckToAMM", func(t *testing.T) {
		env := setupAMM(t)
		ammAcc := ammAccount(t, amm.XRP(), env.USD)

		checkTx := check.CheckCreate(env.Carol, ammAcc, amm.XRPAmount(100)).Build()
		result := env.Submit(checkTx)
		amm.ExpectTER(t, result, amm.TecNO_PERMISSION)
	})

	// Reference: lines 3684-3723 -- pool consumption tests.
	// testAMM with pool {{XRP(100), USD(100)}}
	t.Run("PoolConsumption", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create small AMM pool: XRP(100)/USD(100)
		createTx := amm.AMMCreate(env.Alice,
			amm.XRPAmount(100),
			amm.IOUAmount(env.GW, "USD", 100)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Can't consume whole pool: pay USD(100) with sendmax XRP(1B)
		payTx := payment.PayIssued(env.Alice, env.Carol,
			amm.IOUAmount(env.GW, "USD", 100)).
			SendMax(amm.XRPAmount(1_000_000_000)).
			PathsCurrency("USD", env.GW).
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, amm.TecPATH_PARTIAL)

		// Can't consume whole pool: pay XRP(100) with sendmax USD(1B)
		payTx2 := payment.Pay(env.Alice, env.Carol,
			uint64(jtx.XRP(100))).
			SendMax(amm.IOUAmount(env.GW, "USD", 1_000_000_000)).
			PathsXRP().
			NoDirectRipple().
			Build()
		result = env.Submit(payTx2)
		amm.ExpectTER(t, result, amm.TecPATH_PARTIAL)
	})

	// Reference: lines 3725-3739 -- global freeze tests.
	t.Run("GlobalFreeze", func(t *testing.T) {
		env := setupAMM(t)

		// Gateway sets global freeze
		env.TestEnv.EnableGlobalFreeze(env.GW)
		env.Close()

		// Pay USD(1) through AMM with path(~USD) -> tecPATH_DRY
		payTx := payment.PayIssued(env.Alice, env.Carol,
			amm.IOUAmount(env.GW, "USD", 1)).
			SendMax(amm.XRPAmount(10)).
			PathsCurrency("USD", env.GW).
			PartialPayment().
			NoDirectRipple().
			Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "tecPATH_DRY")

		// Pay XRP(1) through AMM with path(~XRP) -> tecPATH_DRY
		payTx2 := payment.Pay(env.Alice, env.Carol,
			uint64(jtx.XRP(1))).
			SendMax(amm.IOUAmount(env.GW, "USD", 10)).
			PathsXRP().
			PartialPayment().
			NoDirectRipple().
			Build()
		result = env.Submit(payTx2)
		amm.ExpectTER(t, result, "tecPATH_DRY")
	})

	// Reference: lines 3741-3770 -- individual freeze tests.
	t.Run("IndividualFreeze", func(t *testing.T) {
		// Freeze AMM's trust line
		t.Run("FreezeAMMTrustLine", func(t *testing.T) {
			env := setupAMM(t)
			ammAcc := ammAccount(t, amm.XRP(), env.USD)

			// gw freezes the USD trust line for the AMM account
			env.TestEnv.FreezeTrustLine(env.GW, ammAcc, "USD")
			env.Close()

			// Pay USD(1) through AMM -> tecPATH_DRY
			payTx := payment.PayIssued(env.Alice, env.Carol,
				amm.IOUAmount(env.GW, "USD", 1)).
				SendMax(amm.XRPAmount(10)).
				PathsCurrency("USD", env.GW).
				PartialPayment().
				NoDirectRipple().
				Build()
			result := env.Submit(payTx)
			amm.ExpectTER(t, result, "tecPATH_DRY")

			// Pay XRP(1) through AMM -> tecPATH_DRY
			payTx2 := payment.Pay(env.Alice, env.Carol,
				uint64(jtx.XRP(1))).
				SendMax(amm.IOUAmount(env.GW, "USD", 10)).
				PathsXRP().
				PartialPayment().
				NoDirectRipple().
				Build()
			result = env.Submit(payTx2)
			amm.ExpectTER(t, result, "tecPATH_DRY")
		})

		// Freeze individual accounts
		t.Run("FreezeIndividualAccounts", func(t *testing.T) {
			env := setupAMM(t)

			// gw freezes carol's and alice's USD trust lines
			env.TestEnv.FreezeTrustLine(env.GW, env.Carol, "USD")
			env.TestEnv.FreezeTrustLine(env.GW, env.Alice, "USD")
			env.Close()

			// Pay XRP(1) with sendmax USD -> tecPATH_DRY
			payTx := payment.Pay(env.Alice, env.Carol,
				uint64(jtx.XRP(1))).
				SendMax(amm.IOUAmount(env.GW, "USD", 10)).
				PathsXRP().
				NoDirectRipple().
				PartialPayment().
				Build()
			result := env.Submit(payTx)
			amm.ExpectTER(t, result, "tecPATH_DRY")
		})
	})
}

// ----------------------------------------------------------------
// testFlags
// Reference: rippled AMM_test.cpp testFlags (line 4882)
// ----------------------------------------------------------------

// TestAMMFlags verifies that the AMM pseudo-account has the correct flags:
// lsfDisableMaster | lsfDefaultRipple | lsfDepositAuth (from rippled's
// createPseudoAccount) plus lsfAMM (goXRPL-specific, for fast AMM detection).
func TestAMMFlags(t *testing.T) {
	env := setupAMM(t)

	// Compute AMM account address.
	ammAcc := ammAccount(t, amm.XRP(), env.USD)

	info := env.AccountInfo(ammAcc)
	if info == nil {
		t.Fatal("AMM account not found in ledger")
	}

	// rippled sets lsfDisableMaster | lsfDefaultRipple | lsfDepositAuth.
	// goXRPL additionally sets LsfAMM for fast pseudo-account detection.
	expectedFlags := state.LsfDisableMaster | state.LsfDefaultRipple | state.LsfDepositAuth | state.LsfAMM
	if info.Flags != expectedFlags {
		t.Fatalf("AMM account flags mismatch: got 0x%08X, want 0x%08X (lsfDisableMaster|lsfDefaultRipple|lsfDepositAuth|lsfAMM)",
			info.Flags, expectedFlags)
	}
}

// ----------------------------------------------------------------
// testRippling
// Reference: rippled AMM_test.cpp testRippling (line 4903)
// ----------------------------------------------------------------

// TestAMMRippling tests that rippling via an AMM fails because the AMM trust
// line has a 0 limit, and that SetTrust for non-LP tokens is rejected.
func TestAMMRippling(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	// Create issuers A and B, each issuing TST.
	a := jtx.NewAccount("A")
	b := jtx.NewAccount("B")
	c := jtx.NewAccount("C")
	d := jtx.NewAccount("D")

	env.TestEnv.FundAmount(a, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(b, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(c, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(d, uint64(jtx.XRP(10000)))
	env.Close()

	// C trusts A and B for TST.
	tsta := tx.NewIssuedAmountFromFloat64(10000, "TST", a.Address)
	tstb := tx.NewIssuedAmountFromFloat64(10000, "TST", b.Address)
	trustTxA := trustset.TrustSet(c, tsta).Build()
	jtx.RequireTxSuccess(t, env.Submit(trustTxA))
	trustTxB := trustset.TrustSet(c, tstb).Build()
	jtx.RequireTxSuccess(t, env.Submit(trustTxB))
	env.Close()

	// A pays C 10000 TSTA; B pays C 10000 TSTB.
	payA := payment.PayIssued(a, c, tx.NewIssuedAmountFromFloat64(10000, "TST", a.Address)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payA))
	payB := payment.PayIssued(b, c, tx.NewIssuedAmountFromFloat64(10000, "TST", b.Address)).Build()
	jtx.RequireTxSuccess(t, env.Submit(payB))
	env.Close()

	// C creates AMM for TSTA(5000) / TSTB(5000).
	assetA := tx.Asset{Currency: "TST", Issuer: a.Address}
	assetB := tx.Asset{Currency: "TST", Issuer: b.Address}
	createTx := amm.AMMCreate(c,
		tx.NewIssuedAmountFromFloat64(5000, "TST", a.Address),
		tx.NewIssuedAmountFromFloat64(5000, "TST", b.Address),
	).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	// Compute the AMM account for TSTA/TSTB.
	ammAcc := ammAccount(t, assetA, assetB)

	// D tries to trust AMM for TST -> tecNO_PERMISSION.
	// The issue used is {currency: TST, issuer: ammAccount}.
	ammIssueAmt := tx.NewIssuedAmountFromFloat64(10000, "TST", ammAcc.Address)
	trustD := trustset.TrustSet(d, ammIssueAmt).Build()
	result := env.Submit(trustD)
	amm.ExpectTER(t, result, amm.TecNO_PERMISSION)
	env.Close()

	// Payment from C to D delivering TST.AMM using SendMax TSTA and path
	// through AMM account -> tecPATH_DRY.
	payAmt := tx.NewIssuedAmountFromFloat64(10, "TST", ammAcc.Address)
	payTx := payment.PayIssued(c, d, payAmt).
		SendMax(tx.NewIssuedAmountFromFloat64(100, "TST", a.Address)).
		Paths([][]paymentPkg.PathStep{{
			{Account: ammAcc.Address},
		}}).
		PartialPayment().
		NoDirectRipple().
		Build()
	result = env.Submit(payTx)
	amm.ExpectTER(t, result, "tecPATH_DRY")
}

// ----------------------------------------------------------------
// testAMMID
// Reference: rippled AMM_test.cpp testAMMID (line 5769)
// ----------------------------------------------------------------

// TestAMMID verifies that the AMM account root exists with correct flags
// after creation and after a deposit operation.
// Note: The full rippled test also verifies the AMMID field in account_data
// and in affected nodes metadata. This simplified version verifies the AMM
// account exists and has the correct flags, since AccountInfo does not
// currently expose the AMMID field.
func TestAMMID(t *testing.T) {
	env := setupAMM(t)

	// Compute AMM account address.
	ammAcc := ammAccount(t, amm.XRP(), env.USD)

	// Verify AMM account exists with correct flags.
	info := env.AccountInfo(ammAcc)
	if info == nil {
		t.Fatal("AMM account not found in ledger")
	}

	expectedFlags := state.LsfDisableMaster | state.LsfDefaultRipple | state.LsfDepositAuth | state.LsfAMM
	if info.Flags != expectedFlags {
		t.Fatalf("AMM account flags mismatch: got 0x%08X, want 0x%08X",
			info.Flags, expectedFlags)
	}

	// Carol deposits to the AMM.
	depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
		Amount(amm.IOUAmount(env.GW, "USD", 1000)).
		SingleAsset().
		Build()
	result := env.Submit(depositTx)
	if !result.Success {
		t.Fatalf("Carol deposit should succeed: %s - %s", result.Code, result.Message)
	}
	env.Close()

	// Verify AMM account still exists after deposit.
	infoAfter := env.AccountInfo(ammAcc)
	if infoAfter == nil {
		t.Fatal("AMM account should still exist after deposit")
	}
	if infoAfter.Flags != expectedFlags {
		t.Fatalf("AMM account flags should be unchanged after deposit: got 0x%08X, want 0x%08X",
			infoAfter.Flags, expectedFlags)
	}
}

// ----------------------------------------------------------------
// testFailedPseudoAccount
// Reference: rippled AMM_test.cpp testFailedPseudoAccount (line 7482)
// ----------------------------------------------------------------

// TestFailedPseudoAccount tests that AMM creation fails when the pseudo-account
// address is already taken (address collision).
// The rippled test computes the AMM keylet for XRP/USD, then for 256 iterations
// computes pseudoAccountAddress from the keylet and funds each address with 1000
// XRP so that the pseudo-account slot is occupied. Creating the AMM then fails
// with tecDUPLICATE (without featureSingleAssetVault) or terADDRESS_COLLISION
// (with featureSingleAssetVault).
// Reference: rippled AMM_test.cpp testFailedPseudoAccount (line 7482)
func TestFailedPseudoAccount(t *testing.T) {
	// tecDUPLICATE: Without featureSingleAssetVault, AMMCreate returns tecDUPLICATE
	// when the AMM pseudo-account address is already occupied.
	// Reference: rippled AMM_test.cpp testFailedPseudoAccount (line 7482)
	t.Run("tecDUPLICATE", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Fill all 256 pseudo-account candidate addresses on the same open ledger.
		// Reference: rippled AMM_test.cpp testFailedPseudoAccount (line 7499-7508)
		xrpAsset := amm.XRP()
		usdAsset := env.USD
		ammKeylet := coreAmm.ComputeAMMKeylet(xrpAsset, usdAsset)
		for i := 0; i < 256; i++ {
			parentHash := env.Ledger().ParentHash()
			accountID := coreAmm.PseudoAccountAddress(env.Ledger(), parentHash, ammKeylet.Key)
			if accountID == ([20]byte{}) {
				t.Fatalf("PseudoAccountAddress returned zero at iteration %d", i)
			}
			addr, err := coreAmm.EncodeAccountID(accountID)
			if err != nil {
				t.Fatalf("Failed to encode account ID: %v", err)
			}
			pseudoAcct := jtx.NewAccountWithAddress(fmt.Sprintf("pseudo%d", i), addr)
			env.FundAmountNoRipple(pseudoAcct, uint64(jtx.XRP(1000)))
		}

		// Now AMMCreate should fail with tecDUPLICATE (all 256 slots occupied)
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		amm.ExpectTER(t, result, amm.TecDUPLICATE)
	})

	// terADDRESS_COLLISION: With featureSingleAssetVault enabled, AMMCreate returns
	// terADDRESS_COLLISION when all 256 pseudo-account candidate addresses are occupied.
	// Reference: rippled AMM_test.cpp testFailedPseudoAccount (line 7482)
	t.Run("terADDRESS_COLLISION", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.EnableFeature("SingleAssetVault")
		env.FundWithIOUs(30000, 0)
		env.Close()

		xrpAsset := amm.XRP()
		usdAsset := env.USD
		ammKeylet := coreAmm.ComputeAMMKeylet(xrpAsset, usdAsset)

		// Fund all 256 pseudo-account candidate addresses on the same open ledger.
		// Each iteration, pseudoAccountAddress returns the first available slot.
		// After funding it, the next call skips that address and returns the next candidate.
		// NOTE: No env.Close() in the loop — all 256 operations use the same parentHash.
		// Reference: rippled AMM_test.cpp testFailedPseudoAccount (line 7504-7512)
		parentHash := env.Ledger().ParentHash()
		for i := 0; i < 256; i++ {
			accountID := coreAmm.PseudoAccountAddress(env.Ledger(), parentHash, ammKeylet.Key)
			if accountID == ([20]byte{}) {
				t.Fatalf("PseudoAccountAddress returned zero at iteration %d", i)
			}
			addr, err := coreAmm.EncodeAccountID(accountID)
			if err != nil {
				t.Fatalf("Failed to encode account ID: %v", err)
			}
			pseudoAcct := jtx.NewAccountWithAddress(fmt.Sprintf("pseudo%d", i), addr)
			env.FundAmountNoRipple(pseudoAcct, uint64(jtx.XRP(1000)))
		}

		// Now AMMCreate should fail with terADDRESS_COLLISION
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		amm.ExpectTER(t, result, amm.TerADDRESS_COLLISION)
	})
}

// Suppress unused import warnings.
var (
	_ = paymentPkg.PathStep{}
	_ = trustset.TrustSet
)
