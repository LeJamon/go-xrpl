package adapter

import (
	"context"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// GetAccountInfo retrieves account information from the ledger
func (a *LedgerServiceAdapter) GetAccountInfo(ctx context.Context, account string, ledgerIndex string) (*types.AccountInfo, error) {
	result, err := a.svc.GetAccountInfo(ctx, account, ledgerIndex)
	if err != nil {
		return nil, err
	}

	var prevTxnID string
	zeroHash := [32]byte{}
	if result.PreviousTxnID != zeroHash {
		prevTxnID = formatLedgerHash(result.PreviousTxnID)
	}

	return &types.AccountInfo{
		Account:           result.Account,
		Balance:           strconv.FormatUint(result.Balance, 10),
		Flags:             result.Flags,
		OwnerCount:        result.OwnerCount,
		Sequence:          result.Sequence,
		RegularKey:        result.RegularKey,
		Domain:            result.Domain,
		EmailHash:         result.EmailHash,
		TransferRate:      result.TransferRate,
		TickSize:          result.TickSize,
		PreviousTxnID:     prevTxnID,
		PreviousTxnLgrSeq: result.PreviousTxnLgrSeq,
		LedgerIndex:       result.LedgerIndex,
		LedgerHash:        formatLedgerHash(result.LedgerHash),
		Validated:         result.Validated,
		RawData:           result.RawData,
		Index:             formatLedgerHash(result.Index),
	}, nil
}

// GetAccountLines retrieves trust lines for an account
func (a *LedgerServiceAdapter) GetAccountLines(ctx context.Context, account string, ledgerIndex string, peer string, limit uint32, marker string) (*types.AccountLinesResult, error) {
	result, err := a.svc.GetAccountLines(ctx, account, ledgerIndex, peer, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service types to RPC types
	lines := make([]types.TrustLine, len(result.Lines))
	for i, line := range result.Lines {
		lines[i] = types.TrustLine{
			Account:        line.Account,
			Balance:        line.Balance,
			Currency:       line.Currency,
			Limit:          line.Limit,
			LimitPeer:      line.LimitPeer,
			QualityIn:      line.QualityIn,
			QualityOut:     line.QualityOut,
			NoRipple:       line.NoRipple,
			NoRipplePeer:   line.NoRipplePeer,
			Authorized:     line.Authorized,
			PeerAuthorized: line.PeerAuthorized,
			Freeze:         line.Freeze,
			FreezePeer:     line.FreezePeer,
			DeepFreeze:     line.DeepFreeze,
			DeepFreezePeer: line.DeepFreezePeer,
			HasReserve:     line.HasReserve,
		}
	}

	return &types.AccountLinesResult{
		Account:     result.Account,
		Lines:       lines,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Validated:   result.Validated,
		Marker:      result.Marker,
	}, nil
}

// GetAccountOffers retrieves offers for an account
func (a *LedgerServiceAdapter) GetAccountOffers(ctx context.Context, account string, ledgerIndex string, limit uint32, marker string) (*types.AccountOffersResult, error) {
	result, err := a.svc.GetAccountOffers(ctx, account, ledgerIndex, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service types to RPC types
	offers := make([]types.AccountOffer, len(result.Offers))
	for i, offer := range result.Offers {
		offers[i] = types.AccountOffer{
			Flags:      offer.Flags,
			Seq:        offer.Seq,
			TakerGets:  offer.TakerGets,
			TakerPays:  offer.TakerPays,
			Quality:    offer.Quality,
			Expiration: offer.Expiration,
		}
	}

	return &types.AccountOffersResult{
		Account:     result.Account,
		Offers:      offers,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Validated:   result.Validated,
		Marker:      result.Marker,
	}, nil
}

func (a *LedgerServiceAdapter) GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	// Convert RPC marker to service marker
	var svcMarker *relationaldb.AccountTxMarker
	if marker != nil {
		svcMarker = &relationaldb.AccountTxMarker{
			LedgerSeq: relationaldb.LedgerIndex(marker.LedgerSeq),
			TxnSeq:    marker.TxnSeq,
		}
	}

	result, err := a.svc.GetAccountTransactions(ctx, account, ledgerMin, ledgerMax, limit, svcMarker, forward)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	txs := make([]types.AccountTransaction, len(result.Transactions))
	for i, tx := range result.Transactions {
		txs[i] = types.AccountTransaction{
			Hash:        tx.Hash,
			LedgerIndex: tx.LedgerIndex,
			TxnSeq:      tx.TxnSeq,
			TxBlob:      tx.TxBlob,
			Meta:        tx.Meta,
		}
	}

	var rpcMarker *types.AccountTxMarker
	if result.Marker != nil {
		rpcMarker = &types.AccountTxMarker{
			LedgerSeq: uint32(result.Marker.LedgerSeq),
			TxnSeq:    result.Marker.TxnSeq,
		}
	}

	return &types.AccountTxResult{
		Account:      result.Account,
		LedgerMin:    result.LedgerMin,
		LedgerMax:    result.LedgerMax,
		Limit:        result.Limit,
		Marker:       rpcMarker,
		Transactions: txs,
		Validated:    result.Validated,
	}, nil
}

// GetAccountObjects retrieves all objects owned by an account
func (a *LedgerServiceAdapter) GetAccountObjects(ctx context.Context, account string, ledgerIndex string, objType string, limit uint32, marker string) (*types.AccountObjectsResult, error) {
	result, err := a.svc.GetAccountObjects(ctx, account, ledgerIndex, objType, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	objects := make([]types.AccountObjectItem, len(result.AccountObjects))
	for i, obj := range result.AccountObjects {
		objects[i] = types.AccountObjectItem{
			Index:           obj.Index,
			LedgerEntryType: obj.LedgerEntryType,
			Data:            obj.Data,
		}
	}

	return &types.AccountObjectsResult{
		Account:        result.Account,
		AccountObjects: objects,
		LedgerIndex:    result.LedgerIndex,
		LedgerHash:     result.LedgerHash,
		Validated:      result.Validated,
		Marker:         result.Marker,
	}, nil
}

// GetOwnerInfo walks the account's owner directory for the owner_info RPC,
// implementing types.OwnerDirectoryReader.
func (a *LedgerServiceAdapter) GetOwnerInfo(ctx context.Context, account string, ledgerIndex string) (*types.OwnerInfoResult, error) {
	result, err := a.svc.GetOwnerInfo(ctx, account, ledgerIndex)
	if err != nil {
		return nil, err
	}

	return &types.OwnerInfoResult{
		Offers:      toRPCAccountObjectItems(result.Offers),
		RippleLines: toRPCAccountObjectItems(result.RippleLines),
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Validated:   result.Validated,
	}, nil
}

func toRPCAccountObjectItems(items []service.AccountObjectItem) []types.AccountObjectItem {
	out := make([]types.AccountObjectItem, len(items))
	for i, obj := range items {
		out[i] = types.AccountObjectItem{
			Index:           obj.Index,
			LedgerEntryType: obj.LedgerEntryType,
			Data:            obj.Data,
		}
	}
	return out
}

func (a *LedgerServiceAdapter) GetAccountChannels(ctx context.Context, account string, destinationAccount string, ledgerIndex string, limit uint32, marker string) (*types.AccountChannelsResult, error) {
	result, err := a.svc.GetAccountChannels(ctx, account, destinationAccount, ledgerIndex, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	channels := make([]types.AccountChannel, len(result.Channels))
	for i, ch := range result.Channels {
		channels[i] = types.AccountChannel{
			ChannelID:          ch.ChannelID,
			Account:            ch.Account,
			DestinationAccount: ch.DestinationAccount,
			Amount:             ch.Amount,
			Balance:            ch.Balance,
			SettleDelay:        ch.SettleDelay,
			PublicKey:          ch.PublicKey,
			PublicKeyHex:       ch.PublicKeyHex,
			Expiration:         ch.Expiration,
			CancelAfter:        ch.CancelAfter,
			SourceTag:          ch.SourceTag,
			DestinationTag:     ch.DestinationTag,
			HasSourceTag:       ch.HasSourceTag,
			HasDestTag:         ch.HasDestTag,
		}
	}

	return &types.AccountChannelsResult{
		Account:     result.Account,
		Channels:    channels,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Validated:   result.Validated,
		Marker:      result.Marker,
	}, nil
}

// GetAccountCurrencies retrieves currencies an account can send and receive
func (a *LedgerServiceAdapter) GetAccountCurrencies(ctx context.Context, account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	result, err := a.svc.GetAccountCurrencies(ctx, account, ledgerIndex)
	if err != nil {
		return nil, err
	}

	return &types.AccountCurrenciesResult{
		ReceiveCurrencies: result.ReceiveCurrencies,
		SendCurrencies:    result.SendCurrencies,
		LedgerIndex:       result.LedgerIndex,
		LedgerHash:        result.LedgerHash,
		Validated:         result.Validated,
	}, nil
}

func (a *LedgerServiceAdapter) GetAccountNFTs(ctx context.Context, account string, ledgerIndex string, limit uint32, marker string) (*types.AccountNFTsResult, error) {
	result, err := a.svc.GetAccountNFTs(ctx, account, ledgerIndex, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	nfts := make([]types.NFTInfo, len(result.AccountNFTs))
	for i, nft := range result.AccountNFTs {
		nfts[i] = types.NFTInfo{
			Flags:        nft.Flags,
			Issuer:       nft.Issuer,
			NFTokenID:    nft.NFTokenID,
			NFTokenTaxon: nft.NFTokenTaxon,
			URI:          nft.URI,
			NFTSerial:    nft.NFTSerial,
			TransferFee:  nft.TransferFee,
		}
	}

	return &types.AccountNFTsResult{
		Account:     result.Account,
		AccountNFTs: nfts,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Validated:   result.Validated,
		Marker:      result.Marker,
	}, nil
}
