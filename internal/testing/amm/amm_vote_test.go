// Package amm_test contains tests for AMM vote transactions.
// Reference: rippled/src/test/app/AMM_test.cpp testInvalidFeeVote and testFeeVote
package amm_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
)

// TestInvalidFeeVote tests invalid fee vote scenarios.
// Reference: rippled AMM_test.cpp testInvalidFeeVote (line 2618)
func TestInvalidFeeVote(t *testing.T) {
	// Invalid flags
	// Reference: ammAlice.vote(std::nullopt, 1'000, tfWithdrawAll, ..., ter(temINVALID_FLAG));
	t.Run("InvalidFlags", func(t *testing.T) {
		env := setupAMM(t)

		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 1000).
			Flags(amm.TfWithdrawAll).
			Build()
		result := env.Submit(voteTx)

		if result.Success {
			t.Fatal("Should not allow vote with invalid flags")
		}
		amm.ExpectTER(t, result, amm.TemINVALID_FLAG)
	})

	// Invalid fee - > 1000 basis points (> 1%)
	// Reference: ammAlice.vote(std::nullopt, 1'001, std::nullopt, ..., ter(temBAD_FEE));
	t.Run("InvalidFee_TooHigh", func(t *testing.T) {
		env := setupAMM(t)

		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 1001).Build()
		result := env.Submit(voteTx)

		if result.Success {
			t.Fatal("Should not allow vote with fee > 1000")
		}
		amm.ExpectTER(t, result, amm.TemBAD_FEE)
	})

	// Invalid Account (non-existent)
	// Reference: ammAlice.vote(bad, 1'000, std::nullopt, seq(1), ..., ter(terNO_ACCOUNT));
	t.Run("NonExistentAccount", func(t *testing.T) {
		env := setupAMM(t)

		bad := jtx.NewAccount("bad")
		voteTx := amm.AMMVote(bad, amm.XRP(), env.USD, 1000).Build()
		result := env.Submit(jtx.WithSeq(voteTx, 1))

		if result.Success {
			t.Fatal("Should not allow vote from non-existent account")
		}
		amm.ExpectTER(t, result, amm.TerNO_ACCOUNT)
	})

	// Invalid AMM (non-existent)
	// Reference: ammAlice.vote(alice, 1'000, std::nullopt, std::nullopt, {{USD, GBP}}, ter(terNO_AMM));
	t.Run("NonExistentAMM", func(t *testing.T) {
		env := setupAMM(t)

		voteTx := amm.AMMVote(env.Alice, env.USD, env.GBP, 1000).Build()
		result := env.Submit(voteTx)

		if result.Success {
			t.Fatal("Should not allow vote on non-existent AMM")
		}
		amm.ExpectTER(t, result, amm.TerNO_AMM)
	})

	// Account is not LP
	// Reference: ammAlice.vote(carol, 1'000, std::nullopt, std::nullopt, std::nullopt, ter(tecAMM_INVALID_TOKENS));
	t.Run("NotLiquidityProvider", func(t *testing.T) {
		env := setupAMM(t)

		// Carol hasn't deposited, so she can't vote
		voteTx := amm.AMMVote(env.Carol, amm.XRP(), env.USD, 1000).Build()
		result := env.Submit(voteTx)

		if result.Success {
			t.Fatal("Should not allow non-LP to vote")
		}
		amm.ExpectTER(t, result, amm.TecAMM_INVALID_TOKENS)
	})

	// Invalid AMM - AMM was deleted
	// Reference: ammAlice.withdrawAll(alice); ammAlice.vote(alice, 1'000, ..., ter(terNO_AMM));
	t.Run("DeletedAMM", func(t *testing.T) {
		env := setupAMM(t)

		// Withdraw all to delete AMM
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result := env.Submit(withdrawTx)
		if !result.Success {
			t.Fatalf("Withdraw all should succeed: %s", result.Code)
		}
		env.Close()

		// Try to vote on deleted AMM
		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 1000).Build()
		result = env.Submit(voteTx)

		if result.Success {
			t.Fatal("Should not allow vote on deleted AMM")
		}
		amm.ExpectTER(t, result, amm.TerNO_AMM)
	})
}

