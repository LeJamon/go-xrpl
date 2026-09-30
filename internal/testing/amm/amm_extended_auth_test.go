// Behavioral vectors from rippled's AMMExtended_test.cpp.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestAMMExtended_DepositAuth(t *testing.T) {
	t.Run("DepositAuth_SelfPayment", func(t *testing.T) {
		// A user with DepositAuth can pay themselves through AMM.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 10000)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Bob sets DepositAuth
		env.EnableDepositAuth(env.Bob)
		env.Close()

		// Bob pays himself USD through the XRP→AMM path (self-payment should work).
		payTx := payment.PayIssued(env.Bob, env.Bob, amm.IOUAmount(env.GW, "USD", 10)).
			SendMax(amm.XRPAmount(20)).
			Paths([][]paymenttx.PathStep{{{Currency: "USD", Issuer: env.GW.Address}}}).
			Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("DepositAuth_BlocksIncoming", func(t *testing.T) {
		// Direct IOU payment to a DepositAuth account should fail.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()

		// Bob sets DepositAuth
		env.EnableDepositAuth(env.Bob)
		env.Close()

		// Alice tries to send USD to DepositAuth Bob — should fail
		payTx := payment.PayIssued(env.Alice, env.Bob, amm.IOUAmount(env.GW, "USD", 50)).Build()
		result := env.Submit(payTx)
		amm.ExpectTER(t, result, "tecNO_PERMISSION")
	})

	t.Run("DepositAuth_ClearedAllows", func(t *testing.T) {
		// After clearing DepositAuth, payments work again.
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(30000)))
		env.Trust(env.Bob, env.GW, "USD", 100000)
		env.Close()

		env.EnableDepositAuth(env.Bob)
		env.Close()
		env.DisableDepositAuth(env.Bob)
		env.Close()

		// Alice can now send USD to Bob
		payTx := payment.PayIssued(env.Alice, env.Bob, amm.IOUAmount(env.GW, "USD", 50)).Build()
		result := env.Submit(payTx)
		jtx.RequireTxSuccess(t, result)
	})
}

func TestAMMExtended_Multisign(t *testing.T) {
	t.Run("MultisignedAMMCreate", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)

		// Set up signers for Alice
		signer1 := jtx.NewAccount("signer1")
		signer2 := jtx.NewAccount("signer2")
		env.TestEnv.Fund(signer1, signer2)
		env.Close()

		env.SetSignerList(env.Alice, 2, []jtx.TestSigner{
			{Account: signer1, Weight: 1},
			{Account: signer2, Weight: 1},
		})
		env.Close()

		// Create AMM using multisign
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.SubmitMultiSigned(createTx, []*jtx.Account{signer1, signer2})
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	t.Run("MultisignedAMMDeposit", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create AMM first
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Set up signers for Carol
		signer := jtx.NewAccount("carolsigner")
		env.TestEnv.Fund(signer)
		env.Close()
		env.SetSignerList(env.Carol, 1, []jtx.TestSigner{
			{Account: signer, Weight: 1},
		})
		env.Close()

		// Carol deposits via multisign
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(1000)).
			Amount2(amm.IOUAmount(env.GW, "USD", 1000)).
			TwoAsset().
			Build()
		result := env.SubmitMultiSigned(depositTx, []*jtx.Account{signer})
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("MultisignedAMMWithdraw", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Set up signers for Alice
		signer := jtx.NewAccount("alicesigner")
		env.TestEnv.Fund(signer)
		env.Close()
		env.SetSignerList(env.Alice, 1, []jtx.TestSigner{
			{Account: signer, Weight: 1},
		})
		env.Close()

		// Alice withdraws via multisign
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			Amount(amm.XRPAmount(100)).
			SingleAsset().
			Build()
		result := env.SubmitMultiSigned(withdrawTx, []*jtx.Account{signer})
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("MultisignedAMMVote", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Create AMM
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		// Set up signers for Alice
		signer := jtx.NewAccount("votersigner")
		env.TestEnv.Fund(signer)
		env.Close()
		env.SetSignerList(env.Alice, 1, []jtx.TestSigner{
			{Account: signer, Weight: 1},
		})
		env.Close()

		// Alice votes via multisign
		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 500).Build()
		result := env.SubmitMultiSigned(voteTx, []*jtx.Account{signer})
		jtx.RequireTxSuccess(t, result)
	})
}

