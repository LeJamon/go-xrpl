package adapter

import (
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// SimulateTransaction runs a transaction against a snapshot without committing
func (a *LedgerServiceAdapter) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	transaction, err := tx.ParseJSON(txJSON)
	if err != nil {
		return malformedSubmitResult(), nil
	}

	result, err := a.svc.SimulateTransaction(transaction)
	if err != nil {
		return internalSubmitResult(), nil
	}

	out := &types.SubmitResult{
		EngineResult:           result.Result.String(),
		EngineResultCode:       int(result.Result),
		EngineResultMessage:    result.Message,
		Applied:                result.Applied,
		Fee:                    result.Fee,
		CurrentLedger:          result.CurrentLedger,
		CurrentLedgerCloseTime: result.CurrentLedgerCloseTime,
		ValidatedLedger:        result.ValidatedLedger,
	}
	if result.Metadata != nil {
		blob, serErr := tx.SerializeMetadata(result.Metadata)
		if serErr != nil {
			return nil, fmt.Errorf("serialize metadata: %w", serErr)
		}
		out.Metadata = &types.SubmitMetadata{
			JSON: result.Metadata,
			Blob: blob,
		}
	}
	return out, nil
}

// TransactionRules returns the rules used by the ledger service for the
// current open ledger.
func (a *LedgerServiceAdapter) TransactionRules() *amendment.Rules {
	return a.svc.TransactionRules()
}

func (a *LedgerServiceAdapter) GetAutofillFee(txJSON []byte, unlimited bool, mult, div int) (uint64, error) {
	// Fee autofill falls back to the reference fee when parsing fails; the
	// service still performs the later structural checks.
	parsedTx, _ := tx.ParseJSON(txJSON)
	return a.svc.GetAutofillFee(parsedTx, unlimited, mult, div)
}

func (a *LedgerServiceAdapter) GetAutofillSequence(account string, hasTicketSequence bool) (uint32, error) {
	return a.svc.GetAutofillSequence(account, hasTicketSequence)
}

// IsAmendmentBlocked returns true if the server is blocked by unsupported amendments
func (a *LedgerServiceAdapter) IsAmendmentBlocked() bool {
	return a.svc.IsAmendmentBlocked()
}

// Table exposes the live amendment table for RPC introspection
// (feature command, server_info warnings). May be nil.
func (a *LedgerServiceAdapter) Table() *amendment.Table {
	return a.svc.Table()
}

// SetAmendmentVote records an operator veto/upvote and persists it.
func (a *LedgerServiceAdapter) SetAmendmentVote(ctx context.Context, id [32]byte, vetoed bool) error {
	return a.svc.SetAmendmentVote(ctx, id, vetoed)
}
