package did_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
)

func TestEnabled(t *testing.T) {
	runWithFeatureSets(t, testEnabled)
}

func testEnabled(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	env.DisableFeature("DID")

	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	env.Close()

	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after failed DIDSet, got %d", env.OwnerCount(alice))
	}

	tx2 := did.DIDDelete(alice).Build()
	result = env.Submit(tx2)
	jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	env.Close()
}

func TestAccountReserve(t *testing.T) {
	runWithFeatureSets(t, testAccountReserve)
}

func testAccountReserve(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	alice := jtx.NewAccount("alice")

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

	tx1 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result := env.Submit(tx1)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()
	requireDIDAbsent(t, env, alice)
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	master := env.MasterAccount()
	payTx := payment.Pay(master, alice, incReserve+2*baseFee-1).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))
	env.Close()
	require.Equal(t, acctReserve+incReserve+baseFee-1, env.Balance(alice))

	tx2 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx2)
	jtx.RequireTxClaimed(t, result, jtx.TecINSUFFICIENT_RESERVE)
	env.Close()
	requireDIDAbsent(t, env, alice)
	require.Equal(t, acctReserve+incReserve-1, env.Balance(alice))
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0, got %d", env.OwnerCount(alice))
	}

	payTx2 := payment.Pay(master, alice, baseFee+1).Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx2))
	env.Close()
	require.Equal(t, acctReserve+incReserve+baseFee, env.Balance(alice))

	tx3 := did.DIDSet(alice).URI("uri").Document("doc").Data("data").Build()
	result = env.Submit(tx3)
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, acctReserve+incReserve, env.Balance(alice))
	if env.OwnerCount(alice) != 1 {
		t.Errorf("Expected owner count 1, got %d", env.OwnerCount(alice))
	}

	tx4 := did.DIDDelete(alice).Build()
	result = env.Submit(tx4)
	jtx.RequireTxSuccess(t, result)
	if env.OwnerCount(alice) != 0 {
		t.Errorf("Expected owner count 0 after delete, got %d", env.OwnerCount(alice))
	}
	env.Close()
}