func TestAMMExtended_MissingAuth(t *testing.T) {
	// Alice tries to create AMM without trust line (no funds) -> tecUNFUNDED_AMM
	t.Run("NoTrustLine_Unfunded", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(400000)))
		env.Close()

		// Alice has no USD trust line, so no funds
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 1000), amm.XRPAmount(1000)).Build()
		result := env.Submit(createTx)
		amm.ExpectTER(t, result, ter.TecUNFUNDED_AMM.String())
	})

	// GW sets RequireAuth, authorizes bob but not alice
	t.Run("NoAuth_NoLine", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(400000)))
		env.Close()

		// GW sets RequireAuth
		env.TestEnv.EnableRequireAuth(env.GW)
		env.Close()

		// GW authorizes bob
		env.TestEnv.AuthorizeTrustLine(env.GW, env.Bob, "USD")
		env.Close()
		env.Trust(env.Bob, env.GW, "USD", 50)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 50)
		env.Close()

		// Alice has no trust line at all -> tecNO_LINE
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 1000), amm.XRPAmount(1000)).Build()
		result := env.Submit(createTx)
		amm.ExpectTER(t, result, "tecNO_LINE")
	})

	// GW has trust line for alice but NOT authorized -> tecNO_AUTH
	t.Run("TrustLine_NotAuthorized", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(400000)))
		env.Close()

		// GW sets RequireAuth
		env.TestEnv.EnableRequireAuth(env.GW)
		env.Close()

		// GW creates trust line for alice without auth
		// (in rippled: trust(gw, alice["USD"](2000)) without tfSetfAuth)
		env.Trust(env.Alice, env.GW, "USD", 2000)
		env.Close()

		// Alice tries to create AMM -> tecNO_AUTH (trust line exists but not authorized)
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 1000), amm.XRPAmount(1000)).Build()
		result := env.Submit(createTx)
		amm.ExpectTER(t, result, ter.TecNO_AUTH.String())
	})

	// Finally authorize alice -> AMM creation succeeds
	t.Run("Authorized_Succeeds", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(400000)))
		env.Close()

		// GW sets RequireAuth
		env.TestEnv.EnableRequireAuth(env.GW)
		env.Close()

		// GW authorizes alice
		env.TestEnv.AuthorizeTrustLine(env.GW, env.Alice, "USD")
		env.Close()
		env.Trust(env.Alice, env.GW, "USD", 2000)
		env.Close()
		env.PayIOU(env.GW, env.Alice, "USD", 1000)
		env.Close()

		// Alice creates AMM -> should succeed
		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 1000), amm.XRPAmount(1050)).Build()
		result := env.Submit(createTx)
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Offer crossing AMM — after RequireAuth setup, AMM account needs auth too
	// Reference: rippled AMMExtended_test.cpp testMissingAuth lines 1427-1443
	t.Run("OfferCrossingAMM", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(400000)))
		env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(400000)))
		env.Close()

		// GW sets RequireAuth
		env.TestEnv.EnableRequireAuth(env.GW)
		env.Close()

		// Authorize alice
		env.TestEnv.AuthorizeTrustLine(env.GW, env.Alice, "USD")
		env.Close()
		env.Trust(env.Alice, env.GW, "USD", 2000)
		env.Close()
		env.PayIOU(env.GW, env.Alice, "USD", 1000)
		env.Close()

		// Authorize bob
		env.TestEnv.AuthorizeTrustLine(env.GW, env.Bob, "USD")
		env.Close()
		env.Trust(env.Bob, env.GW, "USD", 50)
		env.Close()
		env.PayIOU(env.GW, env.Bob, "USD", 50)
		env.Close()

		// Alice creates AMM: USD(1000)/XRP(1050)
		createTx := amm.AMMCreate(env.Alice,
			amm.IOUAmount(env.GW, "USD", 1000),
			amm.XRPAmount(1050)).Build()
		jtx.RequireTxSuccess(t, env.Submit(createTx))
		env.Close()

		ammAcc := env.ReadAMMAccount(env.USD, amm.XRP())
		if ammAcc == nil {
			t.Fatal("AMM not found")
		}

		// Authorize AMM account's trust line with gw
		env.TestEnv.AuthorizeTrustLine(env.GW, ammAcc, "USD")
		env.Close()

		// Bob creates offer: buy XRP(50), sell USD(50) — should cross with AMM
		offerTx := offerbuild.OfferCreate(env.Bob,
			amm.XRPAmount(50),
			amm.IOUAmount(env.GW, "USD", 50)).Build()
		jtx.RequireTxSuccess(t, env.Submit(offerTx))
		env.Close()

		// AMM balances should be USD(1050)/XRP(1000)
		ammAddr := env.ReadAMMAccount(env.USD, amm.XRP())
		if ammAddr == nil {
			t.Fatal("AMM not found")
		}
		usdBal := env.AMMPoolIOU(ammAddr, env.GW, "USD")
		xrpBal := env.AMMPoolXRP(ammAddr)
		require.Equal(t, float64(1050), usdBal)
		require.Equal(t, uint64(1_000_000_000), xrpBal)

		// Bob should have no offers left
		bobOffers := env.AccountOffers(env.Bob)
		require.Empty(t, bobOffers)

		// Bob should have USD(0)
		bobUSD := env.TestEnv.BalanceIOU(env.Bob, "USD", env.GW)
		require.Equal(t, float64(0), bobUSD)
	})
}

