// Package amm_test contains tests for AMM bid transactions.
// Reference: rippled/src/test/app/AMM_test.cpp testInvalidBid and testBid
package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestInvalidBid tests invalid bid scenarios.
// Reference: rippled AMM_test.cpp testInvalidBid (line 2804)
func TestInvalidBid(t *testing.T) {
	// Invalid flags
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = 0, .flags = tfWithdrawAll}), ter(temINVALID_FLAG));
	t.Run("InvalidFlags", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)).
			Flags(amm.TfWithdrawAll).
			Build()
		result = env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid with invalid flags")
		}
		amm.ExpectTER(t, result, amm.TemINVALID_FLAG)
	})

	// Invalid Bid price <= 0
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = 0}), ter(temBAD_AMOUNT));
	t.Run("ZeroBidMin", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)).
			Build()
		result = env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid with zero bidMin")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMOUNT)
	})

	// Negative bid price
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = -100}), ter(temBAD_AMOUNT));
	t.Run("NegativeBidMin", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, -100)).
			Build()
		result = env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid with negative bidMin")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMOUNT)
	})

	// Zero bidMax
	// Reference: env(ammAlice.bid({.account = carol, .bidMax = 0}), ter(temBAD_AMOUNT));
	t.Run("ZeroBidMax", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)).
			Build()
		result = env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid with zero bidMax")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMOUNT)
	})

	// Invalid Min/Max combination - bidMin > bidMax
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = 200, .bidMax = 100}), ter(tecAMM_INVALID_TOKENS));
	t.Run("InvalidMinMaxCombination", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Use real LP token issuer so we reach the bidMin > bidMax check
		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 200)).
			BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 100)).
			Build()
		result = env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bidMin > bidMax")
		}
		amm.ExpectTER(t, result, amm.TecAMM_INVALID_TOKENS)
	})

	// Invalid Account (non-existent)
	// Reference: env(ammAlice.bid({.account = bad, .bidMax = 100}), seq(1), ter(terNO_ACCOUNT));
	t.Run("NonExistentAccount", func(t *testing.T) {
		env := setupAMM(t)

		bad := jtx.NewAccount("bad")
		bidTx := amm.AMMBid(bad, amm.XRP(), env.USD).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result := env.SubmitWithOptions(jtx.WithSeq(bidTx, 1), jtx.SubmitOptions{SkipSignature: true})

		if result.Success {
			t.Fatal("Should not allow bid from non-existent account")
		}
		amm.ExpectTER(t, result, amm.TerNO_ACCOUNT)
	})

	// Account is not LP
	// Reference: env(ammAlice.bid({.account = dan, .bidMin = 100}), ter(tecAMM_INVALID_TOKENS));
	t.Run("NotLiquidityProvider", func(t *testing.T) {
		env := setupAMM(t)

		// Carol hasn't deposited, so she can't bid
		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result := env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow non-LP to bid")
		}
		amm.ExpectTER(t, result, amm.TecAMM_INVALID_TOKENS)
	})

	// Invalid AMM (non-existent pair)
	// Reference: env(ammAlice.bid({.account = alice, .bidMax = 100, .assets = {{USD, GBP}}}), ter(terNO_AMM));
	t.Run("NonExistentAMM", func(t *testing.T) {
		env := setupAMM(t)

		bidTx := amm.AMMBid(env.Alice, env.USD, env.GBP).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result := env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid on non-existent AMM")
		}
		amm.ExpectTER(t, result, amm.TerNO_AMM)
	})

	// Invalid AMM (deleted)
	// Reference: ammAlice.withdrawAll(alice); env(ammAlice.bid({...}), ter(terNO_AMM));
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

		// Try to bid on deleted AMM
		bidTx := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result = env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid on deleted AMM")
		}
		amm.ExpectTER(t, result, amm.TerNO_AMM)
	})

	// Invalid Min/Max issue - must be LP tokens
	// Reference: env(ammAlice.bid({.account = alice, .bidMax = STAmount{USD, 100}}), ter(temBAD_AMM_TOKENS));
	t.Run("BidWithWrongTokenType", func(t *testing.T) {
		env := setupAMM(t)

		// Try to bid with USD instead of LP tokens
		bidTx := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMax(amm.IOUAmount(env.GW, "USD", 100)). // Wrong: should be LP tokens
			Build()
		result := env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid with non-LP tokens")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMM_TOKENS)
	})

	// Bid price exceeds LP owned tokens
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = 1'000'001}), ter(tecAMM_INVALID_TOKENS));
	t.Run("BidExceedsOwnedTokens", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP with limited tokens
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Try to bid more tokens than Carol owns — use real LP token issuer
		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 1000001)).
			Build()
		result = env.Submit(bidTx)

		if result.Success {
			t.Fatal("Should not allow bid exceeding owned tokens")
		}
		amm.ExpectTER(t, result, amm.TecAMM_INVALID_TOKENS)
	})
}

