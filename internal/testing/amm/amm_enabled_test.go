// Package amm_test contains tests for AMM transactions.
// Reference: rippled/src/test/app/AMM_test.cpp
package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/testing/amm"
)

// TestEnabled tests that AMM operations are disabled without the amendment and enabled with it.
// Reference: rippled doesn't have an explicit testEnabled for AMM, but the amendment
// check pattern is consistent across all amendment-gated transactions.
func TestEnabled(t *testing.T) {
	// Test 1: With amendment DISABLED, all AMM transactions should return temDISABLED
	t.Run("Disabled", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Disable AMM amendment (deferred — Close to apply it before submitting).
		env.DisableFeature("AMM")
		env.Close()

		// AMMCreate should fail
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		if result.Code != "temDISABLED" {
			t.Errorf("AMMCreate: expected temDISABLED, got %s", result.Code)
		}

		// AMMDeposit should fail (using fake AMM asset)
		depositTx := amm.AMMDeposit(env.Alice, env.USD, amm.XRP()).
			Amount(amm.XRPAmount(1000)).
			Build()
		result = env.Submit(depositTx)
		if result.Code != "temDISABLED" {
			t.Errorf("AMMDeposit: expected temDISABLED, got %s", result.Code)
		}

		// AMMWithdraw should fail
		withdrawTx := amm.AMMWithdraw(env.Alice, env.USD, amm.XRP()).
			Amount(amm.XRPAmount(100)).
			Build()
		result = env.Submit(withdrawTx)
		if result.Code != "temDISABLED" {
			t.Errorf("AMMWithdraw: expected temDISABLED, got %s", result.Code)
		}

		// AMMVote should fail
		voteTx := amm.AMMVote(env.Alice, env.USD, amm.XRP(), 500).Build()
		result = env.Submit(voteTx)
		if result.Code != "temDISABLED" {
			t.Errorf("AMMVote: expected temDISABLED, got %s", result.Code)
		}

		// AMMBid should fail
		bidTx := amm.AMMBid(env.Alice, env.USD, amm.XRP()).Build()
		result = env.Submit(bidTx)
		if result.Code != "temDISABLED" {
			t.Errorf("AMMBid: expected temDISABLED, got %s", result.Code)
		}

		// AMMDelete should fail
		deleteTx := amm.AMMDelete(env.Alice, env.USD, amm.XRP()).Build()
		result = env.Submit(deleteTx)
		if result.Code != "temDISABLED" {
			t.Errorf("AMMDelete: expected temDISABLED, got %s", result.Code)
		}
	})

	// Test 2: With amendment ENABLED, AMM transactions should work
	t.Run("Enabled", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create AMM (amendment enabled by default)
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)
		if !result.Success {
			t.Fatalf("Failed to create AMM: %s - %s", result.Code, result.Message)
		}
		env.Close()
	})

	t.Log("testEnabled passed")
}

// TestFixUniversalNumberDisabled tests that AMM requires fixUniversalNumber amendment.
func TestFixUniversalNumberDisabled(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.FundWithIOUs(30000, 0)
	env.Close()

	// Disable fixUniversalNumber (AMM requires both AMM and fixUniversalNumber).
	env.DisableFeature("fixUniversalNumber")
	env.Close()

	// AMMCreate should fail
	createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
	result := env.Submit(createTx)
	if result.Code != "temDISABLED" {
		t.Errorf("AMMCreate without fixUniversalNumber: expected temDISABLED, got %s", result.Code)
	}
}

// TestAMMClawbackEnabled tests the AMMClawback amendment requirement.
func TestAMMClawbackEnabled(t *testing.T) {
	t.Run("Disabled", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Disable AMMClawback amendment (deferred — Close to apply it).
		env.DisableFeature("AMMClawback")
		env.Close()

		// AMMClawback requires: AMM, fixUniversalNumber, and AMMClawback
		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).Build()
		result := env.Submit(clawbackTx)
		if result.Code != "temDISABLED" {
			t.Errorf("AMMClawback: expected temDISABLED, got %s", result.Code)
		}
	})

	t.Run("Enabled", func(t *testing.T) {
		// With amendment enabled, the transaction should proceed to other validation
		// (may fail for other reasons like no AMM exists, but not temDISABLED)
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		clawbackTx := amm.AMMClawback(env.GW, env.Alice.Address, env.USD, amm.XRP()).Build()
		result := env.Submit(clawbackTx)
		// Should not be temDISABLED since amendments are enabled
		if result.Code == "temDISABLED" {
			t.Errorf("AMMClawback with amendment enabled should not return temDISABLED")
		}
	})
}
