package adapter

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// SubmitTransaction submits a transaction to the open ledger.
// txBlobHex is the original signed transaction blob in hex. It is used for
// canonical re-ordering during AcceptLedger so the exact bytes are reused.
func (a *LedgerServiceAdapter) SubmitTransaction(txJSON []byte, txBlobHex string) (*types.SubmitResult, error) {
	return a.submitTransaction(txJSON, txBlobHex, false)
}

// SubmitTransactionFailHard submits with fail-hard semantics: a transaction
// that does not apply is neither held locally nor relayed.
func (a *LedgerServiceAdapter) SubmitTransactionFailHard(txJSON []byte, txBlobHex string) (*types.SubmitResult, error) {
	return a.submitTransaction(txJSON, txBlobHex, true)
}

// errorSubmitResult builds a non-applied SubmitResult whose engine fields are
// sourced from the canonical TER table, so the result token, code, and message
// stay in lockstep with rippled.
func errorSubmitResult(r ter.Result) *types.SubmitResult {
	return &types.SubmitResult{
		EngineResult:        r.String(),
		EngineResultCode:    int(r),
		EngineResultMessage: r.Message(),
		Applied:             false,
	}
}

func malformedSubmitResult() *types.SubmitResult {
	return errorSubmitResult(ter.TemMALFORMED)
}

func internalSubmitResult() *types.SubmitResult {
	return errorSubmitResult(ter.TefINTERNAL)
}

func (a *LedgerServiceAdapter) submitTransaction(txJSON []byte, txBlobHex string, failHard bool) (*types.SubmitResult, error) {
	// Parse and canonicalize a supplied blob before handing it to the ledger.
	// STTx serialization orders fields canonically, so accepted non-canonical
	// input must not change the transaction ID or relayed bytes.
	var rawBlob []byte
	var transaction tx.Transaction
	if txBlobHex != "" {
		var decodeErr error
		rawBlob, decodeErr = hex.DecodeString(txBlobHex)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode tx_blob: %w", decodeErr)
		}
		var parseErr error
		transaction, parseErr = tx.ParseFromBinary(rawBlob)
		if parseErr != nil {
			return malformedSubmitResult(), nil
		}
		fields, canonicalErr := binarycodec.DecodeBytes(rawBlob)
		if canonicalErr != nil {
			return malformedSubmitResult(), nil
		}
		canonicalHex, canonicalErr := binarycodec.Encode(fields)
		if canonicalErr != nil {
			return malformedSubmitResult(), nil
		}
		rawBlob, canonicalErr = hex.DecodeString(canonicalHex)
		if canonicalErr != nil {
			return nil, fmt.Errorf("decode canonical tx_blob: %w", canonicalErr)
		}
		transaction.SetRawBytes(rawBlob)
	} else {
		var parseErr error
		transaction, parseErr = tx.ParseJSON(txJSON)
		if parseErr != nil {
			return malformedSubmitResult(), nil
		}
	}

	// If no signed blob was supplied, encode the parsed JSON transaction for
	// open-ledger processing and relay.
	if rawBlob == nil {
		if txMap, fErr := transaction.Flatten(); fErr == nil {
			if hexStr, eErr := binarycodec.Encode(txMap); eErr == nil {
				var decodeErr error
				rawBlob, decodeErr = hex.DecodeString(hexStr)
				if decodeErr != nil {
					return nil, fmt.Errorf("decode encoded transaction: %w", decodeErr)
				}
			}
		}
	}
	if rawBlob == nil {
		var jsonMap map[string]any
		if jErr := json.Unmarshal(txJSON, &jsonMap); jErr == nil {
			if hexStr, eErr := binarycodec.Encode(jsonMap); eErr == nil {
				var decodeErr error
				rawBlob, decodeErr = hex.DecodeString(hexStr)
				if decodeErr != nil {
					return nil, fmt.Errorf("decode encoded transaction: %w", decodeErr)
				}
			}
		}
	}

	// Submit to the service with the raw blob for canonical ordering
	result, err := a.svc.SubmitTransaction(transaction, rawBlob, failHard)
	if err != nil {
		return internalSubmitResult(), nil
	}

	// Relay successful, claim, and queued outcomes. Retry/failure outcomes
	// remain local, and fail-hard suppresses relay for every non-applied result.
	broadcast := false
	relayable := result.Applied || result.Result == ter.TerQUEUED
	if failHard && !result.Applied {
		relayable = false
	}
	if relayable && rawBlob != nil && a.txBroadcaster != nil {
		a.txBroadcaster(rawBlob)
		broadcast = true
	}

	// Keep regular submissions except duplicate transactions. Fail-hard keeps
	// only an applied result.
	queued := result.Result == ter.TerQUEUED
	kept := (!failHard || result.Result == ter.TesSUCCESS) &&
		result.Result != ter.TefALREADY

	return &types.SubmitResult{
		EngineResult:           result.Result.String(),
		EngineResultCode:       int(result.Result),
		EngineResultMessage:    result.Message,
		Applied:                result.Applied,
		Broadcast:              broadcast,
		Queued:                 queued,
		Kept:                   kept,
		Fee:                    result.Fee,
		CurrentLedger:          result.CurrentLedger,
		CurrentLedgerCloseTime: result.CurrentLedgerCloseTime,
		ValidatedLedger:        result.ValidatedLedger,
	}, nil
}