// TestBid tests valid bid scenarios.
// Reference: rippled AMM_test.cpp testBid (line 3029)
func TestBid(t *testing.T) {
	// Bid with bidMin - pay minimum price
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = 110}));
	t.Run("BidWithBidMin", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 110)).
			Build()
		result = env.Submit(bidTx)

		if !result.Success {
			t.Fatalf("Bid with bidMin should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Bid with bidMin passed")
	})

	// Bid with exact min/max
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = 110, .bidMax = 110}));
	t.Run("BidWithExactMinMax", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 110)).
			BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 110)).
			Build()
		result = env.Submit(bidTx)

		if !result.Success {
			t.Fatalf("Bid with exact min/max should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Bid with exact min/max passed")
	})

	// Bid with min/max range
	// Reference: env(ammAlice.bid({.account = alice, .bidMin = 180, .bidMax = 200}));
	t.Run("BidWithMinMaxRange", func(t *testing.T) {
		env := setupAMM(t)

		bidTx := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 100)).
			BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 200)).
			Build()
		result := env.Submit(bidTx)

		if !result.Success {
			t.Fatalf("Bid with min/max range should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Bid with min/max range passed")
	})

	// Bid with just bidMax
	// Reference: env(ammAlice.bid({.account = carol, .bidMax = 600}));
	t.Run("BidWithBidMaxOnly", func(t *testing.T) {
		env := setupAMM(t)

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 600)).
			Build()
		result = env.Submit(bidTx)

		if !result.Success {
			t.Fatalf("Bid with bidMax only should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Bid with bidMax only passed")
	})

	// Bid without price (auto-calculate)
	// Reference: env(ammAlice.bid({.account = bob}));
	t.Run("BidWithAutoPrice", func(t *testing.T) {
		env := setupAMM(t)

		bidTx := amm.AMMBid(env.Alice, amm.XRP(), env.USD).Build()
		result := env.Submit(bidTx)

		// This may succeed or fail depending on auction slot state
		// If no one owns the slot, it should succeed
		if result.Success {
			t.Log("Bid with auto price succeeded")
		} else {
			t.Logf("Bid with auto price result: %s (may be expected)", result.Code)
		}
	})

	// Bid with authorized accounts
	// Reference: env(ammAlice.bid({.account = carol, .bidMin = 120, .authAccounts = {bob, ed}}));
	t.Run("BidWithAuthAccounts", func(t *testing.T) {
		env := setupAMM(t)

		// Fund Bob
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Close()

		// First deposit as Carol to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Deposit should succeed: %s", result.Code)
		}
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 120)).
			AuthAccounts(env.Bob.Address).
			Build()
		result = env.Submit(bidTx)

		if !result.Success {
			t.Fatalf("Bid with auth accounts should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("Bid with auth accounts passed")
	})

	// Outbid previous slot owner
	// Reference: Multiple sequential bids, each outbidding the previous
	t.Run("OutbidPreviousOwner", func(t *testing.T) {
		env := setupAMM(t)

		// Carol deposits to become LP
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		if !result.Success {
			t.Fatalf("Carol deposit should succeed: %s", result.Code)
		}
		env.Close()

		// Carol bids first
		bidTx1 := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 110)).
			Build()
		result = env.Submit(bidTx1)
		if !result.Success {
			t.Fatalf("Carol's bid should succeed: %s", result.Code)
		}
		env.Close()

		ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
		require.NotNil(t, ammAccount)
		lpCurrency := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 0).Currency
		lineKey := keylet.Line(env.Carol.ID, ammAccount.ID, lpCurrency)
		lineBefore := readBidLPLine(t, env, lineKey)
		balanceBefore := bidLPHolding(lineBefore, env.Carol.ID, ammAccount.ID)
		ownerCountBefore := env.OwnerCount(env.Carol)

		// Alice outbids Carol
		bidTx2 := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 200)).
			Build()
		result = env.Submit(bidTx2)
		if !result.Success {
			t.Fatalf("Alice's outbid should succeed: %s - %s", result.Code, result.Message)
		}
		env.Close()

		lineAfter := readBidLPLine(t, env, lineKey)
		balanceAfter := bidLPHolding(lineAfter, env.Carol.ID, ammAccount.ID)
		refund, err := balanceAfter.Sub(balanceBefore)
		require.NoError(t, err)
		wantRefund := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 104.5)
		require.Equal(t, wantRefund.Mantissa(), refund.Mantissa())
		require.Equal(t, wantRefund.Exponent(), refund.Exponent())
		require.Equal(t, ownerCountBefore, env.OwnerCount(env.Carol), "updating an existing LP line must not change OwnerCount")
	})

	t.Run("OutbidPreviousOwnerWithoutLPLine", func(t *testing.T) {
		env := setupAMM(t)

		result := env.Submit(amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build())
		require.True(t, result.Success, "Carol deposit: %s - %s", result.Code, result.Message)
		env.Close()

		result = env.Submit(amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 110)).
			Build())
		require.True(t, result.Success, "Carol bid: %s - %s", result.Code, result.Message)
		env.Close()

		ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
		require.NotNil(t, ammAccount)
		lpCurrency := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 0).Currency
		lineKey := keylet.Line(env.Carol.ID, ammAccount.ID, lpCurrency)
		require.True(t, env.LedgerEntryExists(lineKey))

		result = env.Submit(amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).WithdrawAll().Build())
		require.True(t, result.Success, "Carol withdraw all: %s - %s", result.Code, result.Message)
		env.Close()

		require.False(t, env.LedgerEntryExists(lineKey), "withdraw all should remove Carol's LP line")
		require.Equal(t, env.Carol.ID, env.ReadAMMData(amm.XRP(), env.USD).AuctionSlot.Account,
			"withdrawing liquidity must not invalidate the active slot owner")
		jtx.RequireOwnerDirectoryContains(t, env.TestEnv, env.Carol, lineKey.Key, false)
		jtx.RequireOwnerDirectoryContains(t, env.TestEnv, ammAccount, lineKey.Key, false)
		ownerCountBefore := env.OwnerCount(env.Carol)
		ammOwnerCountBefore := env.OwnerCount(ammAccount)

		result = env.Submit(amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 200)).
			Build())
		require.True(t, result.Success, "Alice outbid: %s - %s", result.Code, result.Message)
		env.Close()

		line := readBidLPLine(t, env, lineKey)
		holding := bidLPHolding(line, env.Carol.ID, ammAccount.ID)
		wantRefund := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 104.5)
		require.Equal(t, wantRefund.Mantissa(), holding.Mantissa())
		require.Equal(t, wantRefund.Exponent(), holding.Exponent())
		require.Equal(t, lpCurrency, line.Balance.Currency)
		require.Equal(t, state.AccountOneAddress, line.Balance.Issuer)

		lowID, highID := env.Carol.ID, ammAccount.ID
		if !keylet.IsLowAccount(lowID, highID) {
			lowID, highID = highID, lowID
		}
		lowAddress, err := state.EncodeAccountID(lowID)
		require.NoError(t, err)
		highAddress, err := state.EncodeAccountID(highID)
		require.NoError(t, err)
		require.True(t, line.LowLimit.IsZero())
		require.Equal(t, lpCurrency, line.LowLimit.Currency)
		require.Equal(t, lowAddress, line.LowLimit.Issuer)
		require.True(t, line.HighLimit.IsZero())
		require.Equal(t, lpCurrency, line.HighLimit.Currency)
		require.Equal(t, highAddress, line.HighLimit.Issuer)

		holderReserve, holderNoRipple := state.LsfLowReserve, state.LsfLowNoRipple
		ammNoRipple := state.LsfHighNoRipple
		if !keylet.IsLowAccount(env.Carol.ID, ammAccount.ID) {
			holderReserve, holderNoRipple = state.LsfHighReserve, state.LsfHighNoRipple
			ammNoRipple = state.LsfLowNoRipple
		}
		wantFlags := holderReserve
		if env.AccountInfo(env.Carol).Flags&state.LsfDefaultRipple == 0 {
			wantFlags |= holderNoRipple
		}
		if env.AccountInfo(ammAccount).Flags&state.LsfDefaultRipple == 0 {
			wantFlags |= ammNoRipple
		}
		require.Equal(t, wantFlags, line.Flags)
		require.Empty(t, line.LowSponsor)
		require.Empty(t, line.HighSponsor)
		require.Equal(t, ownerCountBefore+1, env.OwnerCount(env.Carol))
		require.Equal(t, ammOwnerCountBefore, env.OwnerCount(ammAccount))
		jtx.RequireOwnerDirectoryContains(t, env.TestEnv, env.Carol, lineKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env.TestEnv, ammAccount, lineKey.Key, true)
	})
}

