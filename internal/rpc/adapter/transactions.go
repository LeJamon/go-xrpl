package adapter

import (
	"context"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// GetTransaction retrieves a transaction by its hash
func (a *LedgerServiceAdapter) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	result, err := a.svc.GetTransaction(txHash)
	if err != nil {
		return nil, err
	}
	return rpcTransactionInfo(result), nil
}

// GetTransactionWithRange performs the optional transaction-table lookup used
// by the tx RPC without adding that method to the broad ledger service contract.
func (a *LedgerServiceAdapter) GetTransactionWithRange(ctx context.Context, txHash [32]byte, minLedger, maxLedger uint32) (*types.TransactionInfo, types.TxSearchResult, error) {
	result, searched, err := a.svc.GetTransactionWithRange(ctx, txHash, minLedger, maxLedger)
	if result == nil {
		return nil, rpcTxSearchResult(searched), err
	}
	return rpcTransactionInfo(result), rpcTxSearchResult(searched), err
}

func rpcTransactionInfo(result *service.TransactionResult) *types.TransactionInfo {
	ledgerHash := ""
	if result.LedgerHash != ([32]byte{}) {
		ledgerHash = formatLedgerHash(result.LedgerHash)
	}
	return &types.TransactionInfo{
		TxData:      result.TxData,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  ledgerHash,
		Validated:   result.Validated,
		TxIndex:     result.TxIndex,
		CloseTime:   result.CloseTime,
	}
}

// GetTransactionHistory retrieves recent transactions
func (a *LedgerServiceAdapter) GetTransactionHistory(ctx context.Context, startIndex uint32) (*types.TxHistoryResult, error) {
	result, err := a.svc.GetTransactionHistory(ctx, startIndex)
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
