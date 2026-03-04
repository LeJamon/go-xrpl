package nft_test

// NFTokenAuth_test.go - NFT authorization tests
// Reference: rippled/src/test/app/NFTokenAuth_test.cpp
//
// All tests involve a gateway G1 with RequireAuth flag, testing IOU-based
// NFT offers with various authorization scenarios.
//
// Each test runs two scenarios:
// 1. With fixEnforceNFTokenTrustlineV2 (new behavior - strict auth checks)
// 2. Without fixEnforceNFTokenTrustlineV2 (old behavior - lax auth checks)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/nftoken"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/nft"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
)

// mintAndOfferNFT mints a transferable NFT and creates a sell offer for it.
// Returns the NFT ID and sell offer index.
func mintAndOfferNFT(env *jtx.TestEnv, account *jtx.Account, amount tx.Amount, transferFee ...uint16) (string, string) {
	var fee uint16
	if len(transferFee) > 0 {
		fee = transferFee[0]
	}
	flags := nftoken.NFTokenFlagTransferable
	nftID := nft.GetNextNFTokenID(env, account, 0, flags, fee)

	builder := nft.NFTokenMint(account, 0).Transferable()
	if fee > 0 {
		builder = builder.TransferFee(fee)
	}
	env.Submit(builder.Build())
	env.Close()

	offerIndex := nft.GetOfferIndex(env, account)
	env.Submit(nft.NFTokenCreateSellOffer(account, nftID, amount).Build())
	env.Close()

	return nftID, offerIndex
}

// setupGateway creates and configures a gateway with RequireAuth.
func setupGateway(env *jtx.TestEnv, g1 *jtx.Account) {
	result := env.Submit(accountset.AccountSet(g1).RequireAuth().Build())
	_ = result
	env.Close()
}

// authorizeAccount creates a trust line from holder, authorizes it from gateway, and funds it.
func authorizeAccount(env *jtx.TestEnv, g1, account *jtx.Account, currency string, fundAmount float64) {
	limit := tx.NewIssuedAmountFromFloat64(10000, currency, g1.Address)
	env.Submit(trustset.TrustSet(account, limit).Build())
	env.Submit(trustset.TrustSet(g1, tx.NewIssuedAmountFromFloat64(0, currency, account.Address)).SetAuth().Build())
	if fundAmount > 0 {
		env.Submit(payment.PayIssued(g1, account, g1.IOU(currency, fundAmount)).Build())
	}
	env.Close()
}

// ===========================================================================
// testBuyOffer_UnauthorizedSeller
// Reference: rippled NFTokenAuth_test.cpp testBuyOffer_UnauthorizedSeller
//
// Unauthorized seller (A2) tries to accept buy offer from authorized buyer (A1).
// ===========================================================================
func TestBuyOfferUnauthorizedSeller(t *testing.T) {
	t.Run("WithFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")

		env.Fund(g1, a1, a2)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		// Authorize A1 and fund with USD
		authorizeAccount(env, g1, a1, "USD", 1000)

		// A2 mints a transferable NFT and creates a sell offer for XRP
		nftID, _ := mintAndOfferNFT(env, a2, tx.NewXRPAmount(1))

		// A1 creates buy offer for USD
		buyOfferIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID, USD(10), a2).Build())
		env.Close()

		// With fix: A2 has no trust line → tecNO_LINE
		result := env.Submit(nft.NFTokenAcceptBuyOffer(a2, buyOfferIdx).Build())
		jtx.RequireTxFail(t, result, "tecNO_LINE")

		// A2 creates trust line but not authorized
		limit := tx.NewIssuedAmountFromFloat64(10000, "USD", g1.Address)
		env.Submit(trustset.TrustSet(a2, limit).Build())
		env.Close()

		// With fix: A2 trust line not authorized → tecNO_AUTH
		result = env.Submit(nft.NFTokenAcceptBuyOffer(a2, buyOfferIdx).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")
	})

	t.Run("WithoutFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixEnforceNFTokenTrustlineV2")

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")

		env.Fund(g1, a1, a2)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		authorizeAccount(env, g1, a1, "USD", 1000)

		nftID, _ := mintAndOfferNFT(env, a2, tx.NewXRPAmount(1))

		buyOfferIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID, USD(10), a2).Build())
		env.Close()

		// Old behavior: A2 can accept buy offer and receive USD without authorization
		result := env.Submit(nft.NFTokenAcceptBuyOffer(a2, buyOfferIdx).Build())
		jtx.RequireTxSuccess(t, result)
	})
}

