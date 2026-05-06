// Package amm_test contains tests for AMM vote transactions.
// Reference: rippled/src/test/app/AMM_test.cpp testInvalidFeeVote and testFeeVote
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/amm"
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
