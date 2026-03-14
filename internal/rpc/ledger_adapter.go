package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// LedgerServiceAdapter adapts the ledger service to the RPC LedgerService interface
type LedgerServiceAdapter struct {
	svc *service.Service
}

// NewLedgerServiceAdapter creates a new adapter
func NewLedgerServiceAdapter(svc *service.Service) *LedgerServiceAdapter {
	return &LedgerServiceAdapter{svc: svc}
}

// GetCurrentLedgerIndex returns the current open ledger index
func (a *LedgerServiceAdapter) GetCurrentLedgerIndex() uint32 {
	return a.svc.GetCurrentLedgerIndex()
}

// GetClosedLedgerIndex returns the last closed ledger index
func (a *LedgerServiceAdapter) GetClosedLedgerIndex() uint32 {
	return a.svc.GetClosedLedgerIndex()
}

// GetValidatedLedgerIndex returns the highest validated ledger index
func (a *LedgerServiceAdapter) GetValidatedLedgerIndex() uint32 {
	return a.svc.GetValidatedLedgerIndex()
}

// AcceptLedger closes the current open ledger (standalone mode only)
func (a *LedgerServiceAdapter) AcceptLedger() (uint32, error) {
	return a.svc.AcceptLedger()
}

// IsStandalone returns true if running in standalone mode
func (a *LedgerServiceAdapter) IsStandalone() bool {
	return a.svc.IsStandalone()
}

// GetServerInfo returns server status information
func (a *LedgerServiceAdapter) GetServerInfo() types.LedgerServerInfo {
	info := a.svc.GetServerInfo()
	return types.LedgerServerInfo{
		Standalone:          info.Standalone,
		OpenLedgerSeq:       info.OpenLedgerSeq,
		ClosedLedgerSeq:     info.ClosedLedgerSeq,
		ClosedLedgerHash:    info.ClosedLedgerHash,
		ValidatedLedgerSeq:  info.ValidatedLedgerSeq,
		ValidatedLedgerHash: info.ValidatedLedgerHash,
		CompleteLedgers:     info.CompleteLedgers,
	}
}

// GetLedgerBySequence returns a ledger by its sequence number
func (a *LedgerServiceAdapter) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	l, err := a.svc.GetLedgerBySequence(seq)
	if err != nil {
		return nil, err
	}
	return &ledgerReaderAdapter{l: l}, nil
}

// GetLedgerByHash returns a ledger by its hash
func (a *LedgerServiceAdapter) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	l, err := a.svc.GetLedgerByHash(hash)
	if err != nil {
		return nil, err
	}
	return &ledgerReaderAdapter{l: l}, nil
}

// GetGenesisAccount returns the genesis account address
func (a *LedgerServiceAdapter) GetGenesisAccount() (string, error) {
	return a.svc.GetGenesisAccount()
}

// ledgerReaderAdapter adapts ledger.Ledger to types.LedgerReader interface
type ledgerReaderAdapter struct {
	l *ledger.Ledger
}

func (a *ledgerReaderAdapter) Sequence() uint32 {
	return a.l.Sequence()
}

func (a *ledgerReaderAdapter) Hash() [32]byte {
	return a.l.Hash()
}

func (a *ledgerReaderAdapter) ParentHash() [32]byte {
	return a.l.ParentHash()
}

func (a *ledgerReaderAdapter) IsClosed() bool {
	return a.l.IsClosed()
}

func (a *ledgerReaderAdapter) IsValidated() bool {
	return a.l.IsValidated()
}

func (a *ledgerReaderAdapter) TotalDrops() uint64 {
	return a.l.TotalDrops()
}

// rippleEpoch is 2000-01-01T00:00:00Z in Unix seconds
const rippleEpoch int64 = 946684800

func (a *ledgerReaderAdapter) CloseTime() int64 {
	t := a.l.CloseTime()
	if t.IsZero() {
		return 0
	}
	return t.Unix() - rippleEpoch
}

func (a *ledgerReaderAdapter) CloseTimeResolution() uint32 {
	return a.l.Header().CloseTimeResolution
}

func (a *ledgerReaderAdapter) CloseFlags() uint8 {
	return a.l.Header().CloseFlags
}

func (a *ledgerReaderAdapter) ParentCloseTime() int64 {
	t := a.l.ParentCloseTime()
	if t.IsZero() {
		return 0
	}
	return t.Unix() - rippleEpoch
}

func (a *ledgerReaderAdapter) TxMapHash() [32]byte {
	h, _ := a.l.TxMapHash()
	return h
}