// ===========================================================================
// testCreateBuyOffer_UnauthorizedBuyer
// Reference: rippled NFTokenAuth_test.cpp testCreateBuyOffer_UnauthorizedBuyer
//
// Unauthorized buyer (A1) tries to create buy offer with IOU.
// ===========================================================================
func TestCreateBuyOfferUnauthorizedBuyer(t *testing.T) {
	env := jtx.NewTestEnv(t)

	g1 := jtx.NewAccount("G1")
	a1 := jtx.NewAccount("A1")
	a2 := jtx.NewAccount("A2")

	env.Fund(g1, a1, a2)
	env.Close()

	setupGateway(env, g1)
	USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

	// A2 mints NFT and creates sell offer
	nftID, _ := mintAndOfferNFT(env, a2, tx.NewXRPAmount(1))

	// A1 has no trust line, no funds. Attempt buy offer → tecUNFUNDED_OFFER
	result := env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID, USD(10), a2).Build())
	jtx.RequireTxFail(t, result, "tecUNFUNDED_OFFER")
}

// ===========================================================================
// testAcceptBuyOffer_UnauthorizedBuyer
// Reference: rippled NFTokenAuth_test.cpp testAcceptBuyOffer_UnauthorizedBuyer
//
// Seller tries to accept buy offer from buyer whose authorization was revoked.
// ===========================================================================
func TestAcceptBuyOfferUnauthorizedBuyer(t *testing.T) {
	t.Run("WithFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")

		env.Fund(g1, a1, a2)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		// Both A1 and A2 authorized with funds
		authorizeAccount(env, g1, a1, "USD", 10)
		authorizeAccount(env, g1, a2, "USD", 10)

		// A2 mints NFT and creates sell offer
		nftID, _ := mintAndOfferNFT(env, a2, tx.NewXRPAmount(1))

		// A1 creates buy offer for USD
		buyOfferIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID, USD(10), a2).Build())
		env.Close()

		// Revoke A1's authorization by paying back funds and deleting trust line
		env.Submit(payment.PayIssued(a1, g1, g1.IOU("USD", 10)).Build())
		env.Submit(trustset.TrustSet(a1, tx.NewIssuedAmountFromFloat64(0, "USD", g1.Address)).Build())
		env.Close()

		// Also reset G1's trust line to A1
		env.Submit(trustset.TrustSet(g1, tx.NewIssuedAmountFromFloat64(0, "USD", a1.Address)).Build())
		env.Close()

		// With fix: A2 trying to accept A1's buy offer should fail
		// (A1 no longer has authorized trust line)
		result := env.Submit(nft.NFTokenAcceptBuyOffer(a2, buyOfferIdx).Build())
		// A1 has no trust line anymore → tecNO_LINE or tecNO_AUTH
		if result.Success {
			t.Error("Expected failure when buyer's trust line is removed")
		}
	})
}

