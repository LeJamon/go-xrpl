// Package networkid_test tests NetworkID validation behavior.
// Reference: rippled/src/test/app/NetworkID_test.cpp
package networkid_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx/account"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
)

// TestNetworkID_Mainnet tests that on mainnet (NetworkID=0), transactions
// without NetworkID succeed, and those with NetworkID fail.
// Reference: rippled NetworkID_test.cpp testNetworkID() lines 78-97
func TestNetworkID_Mainnet(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetNetworkID(0)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// Transaction without NetworkID → success
	t.Run("No NetworkID succeeds", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		result := env.Submit(noop)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Transaction with NetworkID=0 → telNETWORK_ID_MAKES_TX_NON_CANONICAL
	t.Run("NetworkID=0 fails", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		nid := uint32(0)
		noop.GetCommon().NetworkID = &nid
		result := env.Submit(noop)
		jtx.RequireTxFail(t, result, "telNETWORK_ID_MAKES_TX_NON_CANONICAL")
	})

	// Transaction with NetworkID=10000 → telNETWORK_ID_MAKES_TX_NON_CANONICAL
	t.Run("NetworkID=10000 fails", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		nid := uint32(10000)
		noop.GetCommon().NetworkID = &nid
		result := env.Submit(noop)
		jtx.RequireTxFail(t, result, "telNETWORK_ID_MAKES_TX_NON_CANONICAL")
	})
}

// TestNetworkID_Legacy1024 tests that network ID=1024 (legacy boundary) behaves
// like mainnet: no NetworkID allowed in transactions.
// Reference: rippled NetworkID_test.cpp testNetworkID() lines 99-117
func TestNetworkID_Legacy1024(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetNetworkID(1024)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// No NetworkID → success
	t.Run("No NetworkID succeeds", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		result := env.Submit(noop)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// NetworkID=1024 → telNETWORK_ID_MAKES_TX_NON_CANONICAL
	t.Run("NetworkID=1024 fails", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		nid := uint32(1024)
		noop.GetCommon().NetworkID = &nid
		result := env.Submit(noop)
		jtx.RequireTxFail(t, result, "telNETWORK_ID_MAKES_TX_NON_CANONICAL")
	})

	// NetworkID=1000 → telNETWORK_ID_MAKES_TX_NON_CANONICAL
	t.Run("NetworkID=1000 fails", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		nid := uint32(1000)
		noop.GetCommon().NetworkID = &nid
		result := env.Submit(noop)
		jtx.RequireTxFail(t, result, "telNETWORK_ID_MAKES_TX_NON_CANONICAL")
	})
}

// TestNetworkID_DevNet1025 tests that network ID=1025 (above legacy threshold)
// requires NetworkID in transactions and rejects mismatches.
// Reference: rippled NetworkID_test.cpp testNetworkID() lines 119-158
func TestNetworkID_DevNet1025(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	// Fund alice before setting network ID (Fund() doesn't set NetworkID on txns)
	env.Fund(alice)
	env.Close()
	env.SetNetworkID(1025)

	// No NetworkID → telREQUIRES_NETWORK_ID
	t.Run("No NetworkID fails", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		result := env.Submit(noop)
		jtx.RequireTxFail(t, result, "telREQUIRES_NETWORK_ID")
	})

	// Wrong NetworkID=0 → telWRONG_NETWORK
	t.Run("Wrong NetworkID=0 fails", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		nid := uint32(0)
		noop.GetCommon().NetworkID = &nid
		result := env.Submit(noop)
		jtx.RequireTxFail(t, result, "telWRONG_NETWORK")
	})

	// Wrong NetworkID=1024 → telWRONG_NETWORK
	t.Run("Wrong NetworkID=1024 fails", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		nid := uint32(1024)
		noop.GetCommon().NetworkID = &nid
		result := env.Submit(noop)
		jtx.RequireTxFail(t, result, "telWRONG_NETWORK")
	})

	// Correct NetworkID=1025 → success
	t.Run("Correct NetworkID=1025 succeeds", func(t *testing.T) {
		noop := account.NewAccountSet(alice.Address)
		nid := uint32(1025)
		noop.GetCommon().NetworkID = &nid
		result := env.Submit(noop)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}