func (a *ledgerReaderAdapter) StateMapHash() [32]byte {
	h, _ := a.l.StateMapHash()
	return h
}

func (a *ledgerReaderAdapter) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	return a.l.ForEachTransaction(fn)
}

// SubmitTransaction submits a transaction to the open ledger.
// The optional txBlobHex is the original signed transaction blob in hex.
// This is used for canonical re-ordering during AcceptLedger to ensure
// the exact same bytes (and thus same tx hash) are used during re-application.
func (a *LedgerServiceAdapter) SubmitTransaction(txJSON []byte, txBlobHex ...string) (*types.SubmitResult, error) {
	// Parse the transaction from JSON
	transaction, err := tx.ParseJSON(txJSON)
	if err != nil {
		return &types.SubmitResult{
			EngineResult:        "temMALFORMED",
			EngineResultCode:    -299,
			EngineResultMessage: "Transaction is malformed: " + err.Error(),
			Applied:             false,
		}, nil
	}

	// Use the original signed blob if provided, otherwise re-encode
	var rawBlob []byte
	if len(txBlobHex) > 0 && txBlobHex[0] != "" {
		rawBlob, _ = hex.DecodeString(txBlobHex[0])
	}
	if rawBlob == nil {
		if txMap, fErr := transaction.Flatten(); fErr == nil {
			if hexStr, eErr := binarycodec.Encode(txMap); eErr == nil {
				rawBlob, _ = hex.DecodeString(hexStr)
			}
		}
	}
	if rawBlob == nil {
		var jsonMap map[string]interface{}
		if jErr := json.Unmarshal(txJSON, &jsonMap); jErr == nil {
			if hexStr, eErr := binarycodec.Encode(jsonMap); eErr == nil {
				rawBlob, _ = hex.DecodeString(hexStr)
			}
		}
	}

	// Submit to the service with the raw blob for canonical ordering
	result, err := a.svc.SubmitTransaction(transaction, rawBlob)
	if err != nil {
		return &types.SubmitResult{
			EngineResult:        "tefINTERNAL",
			EngineResultCode:    -199,
			EngineResultMessage: "Internal error: " + err.Error(),
			Applied:             false,
		}, nil
	}

	return &types.SubmitResult{
		EngineResult:        result.Result.String(),
		EngineResultCode:    int(result.Result),
		EngineResultMessage: result.Message,
		Applied:             result.Applied,
		Broadcast:           result.Applied, // If applied, it would be broadcast to peers
		Queued:              false,          // Not queued when applied directly
		Kept:                result.Applied, // If applied, it is kept
		Fee:                 result.Fee,
		CurrentLedger:       result.CurrentLedger,
		ValidatedLedger:     result.ValidatedLedger,
	}, nil
}

// GetCurrentFees returns the current fee settings
func (a *LedgerServiceAdapter) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return a.svc.GetCurrentFees()
}

// GetAccountInfo retrieves account information from the ledger
func (a *LedgerServiceAdapter) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
	result, err := a.svc.GetAccountInfo(account, ledgerIndex)
	if err != nil {
		return nil, err
	}

	var prevTxnID string
	zeroHash := [32]byte{}
	if result.PreviousTxnID != zeroHash {
		prevTxnID = strings.ToUpper(hex.EncodeToString(result.PreviousTxnID[:]))
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
		LedgerHash:        hex.EncodeToString(result.LedgerHash[:]),
		Validated:         result.Validated,
		RawData:           result.RawData,
		Index:             strings.ToUpper(hex.EncodeToString(result.Index[:])),
	}, nil
}

// GetTransaction retrieves a transaction by its hash
func (a *LedgerServiceAdapter) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	result, err := a.svc.GetTransaction(txHash)
	if err != nil {
		return nil, err
	}

	return &types.TransactionInfo{
		TxData:      result.TxData,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  hex.EncodeToString(result.LedgerHash[:]),
		Validated:   result.Validated,
		TxIndex:     result.TxIndex,
	}, nil
}

// StoreTransaction stores a transaction in the current ledger
func (a *LedgerServiceAdapter) StoreTransaction(txHash [32]byte, txData []byte) error {
	return a.svc.StoreTransaction(txHash, txData)
}

// GetAccountLines retrieves trust lines for an account
func (a *LedgerServiceAdapter) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	result, err := a.svc.GetAccountLines(account, ledgerIndex, peer, limit)
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
func (a *LedgerServiceAdapter) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	result, err := a.svc.GetAccountOffers(account, ledgerIndex, limit)
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