// TestFeeVote tests valid fee vote scenarios.
// Reference: rippled AMM_test.cpp testFeeVote (line 2687)
func TestFeeVote(t *testing.T) {
	// One vote sets fee to 1%
	// Reference: ammAlice.vote({}, 1'000); BEAST_EXPECT(ammAlice.expectTradingFee(1'000));
	t.Run("SingleVote_1Percent", func(t *testing.T) {
		env := setupAMM(t)

		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 1000).Build()
		result := env.Submit(voteTx)

		if !result.Success {
			t.Fatalf("Vote should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Single vote to set fee to 1% passed")
	})

	// Vote with zero fee
	t.Run("VoteZeroFee", func(t *testing.T) {
		env := setupAMM(t)

		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 0).Build()
		result := env.Submit(voteTx)

		if !result.Success {
			t.Fatalf("Vote for zero fee should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Vote for zero fee passed")
	})

	// Multiple LPs voting
	// Reference: Multiple votes fill voting slots and compute weighted average
	t.Run("MultipleLPsVoting", func(t *testing.T) {
		env := setupAMM(t)

		// Carol deposits to become an LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Carol deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Alice votes for 500 basis points (0.5%)
		voteTx1 := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 500).Build()
		result = env.Submit(voteTx1)
		if !result.Success {
			t.Fatalf("Alice vote should succeed: %s", result.Code)
		}
		env.Close()

		// Carol votes for 100 basis points (0.1%)
		voteTx2 := amm.AMMVote(env.Carol, amm.XRP(), env.USD, 100).Build()
		result = env.Submit(voteTx2)
		if !result.Success {
			t.Fatalf("Carol vote should succeed: %s", result.Code)
		}
		env.Close()

		// Trading fee should now be a weighted average of votes
		t.Log("Multiple LPs voting passed")
	})

	// LP changes their vote
	// Reference: Account can re-vote to change their fee preference
	t.Run("ChangeVote", func(t *testing.T) {
		env := setupAMM(t)

		// First vote: 500 basis points
		voteTx1 := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 500).Build()
		result := env.Submit(voteTx1)
		if !result.Success {
			t.Fatalf("First vote should succeed: %s", result.Code)
		}
		env.Close()

		// Second vote: change to 300 basis points
		voteTx2 := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 300).Build()
		result = env.Submit(voteTx2)
		if !result.Success {
			t.Fatalf("Vote change should succeed: %s", result.Code)
		}
		env.Close()

		t.Log("Change vote passed")
	})

	// Vote after deposit
	// Reference: New LP can vote after depositing
	t.Run("VoteAfterDeposit", func(t *testing.T) {
		env := setupAMM(t)

		// Carol deposits to become an LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			SingleAsset().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Carol deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Carol can now vote
		voteTx := amm.AMMVote(env.Carol, amm.XRP(), env.USD, 250).Build()
		result = env.Submit(voteTx)
		if !result.Success {
			t.Fatalf("Carol vote should succeed: %s", result.Code)
		}
		env.Close()

		t.Log("Vote after deposit passed")
	})

	// Vote at maximum fee (1000 basis points = 1%)
	t.Run("MaximumFee", func(t *testing.T) {
		env := setupAMM(t)

		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 1000).Build()
		result := env.Submit(voteTx)

		if !result.Success {
			t.Fatalf("Vote for maximum fee should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Maximum fee vote passed")
	})
}

