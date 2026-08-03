package adapter

import (
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
)

// GetBookOffers retrieves offers from an order book
func (a *LedgerServiceAdapter) GetBookOffers(ctx context.Context, takerGets, takerPays types.Amount, taker, domain string, ledgerIndex string, limit uint32, marker string, withProofs bool) (*types.BookOffersResult, error) {
	txTakerGets, err := rpcBookAmount(takerGets)
	if err != nil {
		return nil, err
	}
	txTakerPays, err := rpcBookAmount(takerPays)
	if err != nil {
		return nil, err
	}

	result, err := a.svc.GetBookOffers(ctx, txTakerGets, txTakerPays, taker, domain, ledgerIndex, limit, marker, withProofs)
	if err != nil {
		return nil, err
	}

	offers := make([]types.BookOffer, len(result.Offers))
	for i, offer := range result.Offers {
		offers[i] = types.BookOffer{
			Account:           offer.Account,
			BookDirectory:     offer.BookDirectory,
			BookNode:          offer.BookNode,
			Expiration:        offer.Expiration,
			Flags:             offer.Flags,
			LedgerEntryType:   offer.LedgerEntryType,
			OwnerNode:         offer.OwnerNode,
			PreviousTxnID:     offer.PreviousTxnID,
			PreviousTxnLgrSeq: offer.PreviousTxnLgrSeq,
			Sequence:          offer.Sequence,
			TakerGets:         offer.TakerGets,
			TakerPays:         offer.TakerPays,
			DomainID:          offer.DomainID,
			AdditionalBooks:   offer.AdditionalBooks,
			Index:             offer.Index,
			Quality:           offer.Quality,
			OwnerFunds:        offer.OwnerFunds,
			TakerGetsFunded:   offer.TakerGetsFunded,
			TakerPaysFunded:   offer.TakerPaysFunded,
			Proof:             offer.Proof,
		}
	}

	return &types.BookOffersResult{
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Offers:      offers,
		Validated:   result.Validated,
		Marker:      result.Marker,
	}, nil
}

func rpcBookAmount(amount types.Amount) (tx.Amount, error) {
	if amount.IsMPT() {
		id, err := mptutil.DecodeID(amount.MPTIssuanceID)
		if err != nil {
			return tx.Amount{}, fmt.Errorf("invalid mpt issuance id: %w", err)
		}
		issuer := state.EncodeAccountIDSafe(mptutil.Issuer(id))
		return state.NewMPTAmountWithIssuanceID(0, issuer, mptutil.EncodeID(id)), nil
	}
	if amount.Currency == "" || amount.Currency == "XRP" {
		return tx.NewXRPAmount(0), nil
	}
	return tx.NewIssuedAmountFromFloat64(0, amount.Currency, amount.Issuer), nil
}

// UseTxTables implements types.TxTablesProvider.
func (a *LedgerServiceAdapter) UseTxTables() bool {
	return a.svc.UseTxTables()
}

// GetNoRippleCheck checks trust lines for proper NoRipple flag settings
func (a *LedgerServiceAdapter) GetNoRippleCheck(ctx context.Context, account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	result, err := a.svc.GetNoRippleCheck(ctx, account, role, ledgerIndex, limit, transactions)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	var txs []types.SuggestedTransaction
	if len(result.Transactions) > 0 {
		txs = make([]types.SuggestedTransaction, len(result.Transactions))
		for i, tx := range result.Transactions {
			txs[i] = types.SuggestedTransaction{
				TransactionType: tx.TransactionType,
				Account:         tx.Account,
				Fee:             tx.Fee,
				Sequence:        tx.Sequence,
				SetFlag:         tx.SetFlag,
				Flags:           tx.Flags,
				LimitAmount:     tx.LimitAmount,
			}
		}
	}

	return &types.NoRippleCheckResult{
		Problems:     result.Problems,
		Transactions: txs,
		LedgerIndex:  result.LedgerIndex,
		LedgerHash:   result.LedgerHash,
		Validated:    result.Validated,
	}, nil
}

