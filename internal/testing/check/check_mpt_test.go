package check_test

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	checkbuilder "github.com/LeJamon/go-xrpl/internal/testing/check"
	mptbuilder "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestCheck_MPTLifecycle(t *testing.T) {
	t.Run("ExactAmountCreatesHolding", func(t *testing.T) {
		env, token, alice, bob := newCheckMPTFixture(t, 0, 0)
		token.Pay(token.Issuer(), alice, 100)

		checkID := checkbuilder.GetCheckID(alice, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(alice, bob, token.MPTAmount(100)).Build()))
		env.Close()
		requireCheckMPTExists(t, env, checkID, true)
		jtx.RequireOwnerCount(t, env, alice, 2)
		jtx.RequireOwnerCount(t, env, bob, 0)

		result := env.Submit(checkbuilder.CheckCashAmount(bob, checkID, token.MPTAmount(100)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		token.RequireMPTokenAmount(alice, 0)
		token.RequireMPTokenAmount(bob, 100)
		requireDeliveredAmount(t, result, token.MPTAmount(100))
		requireCheckMPTExists(t, env, checkID, false)
		jtx.RequireOwnerCount(t, env, alice, 1)
		jtx.RequireOwnerCount(t, env, bob, 1)
	})

	t.Run("DeliverMinTransferFeeRounding", func(t *testing.T) {
		fee := uint16(25_000)
		env, token, alice, bob := newCheckMPTFixture(t, 0, fee)
		token.Pay(token.Issuer(), alice, 1_000)
		checkID := checkbuilder.GetCheckID(alice, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(alice, bob, token.MPTAmount(125)).Build()))
		env.Close()

		result := env.Submit(checkbuilder.CheckCashDeliverMin(bob, checkID, token.MPTAmount(75)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		token.RequireMPTokenAmount(alice, 875)
		token.RequireMPTokenAmount(bob, 100)
		requireDeliveredAmount(t, result, token.MPTAmount(100))
		requireCheckMPTExists(t, env, checkID, false)
	})

	t.Run("LockedIssuancePreservesCheck", func(t *testing.T) {
		env, token, alice, bob := newCheckMPTFixture(t, mptbuilder.TfMPTCanLock, 0)
		token.Pay(token.Issuer(), alice, 100)
		checkID := checkbuilder.GetCheckID(alice, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(alice, bob, token.MPTAmount(100)).Build()))
		env.Close()
		token.Set(mptbuilder.SetOpts{Flags: mptbuilder.TfMPTLock})
		env.Close()

		jtx.RequireTxClaimed(t,
			env.Submit(checkbuilder.CheckCashAmount(bob, checkID, token.MPTAmount(100)).Build()),
			jtx.TecPATH_PARTIAL,
		)
		env.Close()
		requireCheckMPTExists(t, env, checkID, true)
		token.RequireMPTokenAmount(alice, 100)
		token.RequireMPTokenAmount(bob, 0)

		token.Set(mptbuilder.SetOpts{Flags: mptbuilder.TfMPTUnlock})
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCancel(bob, checkID).Build()))
		env.Close()
		requireCheckMPTExists(t, env, checkID, false)
	})

	t.Run("RequireAuthPreservesCheck", func(t *testing.T) {
		env, token, alice, bob := newCheckMPTFixture(t, mptbuilder.TfMPTRequireAuth, 0)
		token.Authorize(mptbuilder.AuthorizeOpts{Holder: alice})
		token.Pay(token.Issuer(), alice, 100)
		checkID := checkbuilder.GetCheckID(alice, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(alice, bob, token.MPTAmount(100)).Build()))
		env.Close()

		jtx.RequireTxClaimed(t,
			env.Submit(checkbuilder.CheckCashAmount(bob, checkID, token.MPTAmount(100)).Build()),
			jtx.TecNO_AUTH,
		)
		env.Close()
		requireCheckMPTExists(t, env, checkID, true)
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCancel(alice, checkID).Build()))
		env.Close()
		requireCheckMPTExists(t, env, checkID, false)
	})
}

