package amm_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestInvalidBid(t *testing.T) {
	t.Run("InvalidFlags", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)).
			Flags(amm.TfWithdrawAll).
			Build()
		result = env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TemINVALID_FLAG.String())
	})

	t.Run("ZeroBidMin", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)).
			Build()
		result = env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TemBAD_AMOUNT.String())
	})

	t.Run("NegativeBidMin", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, -100)).
			Build()
		result = env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TemBAD_AMOUNT.String())
	})

	t.Run("ZeroBidMax", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)).
			Build()
		result = env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TemBAD_AMOUNT.String())
	})

	t.Run("InvalidMinMaxCombination", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 200)).
			BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 100)).
			Build()
		result = env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TecAMM_INVALID_TOKENS.String())
	})

	t.Run("NonExistentAccount", func(t *testing.T) {
		env := setupAMM(t)

		bad := jtx.NewAccount("bad")
		bidTx := amm.AMMBid(bad, amm.XRP(), env.USD).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result := env.SubmitWithOptions(jtx.WithSeq(bidTx, 1), jtx.SubmitOptions{SkipSignature: true})

		jtx.RequireTxFail(t, result, ter.TerNO_ACCOUNT.String())
	})

	t.Run("NotLiquidityProvider", func(t *testing.T) {
		env := setupAMM(t)

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result := env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TecAMM_INVALID_TOKENS.String())
	})

	t.Run("NonExistentAMM", func(t *testing.T) {
		env := setupAMM(t)

		bidTx := amm.AMMBid(env.Alice, env.USD, env.GBP).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result := env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TerNO_AMM.String())
	})

	t.Run("DeletedAMM", func(t *testing.T) {
		env := setupAMM(t)

		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			WithdrawAll().
			Build()
		result := env.Submit(withdrawTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMax(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result = env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TerNO_AMM.String())
	})

	t.Run("BidWithWrongTokenType", func(t *testing.T) {
		env := setupAMM(t)

		bidTx := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMax(amm.IOUAmount(env.GW, "USD", 100)). // Wrong: should be LP tokens
			Build()
		result := env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TemBAD_AMM_TOKENS.String())
	})

	t.Run("BidExceedsOwnedTokens", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 1000001)).
			Build()
		result = env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TecAMM_INVALID_TOKENS.String())
	})
}

