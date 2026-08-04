package did_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
)

// TestEnabled tests that DID transactions are disabled without the featureDID amendment.
func TestEnabled(t *testing.T) {
	runWithFeatureSets(t, testEnabled)
}

func testEnabled(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	// If the DID amendment is not enabled, you should not be able
	// to set or delete DIDs.
	env.DisableFeature("DID")

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// DIDSet should return temDISABLED
	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after failed DIDSet, got %d", env.OwnerCount(alice))
	}

	// DIDDelete should return temDISABLED
	tx2 := did.DIDDelete(alice).Build()
	result = env.Submit(tx2)
	jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	env.Close()
}

// TestAccountReserve tests that the reserve behaves as expected for DID creation.
func TestAccountReserve(t *testing.T) {
	runWithFeatureSets(t, testAccountReserve)
}

func testAccountReserve(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")

	// Fund alice enough to exist, but not enough to meet
	// the reserve for creating a DID.
	acctReserve := env.ReserveBase()
	incReserve := env.ReserveIncrement()
	baseFee := env.BaseFee()

	env.FundAmount(alice, acctReserve)
	env.Close()

	balance := env.Balance(alice)
	require.Equal(t, acctReserve, balance)
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// alice does not have enough XRP to cover the reserve for a DID
	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// Pay alice almost enough to make the reserve for a DID.
	master := env.MasterAccount()
	payTx := payment.Pay(master, alice, incReserve+2*baseFee-1).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()
	require.Equal(t, acctReserve+incReserve+baseFee-1, env.Balance(alice))

	// alice still does not have enough XRP for the reserve of a DID.
	tx2 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx2)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()
	require.Equal(t, acctReserve+incReserve-1, env.Balance(alice))
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	// Pay alice enough to make the reserve for a DID.
	payTx2 := payment.Pay(master, alice, baseFee+1).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx2))
	env.Close()
	require.Equal(t, acctReserve+incReserve+baseFee, env.Balance(alice))

	// Now alice can create a DID.
	tx3 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx3)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, acctReserve+incReserve, env.Balance(alice))
	if env.OwnerCount(alice) != 1 {
		t.Errorf("Expected owner count 1, got %d", env.OwnerCount(alice))
	}

	// alice deletes her DID.
	tx4 := did.DIDDelete(alice).Build()
	result = env.Submit(tx4)
	jtx.RequireTxSuccess(t, result)
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after delete, got %d", env.OwnerCount(alice))
	}
	env.Close()
}