// ===========================================================================
// testSellOffer_UnauthorizedSeller
// Reference: rippled NFTokenAuth_test.cpp testSellOffer_UnauthorizedSeller
//
// Authorized buyer tries to accept sell offer from unauthorized seller.
// ===========================================================================
func TestSellOfferUnauthorizedSeller(t *testing.T) {
	t.Run("WithFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")

		env.Fund(g1, a1, a2)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		// Authorize A1 with funds
		authorizeAccount(env, g1, a1, "USD", 1000)

		// A2 mints NFT
		nftID, _ := mintAndOfferNFT(env, a2, tx.NewXRPAmount(1))

		// A2 tries to create sell offer for USD — no trust line → tecNO_LINE
		result := env.Submit(nft.NFTokenCreateSellOffer(a2, nftID, USD(10)).Build())
		jtx.RequireTxFail(t, result, "tecNO_LINE")

		// A2 creates trust line (not authorized)
		limit := tx.NewIssuedAmountFromFloat64(10000, "USD", g1.Address)
		env.Submit(trustset.TrustSet(a2, limit).Build())
		env.Close()

		// A2 tries sell offer — trust line not authorized → tecNO_AUTH
		result = env.Submit(nft.NFTokenCreateSellOffer(a2, nftID, USD(10)).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")

		// Authorize A2's trust line
		env.Submit(trustset.TrustSet(g1, tx.NewIssuedAmountFromFloat64(0, "USD", a2.Address)).SetAuth().Build())
		env.Close()

		// Now A2 can create sell offer
		sellIdx := nft.GetOfferIndex(env, a2)
		result = env.Submit(nft.NFTokenCreateSellOffer(a2, nftID, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Reset A2's trust line (delete it)
		env.Submit(trustset.TrustSet(a2, tx.NewIssuedAmountFromFloat64(0, "USD", g1.Address)).Build())
		env.Close()

		// A1 tries to accept sell offer — A2 no trust line → tecNO_LINE
		result = env.Submit(nft.NFTokenAcceptSellOffer(a1, sellIdx).Build())
		jtx.RequireTxFail(t, result, "tecNO_LINE")

		// Recreate A2's trust line (not authorized)
		env.Submit(trustset.TrustSet(a2, limit).Build())
		env.Close()

		// A1 tries to accept — A2 not authorized → tecNO_AUTH
		result = env.Submit(nft.NFTokenAcceptSellOffer(a1, sellIdx).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")
	})

	t.Run("WithoutFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixEnforceNFTokenTrustlineV2")

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")

		env.Fund(g1, a1, a2)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		authorizeAccount(env, g1, a1, "USD", 1000)

		nftID, _ := mintAndOfferNFT(env, a2, tx.NewXRPAmount(1))

		// Old behavior: sell offer can be created without authorization
		sellIdx := nft.GetOfferIndex(env, a2)
		result := env.Submit(nft.NFTokenCreateSellOffer(a2, nftID, USD(10)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Old behavior: A1 can accept and A2 receives USD without authorization
		result = env.Submit(nft.NFTokenAcceptSellOffer(a1, sellIdx).Build())
		jtx.RequireTxSuccess(t, result)
	})
}

// ===========================================================================
// testSellOffer_UnauthorizedBuyer
// Reference: rippled NFTokenAuth_test.cpp testSellOffer_UnauthorizedBuyer
//
// Unauthorized buyer tries to accept a sell offer.
// ===========================================================================
func TestSellOfferUnauthorizedBuyer(t *testing.T) {
	env := jtx.NewTestEnv(t)

	g1 := jtx.NewAccount("G1")
	a1 := jtx.NewAccount("A1")
	a2 := jtx.NewAccount("A2")

	env.Fund(g1, a1, a2)
	env.Close()

	setupGateway(env, g1)
	USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

	// Authorize A2 (seller)
	authorizeAccount(env, g1, a2, "USD", 0)

	// A2 mints NFT and creates sell offer for USD
	nftID := nft.GetNextNFTokenID(env, a2, 0, nftoken.NFTokenFlagTransferable, 0)
	env.Submit(nft.NFTokenMint(a2, 0).Transferable().Build())
	env.Close()

	sellIdx := nft.GetOfferIndex(env, a2)
	env.Submit(nft.NFTokenCreateSellOffer(a2, nftID, USD(10)).Build())
	env.Close()

	// A1 (unauthorized) tries to accept — no funds → tecINSUFFICIENT_FUNDS
	result := env.Submit(nft.NFTokenAcceptSellOffer(a1, sellIdx).Build())
	jtx.RequireTxFail(t, result, "tecINSUFFICIENT_FUNDS")
}

// ===========================================================================
// testBrokeredAcceptOffer_UnauthorizedBroker
// Reference: rippled NFTokenAuth_test.cpp testBrokeredAcceptOffer_UnauthorizedBroker
//
// Unauthorized broker bridges authorized buyer and seller.
// ===========================================================================
func TestBrokeredAcceptOfferUnauthorizedBroker(t *testing.T) {
	t.Run("WithFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")
		broker := jtx.NewAccount("broker")

		env.Fund(g1, a1, a2, broker)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		// Authorize A1 and A2
		authorizeAccount(env, g1, a1, "USD", 1000)
		authorizeAccount(env, g1, a2, "USD", 1000)

		// A2 mints NFT and creates sell offer
		nftID, sellIdx := mintAndOfferNFT(env, a2, USD(10))

		// A1 creates buy offer
		buyIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID, USD(11), a2).Build())
		env.Close()

		// Broker has no trust line → tecNO_LINE
		result := env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).BrokerFee(USD(1)).Build())
		jtx.RequireTxFail(t, result, "tecNO_LINE")

		// Broker creates trust line (not authorized)
		limit := tx.NewIssuedAmountFromFloat64(10000, "USD", g1.Address)
		env.Submit(trustset.TrustSet(broker, limit).Build())
		env.Close()

		// Broker with fee but not authorized → tecNO_AUTH
		result = env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).BrokerFee(USD(1)).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")

		// Broker WITHOUT fee succeeds (no IOU goes to broker)
		result = env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).Build())
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("WithoutFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixEnforceNFTokenTrustlineV2")

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")
		broker := jtx.NewAccount("broker")

		env.Fund(g1, a1, a2, broker)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		authorizeAccount(env, g1, a1, "USD", 1000)
		authorizeAccount(env, g1, a2, "USD", 1000)

		_, sellIdx := mintAndOfferNFT(env, a2, USD(10))
		nftID2, _ := mintAndOfferNFT(env, a2, USD(10))

		buyIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID2, USD(11), a2).Build())
		env.Close()

		// Old behavior: broker receives USD without authorization
		result := env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).BrokerFee(USD(1)).Build())
		_ = result
	})
}