func TestBid(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			deposit  bool
			min, max float64
			auth     bool
			price    string
		}{
			{name: "BidWithBidMin", deposit: true, min: 110, price: "110"},
			{name: "BidWithExactMinMax", deposit: true, min: 110, max: 110, price: "110"},
			{name: "BidWithMinMaxRange", min: 100, max: 200, price: "100"},
			{name: "BidWithBidMaxOnly", deposit: true, max: 600, price: "4.4"},
			{name: "BidWithAutoPrice", price: "4"},
			{name: "BidWithAuthAccounts", deposit: true, min: 120, auth: true, price: "120"},
		} {
			t.Run(fmt.Sprintf("%s/cleanup=%t", tc.name, cleanup), func(t *testing.T) {
				env := setupAMMProfile(t, true)
				if !cleanup {
					env.DisableFeature("fixCleanup3_4_0")
					env.Close()
				}
				bidder := env.Alice
				if tc.deposit {
					bidder = env.Carol
					jtx.RequireTxSuccess(t, env.Submit(amm.AMMDeposit(bidder, amm.XRP(), env.USD).
						LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).LPToken().Build()))
					env.Close()
				}
				if tc.auth {
					env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
					env.Close()
				}
				before := captureBidState(t, env, bidder)
				bid := amm.AMMBid(bidder, amm.XRP(), env.USD)
				if tc.min != 0 {
					bid.BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, tc.min))
				}
				if tc.max != 0 {
					bid.BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, tc.max))
				}
				var auth [][20]byte
				if tc.auth {
					bid.AuthAccounts(env.Bob.Address)
					auth = append(auth, env.Bob.ID)
				}
				price := tc.price
				if !cleanup && tc.min == 0 {
					price = "0"
				}
				jtx.RequireTxSuccess(t, env.Submit(bid.Build()))
				assertBidState(t, env, bidder, before, price, "0", 0, auth)
			})
		}
	}

	t.Run("DiscountAndAuthAccountsReset", func(t *testing.T) {
		env := setupAMMProfile(t, true)
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(amm.AMMVote(env.Alice, amm.XRP(), env.USD, 1000).Build()))
		before := captureBidState(t, env, env.Alice)
		jtx.RequireTxSuccess(t, env.Submit(amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 5000)).
			AuthAccounts(env.Bob.Address, env.Carol.Address).Build()))
		assertBidState(t, env, env.Alice, before, "5000", "0", 100, [][20]byte{env.Bob.ID, env.Carol.ID})
		before = captureBidState(t, env, env.Alice)
		jtx.RequireTxSuccess(t, env.Submit(amm.AMMBid(env.Alice, amm.XRP(), env.USD).Build()))
		assertBidState(t, env, env.Alice, before, "9248", "4750", 100, nil)
	})

	t.Run("OutbidPreviousOwner", func(t *testing.T) {
		env := setupAMMProfile(t, true)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		bidTx1 := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 110)).
			Build()
		result = env.Submit(bidTx1)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
		require.NotNil(t, ammAccount)
		lpCurrency := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 0).Currency
		lineKey := keylet.Line(env.Carol.ID, ammAccount.ID, lpCurrency)
		lineBefore := readBidLPLine(t, env, lineKey)
		balanceBefore := bidLPHolding(lineBefore, env.Carol.ID, ammAccount.ID)
		ownerCountBefore := env.OwnerCount(env.Carol)

		beforeBid := captureBidState(t, env, env.Alice)

		bidTx2 := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 200)).
			Build()
		result = env.Submit(bidTx2)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		assertBidState(t, env, env.Alice, beforeBid, "200", "104.5", 0, nil)
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
		env := setupAMMProfile(t, true)

		result := env.Submit(amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 110)).
			Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		ammAccount := env.ReadAMMAccount(amm.XRP(), env.USD)
		require.NotNil(t, ammAccount)
		lpCurrency := env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 0).Currency
		lineKey := keylet.Line(env.Carol.ID, ammAccount.ID, lpCurrency)
		require.True(t, env.LedgerEntryExists(lineKey))

		result = env.Submit(amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).WithdrawAll().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, env.LedgerEntryExists(lineKey), "withdraw all should remove Carol's LP line")
		require.Equal(t, env.Carol.ID, env.ReadAMMData(amm.XRP(), env.USD).AuctionSlot.Account,
			"withdrawing liquidity must not invalidate the active slot owner")
		jtx.RequireOwnerDirectoryContains(t, env.TestEnv, env.Carol, lineKey.Key, false)
		jtx.RequireOwnerDirectoryContains(t, env.TestEnv, ammAccount, lineKey.Key, false)
		ownerCountBefore := env.OwnerCount(env.Carol)
		ammOwnerCountBefore := env.OwnerCount(ammAccount)

		beforeBid := captureBidState(t, env, env.Alice)
		result = env.Submit(amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 200)).
			Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		assertBidState(t, env, env.Alice, beforeBid, "200", "104.5", 0, nil)
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

type bidState struct {
	pool                              *jtx.Account
	owner                             [20]byte
	total, bidderLP, ownerLP, poolUSD tx.Amount
	poolXRP, bidderXRP                uint64
	expiration                        uint32
}

func captureBidState(t *testing.T, env *amm.AMMTestEnv, bidder *jtx.Account) bidState {
	t.Helper()
	data := env.ReadAMMData(amm.XRP(), env.USD)
	require.NotNil(t, data)
	require.NotNil(t, data.AuctionSlot)
	pool := env.ReadAMMAccount(amm.XRP(), env.USD)
	require.NotNil(t, pool)
	usd := env.IOUBalance(pool, env.GW, "USD")
	require.NotNil(t, usd)
	return bidState{
		pool: pool, owner: data.AuctionSlot.Account, total: data.LPTokenBalance,
		bidderLP: bidHolding(t, env, bidder.ID, pool.ID, data.LPTokenBalance.Currency, true),
		ownerLP:  bidHolding(t, env, data.AuctionSlot.Account, pool.ID, data.LPTokenBalance.Currency, false),
		poolUSD:  *usd, poolXRP: env.Balance(pool), bidderXRP: env.Balance(bidder),
		expiration: protocol.ToRippleTime(env.Ledger().ParentCloseTime()) + 86400,
	}
}