func TestAMMExtended_Multisign_WithDisabledMaster(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.FundWithIOUs(20000, 0) // Match rippled: fund with 20000
	env.Close()

	// Create accounts matching rippled's test
	bogie := jtx.NewAccount("bogie")
	becky := jtx.NewAccount("becky")
	alie := jtx.NewAccount("alie") // Regular key for alice

	env.TestEnv.Fund(bogie, becky)
	env.Close()

	// alice sets regular key and disables master
	env.TestEnv.SetRegularKey(env.Alice, alie)
	env.TestEnv.DisableMasterKey(env.Alice)
	env.Close()

	// Attach signers to alice (quorum=2, becky weight=1, bogie weight=1)
	signerList := jtx.NewSignerListSetTx(env.Alice, 2, []jtx.TestSigner{
		{Account: becky, Weight: 1},
		{Account: bogie, Weight: 1},
	})
	jtx.RequireTxSuccess(t, env.SubmitSignedWith(signerList, alie))
	env.Close()

	// Multisigned AMMCreate
	t.Run("Create", func(t *testing.T) {
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.SubmitMultiSigned(createTx, []*jtx.Account{becky, bogie})
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Multisigned AMMDeposit (proportional, 1_000_000 LP tokens)
	t.Run("Deposit", func(t *testing.T) {
		depositTx := amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.SubmitMultiSigned(depositTx, []*jtx.Account{becky, bogie})
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Multisigned AMMWithdraw
	t.Run("Withdraw", func(t *testing.T) {
		withdrawTx := amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).
			LPTokenIn(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		result := env.SubmitMultiSigned(withdrawTx, []*jtx.Account{becky, bogie})
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Multisigned AMMVote
	t.Run("Vote", func(t *testing.T) {
		voteTx := amm.AMMVote(env.Alice, amm.XRP(), env.USD, 1000).Build()
		result := env.SubmitMultiSigned(voteTx, []*jtx.Account{becky, bogie})
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Multisigned AMMBid
	t.Run("Bid", func(t *testing.T) {
		bidTx := amm.AMMBid(env.Alice, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			Build()
		result := env.SubmitMultiSigned(bidTx, []*jtx.Account{becky, bogie})
		jtx.RequireTxSuccess(t, result)
	})
}