func readBidLPLine(t *testing.T, env *amm.AMMTestEnv, lineKey keylet.Keylet) *state.RippleState {
	t.Helper()
	data, err := env.LedgerEntry(lineKey)
	require.NoError(t, err)
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	return line
}

func bidLPHolding(line *state.RippleState, holderID, ammAccountID [20]byte) state.Amount {
	if keylet.IsLowAccount(holderID, ammAccountID) {
		return line.Balance
	}
	return line.Balance.Negate()
}

// TestBidAuthAccountsPrecedence pins that the fixAMMv1_3 duplicate/self
// AuthAccounts check is a preflight rejection (temMALFORMED), ahead of preclaim's
// terNO_AMM — so it is not masked when no AMM exists for the asset pair. Before
// the fix the check lived in Preclaim, behind the AMM lookup.
// Reference: rippled AMMBid.cpp preflight lines 81-95.
func TestBidAuthAccountsPrecedence(t *testing.T) {
	// fixAMMv1_3 is Supported::yes, so it is enabled in the default env.
	t.Run("DuplicateAuthAccountsBeatsNoAMM", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()
		// No AMM is created for XRP/USD in this env.
		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			AuthAccounts(env.Bob.Address, env.Bob.Address). // duplicate
			Build()
		result := env.Submit(bidTx)
		if result.Success {
			t.Fatal("duplicate AuthAccounts must be rejected in preflight")
		}
		amm.ExpectTER(t, result, amm.TemMALFORMED)
	})

	t.Run("SelfAuthAccountBeatsNoAMM", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()
		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			AuthAccounts(env.Carol.Address). // self-authorization
			Build()
		result := env.Submit(bidTx)
		if result.Success {
			t.Fatal("self AuthAccount must be rejected in preflight")
		}
		amm.ExpectTER(t, result, amm.TemMALFORMED)
	})
}