func bidHolding(t *testing.T, env *amm.AMMTestEnv, holder, pool [20]byte, currency string, required bool) tx.Amount {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Line(holder, pool, currency))
	require.NoError(t, err)
	if len(data) == 0 {
		require.False(t, required, "expected bidder LP trust line")
		return tx.NewIssuedAmount(0, 0, currency, state.AccountOneAddress)
	}
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	holding := bidLPHolding(line, holder, pool)
	require.Equal(t, currency, holding.Currency)
	require.Equal(t, state.AccountOneAddress, holding.Issuer)
	return holding
}

func assertBidState(t *testing.T, env *amm.AMMTestEnv, bidder *jtx.Account, before bidState, price, refund string, discountedFee uint16, auth [][20]byte) {
	t.Helper()
	amount := func(value string, prototype tx.Amount) tx.Amount {
		t.Helper()
		result, err := state.NewIssuedAmountFromDecimalString(value, prototype.Currency, prototype.Issuer)
		require.NoError(t, err)
		return result
	}
	subtract := func(a, b tx.Amount) tx.Amount {
		t.Helper()
		result, err := a.Sub(b)
		require.NoError(t, err)
		return result
	}
	add := func(a, b tx.Amount) tx.Amount {
		t.Helper()
		result, err := a.Add(b)
		require.NoError(t, err)
		return result
	}
	data := env.ReadAMMData(amm.XRP(), env.USD)
	require.NotNil(t, data)
	require.NotNil(t, data.AuctionSlot)
	require.Equal(t, bidder.ID, data.AuctionSlot.Account)
	require.Equal(t, amount(price, before.total), data.AuctionSlot.Price)
	require.Equal(t, before.expiration, data.AuctionSlot.Expiration)
	require.Equal(t, discountedFee, data.AuctionSlot.DiscountedFee)
	require.Equal(t, len(auth) != 0, data.AuctionSlot.AuthAccountsPresent)
	require.Len(t, data.AuctionSlot.AuthAccounts, len(auth))
	for i, account := range auth {
		require.Equal(t, account, data.AuctionSlot.AuthAccounts[i])
	}
	burn := subtract(amount(price, before.total), amount(refund, before.total))
	require.Equal(t, subtract(before.total, burn), data.LPTokenBalance, "LP supply burn")
	wantBidder := subtract(before.bidderLP, amount(price, before.bidderLP))
	if before.owner == bidder.ID {
		wantBidder = add(wantBidder, amount(refund, before.bidderLP))
	} else {
		wantOwner := add(before.ownerLP, amount(refund, before.ownerLP))
		require.Equal(t, wantOwner, bidHolding(t, env, before.owner, before.pool.ID, before.total.Currency, !wantOwner.IsZero()), "old owner refund")
	}
	require.Equal(t, wantBidder, bidHolding(t, env, bidder.ID, before.pool.ID, before.total.Currency, !wantBidder.IsZero()), "bidder LP debit")
	require.Equal(t, before.bidderXRP-env.BaseFee(), env.Balance(bidder), "only the transaction fee is charged in XRP")
	require.Equal(t, before.poolXRP, env.Balance(before.pool))
	usd := env.IOUBalance(before.pool, env.GW, "USD")
	require.NotNil(t, usd)
	require.Equal(t, before.poolUSD, *usd)
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

func TestBidAuthAccountsPrecedence(t *testing.T) {
	t.Run("DuplicateAuthAccountsBeatsNoAMM", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()
		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			AuthAccounts(env.Bob.Address, env.Bob.Address). // duplicate
			Build()
		result := env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TemMALFORMED.String())
	})

	t.Run("SelfAuthAccountBeatsNoAMM", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()
		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			AuthAccounts(env.Carol.Address). // self-authorization
			Build()
		result := env.Submit(bidTx)

		jtx.RequireTxFail(t, result, ter.TemMALFORMED.String())
	})
}
