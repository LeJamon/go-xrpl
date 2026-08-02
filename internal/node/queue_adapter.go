package node

import (
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

// queuedTxInfos projects the ledger service's TxQ candidate details into the
// RPC-layer view consumed by account_info and the ledger method's queue_data.
// The transaction body is flattened only for the ledger dump (which echoes it);
// account_info ignores TxJSON.
func queuedTxInfos(details []*txq.CandidateDetails) []types.QueuedTxInfo {
	if len(details) == 0 {
		return nil
	}
	out := make([]types.QueuedTxInfo, 0, len(details))
	for _, d := range details {
		info := types.QueuedTxInfo{
			Account:          d.Account,
			TxID:             d.TxID,
			SeqValue:         d.SeqProxy.Value,
			IsTicket:         d.SeqProxy.IsTicket,
			FeeLevel:         uint64(d.FeeLevel),
			LastValid:        d.LastValid,
			Fee:              d.Fee,
			MaxSpendDrops:    d.PotentialSpend + d.Fee,
			AuthChange:       d.AuthChange,
			RetriesRemaining: d.RetriesRemaining,
			PreflightResult:  d.PreflightResult.String(),
			LastResult:       d.LastResult.String(),
			HasLastResult:    d.HasLastResult,
		}
		if d.Txn != nil {
			if flat, err := d.Txn.Flatten(); err == nil {
				info.TxJSON = flat
			}
		}
		out = append(out, info)
	}
	return out
}