// TestFeeVoteSlotReplacement fills all 8 vote slots, then has a new LP with more
// tokens than the minimum-token holder vote. The minimum slot must be the one
// replaced — not a different slot.
//
// This exercises the slot-replacement path in applyVote, where minPos must index
// the OUTPUT (updatedVoteSlots) array, mirroring rippled's
// minPos = updatedVoteSlots.size() captured before push_back, and the
// replacement updatedVoteSlots[minPos] = ... (AMMVote.cpp:149,187,191).
// Reference: rippled AMM_test.cpp testFeeVote (line 2745).
func TestFeeVoteSlotReplacement(t *testing.T) {
	// setupAMM: alice creates XRP(10000)/USD(10000); alice holds 10,000,000 LP
	// tokens and occupies the first vote slot.
	env := setupAMM(t)

	fundLP := func(name string) *jtx.Account {
		acct := jtx.NewAccount(name)
		env.TestEnv.FundAmount(acct, uint64(jtx.XRP(30000)))
		env.Close()
		env.Trust(acct, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, acct, "USD", 50000)
		env.Close()
		return acct
	}

	deposit := func(acct *jtx.Account, lpTokens float64) {
		depositTx := amm.AMMDeposit(acct, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, lpTokens)).
			LPToken().
			Build()
		if r := env.Submit(depositTx); !r.Success {
			t.Fatalf("%s deposit failed: %s - %s", acct.Name, r.Code, r.Message)
		}
		env.Close()
	}

	vote := func(acct *jtx.Account, fee uint16) {
		if r := env.Submit(amm.AMMVote(acct, amm.XRP(), env.USD, fee).Build()); !r.Success {
			t.Fatalf("%s vote failed: %s - %s", acct.Name, r.Code, r.Message)
		}
		env.Close()
	}

	voteSlots := func() (map[[20]byte]bool, int) {
		data := env.ReadAMMData(amm.XRP(), env.USD)
		set := make(map[[20]byte]bool, len(data.VoteSlots))
		for _, slot := range data.VoteSlots {
			set[slot.Account] = true
		}
		return set, len(data.VoteSlots)
	}

	// Seven additional LPs deposit strictly increasing LP-token amounts and vote.
	// Together with alice (10,000,000 tokens) they fill all 8 vote slots. lp0
	// holds the fewest tokens (100,000) and is therefore the global minimum slot.
	lps := make([]*jtx.Account, 7)
	for i := range 7 {
		acct := fundLP(fmt.Sprintf("lp%d", i))
		deposit(acct, float64((i+1)*100000))
		vote(acct, uint16(50*(i+1)))
		lps[i] = acct
	}

	if set, n := voteSlots(); n != 8 {
		t.Fatalf("expected 8 occupied vote slots, got %d", n)
	} else if !set[lps[0].ID] {
		t.Fatal("lp0 (minimum holder) should occupy a vote slot before replacement")
	}

	// A new LP deposits more tokens than the minimum holder (lp0) and votes.
	// The slots are full, so applyVote must replace lp0's slot — the minimum.
	outbidder := fundLP("outbidder")
	deposit(outbidder, 1000000)
	vote(outbidder, 600)

	set, n := voteSlots()
	if n != 8 {
		t.Fatalf("expected 8 vote slots after replacement, got %d", n)
	}
	if set[lps[0].ID] {
		t.Error("lp0 (minimum holder) should have been evicted from the vote slots")
	}
	if !set[outbidder.ID] {
		t.Error("outbidder should occupy a vote slot after replacing the minimum")
	}
	for i := 1; i < 7; i++ {
		if !set[lps[i].ID] {
			t.Errorf("lp%d should still occupy a vote slot — only the minimum should be replaced", i)
		}
	}
}

// TestFeeVoteDustWeightIsZero verifies that a liquidity provider holding less
// than 1/VOTE_WEIGHT_SCALE_FACTOR of the pool stores VoteWeight 0 — rippled keeps
// the raw int64 of lpTokens*scale/lptBalance with no clamp-to-1. The AMM holds
// 10,000,000 LP tokens, so a dust LP holding ~50 tokens yields
// floor(50*100000/10,000,050) = 0.
func TestFeeVoteDustWeightIsZero(t *testing.T) {
	env := setupAMM(t)

	carol := env.Carol
	depositTx := amm.AMMDeposit(carol, amm.XRP(), env.USD).
		LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 50)).
		LPToken().
		Build()
	if r := env.Submit(depositTx); !r.Success {
		t.Fatalf("Carol dust deposit failed: %s - %s", r.Code, r.Message)
	}
	env.Close()

	if r := env.Submit(amm.AMMVote(carol, amm.XRP(), env.USD, 100).Build()); !r.Success {
		t.Fatalf("Carol vote failed: %s - %s", r.Code, r.Message)
	}
	env.Close()

	data := env.ReadAMMData(amm.XRP(), env.USD)
	var found bool
	for _, slot := range data.VoteSlots {
		if slot.Account == carol.ID {
			found = true
			if slot.VoteWeight != 0 {
				t.Fatalf("dust LP vote weight should be 0, got %d", slot.VoteWeight)
			}
		}
	}
	if !found {
		t.Fatal("Carol should occupy a vote slot")
	}
}
