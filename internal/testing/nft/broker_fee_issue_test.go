package nft_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
)

func TestNFTokenBrokerFeeIssue(t *testing.T) {
	tests := []struct {
		name         string
		brokerFee    func(gateway, otherGateway *jtx.Account) tx.Amount
		expectedCode string
	}{
		{
			name: "same issue",
			brokerFee: func(gateway, _ *jtx.Account) tx.Amount {
				return gateway.IOU("USD", 100)
			},
		},
		{
			name: "same issue canonical hex currency",
			brokerFee: func(gateway, _ *jtx.Account) tx.Amount {
				return gateway.IOU("0000000000000000000000005553440000000000", 100)
			},
		},
		{
			name: "different issuer",
			brokerFee: func(_, otherGateway *jtx.Account) tx.Amount {
				return otherGateway.IOU("USD", 100)
			},
			expectedCode: "tecNFTOKEN_BUY_SELL_MISMATCH",
		},
		{
			name: "different currency",
			brokerFee: func(gateway, _ *jtx.Account) tx.Amount {
				return gateway.IOU("EUR", 100)
			},
			expectedCode: "tecNFTOKEN_BUY_SELL_MISMATCH",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			seller := jtx.NewAccount("seller")
			buyer := jtx.NewAccount("buyer")
			broker := jtx.NewAccount("broker")
			gateway := jtx.NewAccount("gateway")
			otherGateway := jtx.NewAccount("otherGateway")

			env.Fund(seller, buyer, broker, gateway, otherGateway)
			env.Close()

			usd := func(value float64) tx.Amount { return gateway.IOU("USD", value) }
			eur := func(value float64) tx.Amount { return gateway.IOU("EUR", value) }
			otherUSD := func(value float64) tx.Amount { return otherGateway.IOU("USD", value) }

			for _, line := range []struct {
				account *jtx.Account
				limit   tx.Amount
			}{
				{seller, usd(2_000)},
				{buyer, usd(2_000)},
				{broker, usd(2_000)},
				{buyer, eur(2_000)},
				{broker, eur(2_000)},
				{buyer, otherUSD(2_000)},
				{broker, otherUSD(2_000)},
			} {
				jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(line.account, line.limit).Build()))
			}
			env.Close()

			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gateway, buyer, usd(1_000)).Build()))
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gateway, buyer, eur(1_000)).Build()))
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(otherGateway, buyer, otherUSD(1_000)).Build()))
			env.Close()

			nftID := nft.GetNextNFTokenID(env, seller, 0, nftoken.NFTokenFlagTransferable, 0)
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(seller, 0).Transferable().Build()))
			env.Close()

			sellOffer := nft.GetOfferIndex(env, seller)
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(seller, nftID, usd(300)).Build()))
			env.Close()

			buyOffer := nft.GetOfferIndex(env, buyer)
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(buyer, nftID, usd(500), seller).Build()))
			env.Close()

			result := env.Submit(nft.NFTokenBrokeredSale(broker, sellOffer, buyOffer).
				BrokerFee(test.brokerFee(gateway, otherGateway)).Build())
			env.Close()

			if test.expectedCode == "" {
				jtx.RequireTxSuccess(t, result)
				assertIOUBalance(t, env, buyer, gateway, "USD", 500)
				assertIOUBalance(t, env, seller, gateway, "USD", 400)
				assertIOUBalance(t, env, broker, gateway, "USD", 100)
			} else {
				jtx.RequireTxFail(t, result, test.expectedCode)
				assertIOUBalance(t, env, buyer, gateway, "USD", 1_000)
				assertIOUBalance(t, env, seller, gateway, "USD", 0)
				assertIOUBalance(t, env, broker, gateway, "USD", 0)
			}
			assertIOUBalance(t, env, buyer, gateway, "EUR", 1_000)
			assertIOUBalance(t, env, broker, gateway, "EUR", 0)
			assertIOUBalance(t, env, buyer, otherGateway, "USD", 1_000)
			assertIOUBalance(t, env, broker, otherGateway, "USD", 0)
		})
	}
}

func assertIOUBalance(t *testing.T, env *jtx.TestEnv, account, issuer *jtx.Account, currency string, expected float64) {
	t.Helper()
	if balance := env.BalanceIOU(account, currency, issuer); balance != expected {
		t.Fatalf("%s %s/%s balance: got %v, want %v", account.Name, currency, issuer.Name, balance, expected)
	}
}