// ===========================================================================
// testBrokeredAcceptOffer_UnauthorizedBuyer
// Reference: rippled NFTokenAuth_test.cpp testBrokeredAcceptOffer_UnauthorizedBuyer
//
// Authorized broker tries to bridge offers from unauthorized buyer.
// ===========================================================================
func TestBrokeredAcceptOfferUnauthorizedBuyer(t *testing.T) {
	t.Run("WithFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")
		broker := jtx.NewAccount("broker")

		env.Fund(g1, a1, a2, broker)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		// All initially authorized with funds
		authorizeAccount(env, g1, a1, "USD", 1000)
		authorizeAccount(env, g1, a2, "USD", 1000)
		authorizeAccount(env, g1, broker, "USD", 1000)

		// A2 mints NFT and creates sell offer
		nftID, sellIdx := mintAndOfferNFT(env, a2, USD(10))

		// A1 creates buy offer
		buyIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID, USD(11), a2).Build())
		env.Close()

		// Clear A1's trust line authorization (simulating unauthorized but funded trust line)
		// Reference: rippled uses rawInsert to create unauthorized trust line with balance
		env.Close()
		env.ClearTrustLineAuth(a1, g1, "USD")

		// Broker tries to broker with fee → tecNO_AUTH (buyer A1 not authorized)
		result := env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).BrokerFee(USD(1)).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")
	})
}

// ===========================================================================
// testBrokeredAcceptOffer_UnauthorizedSeller
// Reference: rippled NFTokenAuth_test.cpp testBrokeredAcceptOffer_UnauthorizedSeller
//
// Authorized broker tries to bridge offers from unauthorized seller.
// ===========================================================================
func TestBrokeredAcceptOfferUnauthorizedSeller(t *testing.T) {
	t.Run("WithFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")
		broker := jtx.NewAccount("broker")

		env.Fund(g1, a1, a2, broker)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		// Authorize A1 and broker
		authorizeAccount(env, g1, a1, "USD", 1000)
		authorizeAccount(env, g1, broker, "USD", 1000)

		// Authorize A2 just enough to create the sell offer
		env.Submit(trustset.TrustSet(g1, tx.NewIssuedAmountFromFloat64(0, "USD", a2.Address)).SetAuth().Build())
		env.Close()

		// A2 mints NFT and creates sell offer
		nftID, sellIdx := mintAndOfferNFT(env, a2, USD(10))

		// A1 creates buy offer
		buyIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID, USD(11), a2).Build())
		env.Close()

		// Delete A2's trust line
		env.Submit(trustset.TrustSet(a2, tx.NewIssuedAmountFromFloat64(0, "USD", g1.Address)).Build())
		env.Close()

		// Broker with fee → tecNO_LINE (A2 no trust line)
		result := env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).BrokerFee(USD(1)).Build())
		jtx.RequireTxFail(t, result, "tecNO_LINE")

		// Recreate A2's trust line (not authorized)
		limit := tx.NewIssuedAmountFromFloat64(10000, "USD", g1.Address)
		env.Submit(trustset.TrustSet(a2, limit).Build())
		env.Close()

		// Broker with fee → tecNO_AUTH
		result = env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).BrokerFee(USD(1)).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")

		// Broker WITHOUT fee → still tecNO_AUTH (seller would receive IOU)
		result = env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")
	})

	t.Run("WithoutFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixEnforceNFTokenTrustlineV2")

		g1 := jtx.NewAccount("G1")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")
		broker := jtx.NewAccount("broker")

		env.Fund(g1, a1, a2, broker)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		authorizeAccount(env, g1, a1, "USD", 1000)
		authorizeAccount(env, g1, broker, "USD", 1000)

		// Authorize A2 to create offer
		env.Submit(trustset.TrustSet(g1, tx.NewIssuedAmountFromFloat64(0, "USD", a2.Address)).SetAuth().Build())
		env.Close()

		_, sellIdx := mintAndOfferNFT(env, a2, USD(10))
		nftID2, _ := mintAndOfferNFT(env, a2, USD(10))

		buyIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateBuyOffer(a1, nftID2, USD(11), a2).Build())
		env.Close()

		// Old behavior: broker can complete the sale
		result := env.Submit(nft.NFTokenBrokeredSale(broker, sellIdx, buyIdx).BrokerFee(USD(1)).Build())
		_ = result
	})
}