// GetBookOffers retrieves offers from an order book
func (a *LedgerServiceAdapter) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	// Convert RPC types.Amount to tx.Amount
	var txTakerGets, txTakerPays tx.Amount
	if takerGets.Currency == "" || takerGets.Currency == "XRP" {
		txTakerGets = tx.NewXRPAmount(0) // Placeholder - book offers query uses currency/issuer only
	} else {
		txTakerGets = tx.NewIssuedAmountFromFloat64(0, takerGets.Currency, takerGets.Issuer)
	}
	if takerPays.Currency == "" || takerPays.Currency == "XRP" {
		txTakerPays = tx.NewXRPAmount(0)
	} else {
		txTakerPays = tx.NewIssuedAmountFromFloat64(0, takerPays.Currency, takerPays.Issuer)
	}

	result, err := a.svc.GetBookOffers(txTakerGets, txTakerPays, ledgerIndex, limit)
	if err != nil {
		return nil, err
	}

	// Convert service types to RPC types
	offers := make([]types.BookOffer, len(result.Offers))
	for i, offer := range result.Offers {
		offers[i] = types.BookOffer{
			Account:         offer.Account,
			BookDirectory:   offer.BookDirectory,
			BookNode:        offer.BookNode,
			Flags:           offer.Flags,
			LedgerEntryType: offer.LedgerEntryType,
			OwnerNode:       offer.OwnerNode,
			Sequence:        offer.Sequence,
			TakerGets:       offer.TakerGets,
			TakerPays:       offer.TakerPays,
			Index:           offer.Index,
			Quality:         offer.Quality,
			OwnerFunds:      offer.OwnerFunds,
			TakerGetsFunded: offer.TakerGetsFunded,
			TakerPaysFunded: offer.TakerPaysFunded,
		}
	}

	return &types.BookOffersResult{
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Offers:      offers,
		Validated:   result.Validated,
	}, nil
}

// GetAccountTransactions retrieves transaction history for an account
func (a *LedgerServiceAdapter) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	// Convert RPC marker to service marker
	var svcMarker *relationaldb.AccountTxMarker
	if marker != nil {
		svcMarker = &relationaldb.AccountTxMarker{
			LedgerSeq: relationaldb.LedgerIndex(marker.LedgerSeq),
			TxnSeq:    marker.TxnSeq,
		}
	}

	result, err := a.svc.GetAccountTransactions(account, ledgerMin, ledgerMax, limit, svcMarker, forward)
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

// GetTransactionHistory retrieves recent transactions
func (a *LedgerServiceAdapter) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
	result, err := a.svc.GetTransactionHistory(startIndex)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	txs := make([]types.AccountTransaction, len(result.Transactions))
	for i, tx := range result.Transactions {
		txs[i] = types.AccountTransaction{
			Hash:        tx.Hash,
			LedgerIndex: tx.LedgerIndex,
			TxBlob:      tx.TxBlob,
			Meta:        tx.Meta,
		}
	}

	return &types.TxHistoryResult{
		Index:        result.Index,
		Transactions: txs,
	}, nil
}

// GetLedgerRange retrieves ledger hashes for a range of sequences
func (a *LedgerServiceAdapter) GetLedgerRange(minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	result, err := a.svc.GetLedgerRange(minSeq, maxSeq)
	if err != nil {
		return nil, err
	}

	return &types.LedgerRangeResult{
		LedgerFirst: result.LedgerFirst,
		LedgerLast:  result.LedgerLast,
		Hashes:      result.Hashes,
	}, nil
}

// GetLedgerEntry retrieves a specific ledger entry by its index/key
func (a *LedgerServiceAdapter) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	result, err := a.svc.GetLedgerEntry(entryKey, ledgerIndex)
	if err != nil {
		return nil, err
	}

	return &types.LedgerEntryResult{
		Index:       result.Index,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Node:        result.Node,
		NodeBinary:  hex.EncodeToString(result.Node),
		Validated:   result.Validated,
	}, nil
}

