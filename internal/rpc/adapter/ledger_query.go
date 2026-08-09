package adapter

import (
	"context"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

func (a *LedgerServiceAdapter) GetLedgerContext(ctx context.Context, sequence uint32) (*types.LedgerContext, error) {
	result, err := a.svc.GetLedgerContext(ctx, sequence)
	if err != nil {
		return nil, err
	}
	return &types.LedgerContext{Hash: result.Hash, CloseTime: result.CloseTime}, nil
}

func rpcTxSearchResult(result relationaldb.TxSearchResult) types.TxSearchResult {
	switch result {
	case relationaldb.TxSearchSome:
		return types.TxSearchSome
	case relationaldb.TxSearchAll:
		return types.TxSearchAll
	default:
		return types.TxSearchUnknown
	}
}

// GetLedgerRange retrieves ledger hashes for a range of sequences
func (a *LedgerServiceAdapter) GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	result, err := a.svc.GetLedgerRange(ctx, minSeq, maxSeq)
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
func (a *LedgerServiceAdapter) GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	result, err := a.svc.GetLedgerEntry(ctx, entryKey, ledgerIndex)
	if err != nil {
		return nil, err
	}

	return &types.LedgerEntryResult{
		Index:       result.Index,
		LedgerIndex: result.LedgerIndex,
		LedgerHash:  result.LedgerHash,
		Node:        result.Node,
		NodeBinary:  formatHash(result.Node),
		Validated:   result.Validated,
	}, nil
}

// GetLedgerData retrieves all ledger state entries with pagination
func (a *LedgerServiceAdapter) GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	result, err := a.svc.GetLedgerData(ctx, ledgerIndex, limit, marker)
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