func (a *LedgerServiceAdapter) GetGatewayBalances(ctx context.Context, account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	result, err := a.svc.GetGatewayBalances(ctx, account, hotWallets, ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	var balances map[string][]types.CurrencyBalance
	if result.Balances != nil {
		balances = make(map[string][]types.CurrencyBalance)
		for acct, bals := range result.Balances {
			rpcBals := make([]types.CurrencyBalance, len(bals))
			for i, b := range bals {
				rpcBals[i] = types.CurrencyBalance{
					Currency: b.Currency,
					Value:    b.Value,
				}
			}
			balances[acct] = rpcBals
		}
	}

	var frozenBalances map[string][]types.CurrencyBalance
	if result.FrozenBalances != nil {
		frozenBalances = make(map[string][]types.CurrencyBalance)
		for acct, bals := range result.FrozenBalances {
			rpcBals := make([]types.CurrencyBalance, len(bals))
			for i, b := range bals {
				rpcBals[i] = types.CurrencyBalance{
					Currency: b.Currency,
					Value:    b.Value,
				}
			}
			frozenBalances[acct] = rpcBals
		}
	}

	var assets map[string][]types.CurrencyBalance
	if result.Assets != nil {
		assets = make(map[string][]types.CurrencyBalance)
		for acct, bals := range result.Assets {
			rpcBals := make([]types.CurrencyBalance, len(bals))
			for i, b := range bals {
				rpcBals[i] = types.CurrencyBalance{
					Currency: b.Currency,
					Value:    b.Value,
				}
			}
			assets[acct] = rpcBals
		}
	}

	return &types.GatewayBalancesResult{
		Account:        result.Account,
		Obligations:    result.Obligations,
		Balances:       balances,
		FrozenBalances: frozenBalances,
		Assets:         assets,
		Locked:         result.Locked,
		LedgerIndex:    result.LedgerIndex,
		LedgerHash:     result.LedgerHash,
		Validated:      result.Validated,
	}, nil
}

// GetDepositAuthorized checks if a source account is authorized to deposit to a destination account
func (a *LedgerServiceAdapter) GetDepositAuthorized(ctx context.Context, sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	result, err := a.svc.GetDepositAuthorized(ctx, sourceAccount, destinationAccount, ledgerIndex, credentials)
	if err != nil {
		return nil, err
	}

	return &types.DepositAuthorizedResult{
		SourceAccount:      result.SourceAccount,
		DestinationAccount: result.DestinationAccount,
		DepositAuthorized:  result.DepositAuthorized,
		LedgerIndex:        result.LedgerIndex,
		LedgerHash:         result.LedgerHash,
		Validated:          result.Validated,
	}, nil
}

// GetNFTBuyOffers retrieves buy offers for an NFToken
func (a *LedgerServiceAdapter) GetNFTBuyOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	result, err := a.svc.GetNFTBuyOffers(ctx, nftID, ledgerIndex, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	offers := make([]types.NFTOfferInfo, len(result.Offers))
	for i, offer := range result.Offers {
		offers[i] = types.NFTOfferInfo{
			NFTOfferIndex: offer.NFTOfferIndex,
			Flags:         offer.Flags,
			Owner:         offer.Owner,
			Amount:        offer.Amount,
			Destination:   offer.Destination,
			Expiration:    offer.Expiration,
		}
	}

	return &types.NFTOffersResult{
		NFTID:       result.NFTID,
		Offers:      offers,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Validated:   result.Validated,
		Limit:       result.Limit,
		Marker:      result.Marker,
	}, nil
}

// GetNFTSellOffers retrieves sell offers for an NFToken
func (a *LedgerServiceAdapter) GetNFTSellOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	result, err := a.svc.GetNFTSellOffers(ctx, nftID, ledgerIndex, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	offers := make([]types.NFTOfferInfo, len(result.Offers))
	for i, offer := range result.Offers {
		offers[i] = types.NFTOfferInfo{
			NFTOfferIndex: offer.NFTOfferIndex,
			Flags:         offer.Flags,
			Owner:         offer.Owner,
			Amount:        offer.Amount,
			Destination:   offer.Destination,
			Expiration:    offer.Expiration,
		}
	}

	return &types.NFTOffersResult{
		NFTID:       result.NFTID,
		Offers:      offers,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Validated:   result.Validated,
		Limit:       result.Limit,
		Marker:      result.Marker,
	}, nil
}