func TestCheck_MPTokensV2Disabled(t *testing.T) {
	t.Run("Create", func(t *testing.T) {
		env, token, alice, bob := newCheckMPTFixture(t, 0, 0)
		env.DisableFeature("MPTokensV2")
		env.Close()

		result := env.Submit(checkbuilder.CheckCreate(alice, bob, token.MPTAmount(1)).Build())
		jtx.RequireTxFail(t, result, jtx.TemDISABLED)
	})

	t.Run("Cash", func(t *testing.T) {
		env, token, alice, bob := newCheckMPTFixture(t, 0, 0)
		token.Pay(token.Issuer(), alice, 1)
		checkID := checkbuilder.GetCheckID(alice, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(alice, bob, token.MPTAmount(1)).Build()))
		env.Close()
		env.DisableFeature("MPTokensV2")
		env.Close()

		result := env.Submit(checkbuilder.CheckCashAmount(bob, checkID, token.MPTAmount(1)).Build())
		jtx.RequireTxFail(t, result, jtx.TemDISABLED)
		requireCheckMPTExists(t, env, checkID, true)
	})
}

func TestCheck_MPTWithTickets(t *testing.T) {
	env, token, alice, bob := newCheckMPTFixture(t, 0, 0)
	token.Pay(token.Issuer(), alice, 100)
	aliceTicket := env.CreateTickets(alice, 1)
	bobTicket := env.CreateTickets(bob, 1)
	aliceSequence := env.Seq(alice)
	bobSequence := env.Seq(bob)
	env.Close()

	checkID := checkbuilder.GetCheckID(alice, aliceTicket)
	create := checkbuilder.CheckCreate(alice, bob, token.MPTAmount(100)).Build()
	jtx.WithTicketSeq(create, aliceTicket)
	jtx.RequireTxSuccess(t, env.Submit(create))
	env.Close()
	jtx.RequireSequence(t, env, alice, aliceSequence)

	cash := checkbuilder.CheckCashAmount(bob, checkID, token.MPTAmount(100)).Build()
	jtx.WithTicketSeq(cash, bobTicket)
	jtx.RequireTxSuccess(t, env.Submit(cash))
	env.Close()
	jtx.RequireSequence(t, env, bob, bobSequence)
	token.RequireMPTokenAmount(alice, 0)
	token.RequireMPTokenAmount(bob, 100)
	requireCheckMPTExists(t, env, checkID, false)
}

func newCheckMPTFixture(t *testing.T, extraFlags uint32, transferFee uint16) (*jtx.TestEnv, *mptbuilder.MPTTester, *jtx.Account, *jtx.Account) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("MPTokensV2")
	issuer := jtx.NewAccount("mpt-issuer")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	token := mptbuilder.NewMPTTester(t, env, issuer, mptbuilder.MPTInit{Holders: []*jtx.Account{alice, bob}})
	maximum := uint64(10_000)
	token.Create(mptbuilder.CreateOpts{
		Flags:       mptbuilder.TfMPTCanTransfer | extraFlags,
		MaxAmt:      &maximum,
		TransferFee: optionalCheckMPTFee(transferFee),
	})
	env.Close()
	token.Authorize(mptbuilder.AuthorizeOpts{Account: alice})
	env.Close()
	return env, token, alice, bob
}

func optionalCheckMPTFee(fee uint16) *uint16 {
	if fee == 0 {
		return nil
	}
	return &fee
}

func requireCheckMPTExists(t *testing.T, env *jtx.TestEnv, checkID string, expected bool) {
	t.Helper()
	decoded, err := hex.DecodeString(checkID)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("decode CheckID %q: %v", checkID, err)
	}
	var key [32]byte
	copy(key[:], decoded)
	exists, err := env.Ledger().Exists(keylet.Keylet{Key: key})
	if err != nil {
		t.Fatalf("read Check %s: %v", checkID, err)
	}
	if exists != expected {
		t.Fatalf("Check %s existence = %v, want %v", checkID, exists, expected)
	}
}