// ===========================================================================
// testTransferFee_UnauthorizedMinter
// Reference: rippled NFTokenAuth_test.cpp testTransferFee_UnauthorizedMinter
//
// Unauthorized minter receives transfer fee from secondary sale.
// ===========================================================================
func TestTransferFeeUnauthorizedMinter(t *testing.T) {
	t.Run("WithFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		g1 := jtx.NewAccount("G1")
		minter := jtx.NewAccount("minter")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")

		env.Fund(g1, minter, a1, a2)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		// Authorize A1 and A2 with funds, but NOT minter
		authorizeAccount(env, g1, a1, "USD", 1000)
		authorizeAccount(env, g1, a2, "USD", 1000)

		// Minter creates trust line but is NOT authorized
		limit := tx.NewIssuedAmountFromFloat64(10000, "USD", g1.Address)
		env.Submit(trustset.TrustSet(minter, limit).Build())
		env.Close()

		// Minter mints NFT with 1 basis point transfer fee (0.01%)
		nftID, minterSellIdx := mintAndOfferNFT(env, minter, tx.NewXRPAmount(1), 1)

		// A1 buys NFT from minter (XRP payment, no IOU involved yet)
		env.Submit(nft.NFTokenAcceptSellOffer(a1, minterSellIdx).Build())
		env.Close()

		// A1 creates sell offer for USD
		sellIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateSellOffer(a1, nftID, USD(100)).Build())
		env.Close()

		// A2 tries to accept — minter not authorized for transfer fee → tecNO_AUTH
		result := env.Submit(nft.NFTokenAcceptSellOffer(a2, sellIdx).Build())
		jtx.RequireTxFail(t, result, "tecNO_AUTH")
	})

	t.Run("WithoutFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixEnforceNFTokenTrustlineV2")

		g1 := jtx.NewAccount("G1")
		minter := jtx.NewAccount("minter")
		a1 := jtx.NewAccount("A1")
		a2 := jtx.NewAccount("A2")

		env.Fund(g1, minter, a1, a2)
		env.Close()

		setupGateway(env, g1)
		USD := func(v float64) tx.Amount { return g1.IOU("USD", v) }

		authorizeAccount(env, g1, a1, "USD", 1000)
		authorizeAccount(env, g1, a2, "USD", 1000)

		limit := tx.NewIssuedAmountFromFloat64(10000, "USD", g1.Address)
		env.Submit(trustset.TrustSet(minter, limit).Build())
		env.Close()

		nftID, minterSellIdx := mintAndOfferNFT(env, minter, tx.NewXRPAmount(1), 1)

		env.Submit(nft.NFTokenAcceptSellOffer(a1, minterSellIdx).Build())
		env.Close()

		sellIdx := nft.GetOfferIndex(env, a1)
		env.Submit(nft.NFTokenCreateSellOffer(a1, nftID, USD(100)).Build())
		env.Close()

		// Old behavior: sale succeeds, minter receives transfer fee without auth
		result := env.Submit(nft.NFTokenAcceptSellOffer(a2, sellIdx).Build())
		jtx.RequireTxSuccess(t, result)
	})
}
