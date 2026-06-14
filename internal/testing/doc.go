// Package testing provides test infrastructure for XRPL transaction testing.
//
// This package is inspired by rippled's test::jtx framework and provides
// a similar API for creating deterministic test environments.
//
// # Overview
//
// The testing package provides:
//   - TestEnv: A test environment with ledger state management
//   - Account: Deterministic test accounts with keypairs
//   - Amount helpers: Functions for creating XRP and IOU amounts
//   - Transaction builders: Fluent builders for common transaction types
//   - Assertions: Test assertion helpers for common checks
//
// # Basic Usage
//
//	func TestPayment(t *testing.T) {
//	    env := testing.NewTestEnv(t)
//
//	    alice := testing.NewAccount("alice")
//	    bob := testing.NewAccount("bob")
//	    env.Fund(alice, bob)
//	    env.Close()
//
//	    // Alice sends 100 XRP to Bob. Submit auto-fills the fee and sequence.
//	    pay := payment.NewPayment(alice.Address, bob.Address,
//	        tx.NewXRPAmount(testing.XRP(100)))
//	    testing.RequireTxSuccess(t, env.Submit(pay))
//	}
//
// # TestEnv
//
// TestEnv manages a test ledger environment. It creates a genesis ledger
// with a master account and provides methods for funding accounts,
// submitting transactions, and closing ledgers.
//
//	env := testing.NewTestEnv(t)
//	env.Fund(alice)        // Fund account with 1000 XRP
//	env.FundAmount(bob, testing.XRP(500))  // Fund with specific amount
//	env.Close()            // Close ledger, advance sequence
//	env.Balance(alice)     // Get XRP balance in drops
//	env.Now()              // Get current ledger time
//
// # Account
//
// Account represents a test account with deterministic keypair derivation.
// Using the same name will always produce the same account, making tests
// reproducible.
//
//	alice := testing.NewAccount("alice")        // secp256k1 by default
//	bob := testing.NewAccountWithKeyType("bob", testing.KeyTypeEd25519)
//	master := testing.MasterAccount()           // Genesis account
//
// # Amount Helpers
//
// Amount helpers convert between XRP and drops:
//
//	testing.XRP(100)    // 100 XRP = 100,000,000 drops
//	testing.Drops(1000) // 1000 drops
//
// For issued currencies:
//
//	gateway := testing.NewAccount("gateway")
//	testing.USD(gateway, 100.50)  // $100.50 USD from gateway
//	testing.EUR(gateway, 50.00)   // 50 EUR from gateway
//	testing.IssuedCurrency(gateway, "JPY", 1000.0)  // Custom currency
//
// # Submitting Transactions
//
// Build a transaction with its package constructor, set any optional fields, and
// pass it to env.Submit. Submit auto-fills the fee and sequence when unset:
//
//	pay := payment.NewPayment(alice.Address, bob.Address, tx.NewXRPAmount(testing.XRP(100)))
//	env.Submit(pay)
//
//	ts := trustset.NewTrustSet(alice.Address, gateway.IOU("USD", 1000))
//	env.Submit(ts)
//
// TestEnv also exposes convenience helpers for common setup:
//
//	env.Trust(alice, gateway.IOU("USD", 1000)) // create a trust line
//	env.PayIOU(alice, bob, gateway, "USD", 50) // send issued currency
//	env.CreateOffer(alice, takerGets, takerPays)
//
// # Assertions
//
// Helper functions for common test assertions:
//
//	testing.RequireBalance(t, env, alice, testing.XRP(900))
//	testing.RequireBalanceXRP(t, env, alice, 900)
//	testing.RequireTxSuccess(t, result)
//	testing.RequireTxFail(t, result, testing.TecUNFUNDED_PAYMENT)
//	testing.RequireAccountExists(t, env, alice)
//
// # Clock Control
//
// The test environment uses a ManualClock that can be controlled:
//
//	env.AdvanceTime(10 * time.Second)
//	env.SetTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
//	env.Now()  // Current test time
package testing