// GetLedgerData retrieves all ledger state entries with pagination
func (a *LedgerServiceAdapter) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	result, err := a.svc.GetLedgerData(ledgerIndex, limit, marker)
	if err != nil {
		return nil, err
	}

	// Convert service result to RPC result
	state := make([]types.LedgerDataItem, len(result.State))
	for i, item := range result.State {
		state[i] = types.LedgerDataItem{
			Index: item.Index,
			Data:  item.Data,
		}
	}

	rpcResult := &types.LedgerDataResult{
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		State:       state,
		Marker:      result.Marker,
		Validated:   result.Validated,
	}

	// Convert ledger header info if present
	if result.LedgerHeader != nil {
		rpcResult.LedgerHeader = &types.LedgerHeaderInfo{
			AccountHash:         result.LedgerHeader.AccountHash,
			CloseFlags:          result.LedgerHeader.CloseFlags,
			CloseTime:           result.LedgerHeader.CloseTime,
			CloseTimeHuman:      result.LedgerHeader.CloseTimeHuman,
			CloseTimeISO:        result.LedgerHeader.CloseTimeISO,
			CloseTimeResolution: result.LedgerHeader.CloseTimeResolution,
			Closed:              result.LedgerHeader.Closed,
			LedgerHash:          result.LedgerHeader.LedgerHash,
			LedgerIndex:         result.LedgerHeader.LedgerIndex,
			ParentCloseTime:     result.LedgerHeader.ParentCloseTime,
			ParentHash:          result.LedgerHeader.ParentHash,
			TotalCoins:          result.LedgerHeader.TotalCoins,
			TransactionHash:     result.LedgerHeader.TransactionHash,
		}
	}

	return rpcResult, nil
}

// GetAccountObjects retrieves all objects owned by an account
func (a *LedgerServiceAdapter) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	result, err := a.svc.GetAccountObjects(account, ledgerIndex, objType, limit)
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

// GetAccountChannels retrieves payment channels for an account
func (a *LedgerServiceAdapter) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	result, err := a.svc.GetAccountChannels(account, destinationAccount, ledgerIndex, limit)
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
func (a *LedgerServiceAdapter) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	result, err := a.svc.GetAccountCurrencies(account, ledgerIndex)
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

// GetAccountNFTs retrieves NFTs owned by an account
func (a *LedgerServiceAdapter) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	result, err := a.svc.GetAccountNFTs(account, ledgerIndex, limit)
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

// GetNoRippleCheck checks trust lines for proper NoRipple flag settings
func (a *LedgerServiceAdapter) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	result, err := a.svc.GetNoRippleCheck(account, role, ledgerIndex, limit, transactions)
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

// GetGatewayBalances retrieves obligations and balances for a gateway account
func (a *LedgerServiceAdapter) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	result, err := a.svc.GetGatewayBalances(account, hotWallets, ledgerIndex)
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
func (a *LedgerServiceAdapter) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	result, err := a.svc.GetDepositAuthorized(sourceAccount, destinationAccount, ledgerIndex, credentials)
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
func (a *LedgerServiceAdapter) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	result, err := a.svc.GetNFTBuyOffers(nftID, ledgerIndex, limit, marker)
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

// SimulateTransaction runs a transaction against a snapshot without committing
func (a *LedgerServiceAdapter) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	transaction, err := tx.ParseJSON(txJSON)
	if err != nil {
		return &types.SubmitResult{
			EngineResult:        "temMALFORMED",
			EngineResultCode:    -299,
			EngineResultMessage: "Transaction is malformed: " + err.Error(),
			Applied:             false,
		}, nil
	}

	result, err := a.svc.SimulateTransaction(transaction)
	if err != nil {
		return &types.SubmitResult{
			EngineResult:        "tefINTERNAL",
			EngineResultCode:    -199,
			EngineResultMessage: "Internal error: " + err.Error(),
			Applied:             false,
		}, nil
	}

	return &types.SubmitResult{
		EngineResult:        result.Result.String(),
		EngineResultCode:    int(result.Result),
		EngineResultMessage: result.Message,
		Applied:             result.Applied,
		Fee:                 result.Fee,
		CurrentLedger:       result.CurrentLedger,
		ValidatedLedger:     result.ValidatedLedger,
	}, nil
}

// IsAmendmentBlocked returns true if the server is blocked by unsupported amendments
func (a *LedgerServiceAdapter) IsAmendmentBlocked() bool {
	return false
}

// GetNFTSellOffers retrieves sell offers for an NFToken
func (a *LedgerServiceAdapter) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	result, err := a.svc.GetNFTSellOffers(nftID, ledgerIndex, limit, marker)
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

// GetClosedLedgerView returns a read-only view of the last closed ledger
// for use by pathfinding and other operations that need direct state access.
func (a *LedgerServiceAdapter) GetClosedLedgerView() (types.LedgerStateView, error) {
	l := a.svc.GetClosedLedger()
	if l == nil {
		return nil, fmt.Errorf("no closed ledger available")
	}
	return l, nil
}
