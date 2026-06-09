package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// signCredentials holds the signing credential parameters common to both
// the sign and submit RPC methods.
type signCredentials struct {
	Secret     string
	Seed       string
	SeedHex    string
	Passphrase string
	KeyType    string
}

// feeOptions holds the fee_mult_max and fee_div_max parameters for auto-fee.
// These control the maximum fee the auto-fill logic will accept.
//
// Defaults match rippled (Tuning.h):
//   - defaultAutoFillFeeMultiplier = 10
//   - defaultAutoFillFeeDivisor = 1
//
// The auto-filled fee is capped at: baseFee * mult / div
// If the network fee exceeds that limit, rpcHIGH_FEE is returned.
type feeOptions struct {
	Mult int // fee_mult_max (default 10)
	Div  int // fee_div_max (default 1)
}

// defaultFeeOptions returns fee options with rippled's defaults.
func defaultFeeOptions() feeOptions {
	return feeOptions{Mult: 10, Div: 1}
}

// parseFeeOptions extracts and validates fee_mult_max and fee_div_max from
// the raw RPC params. Returns the parsed options or an error matching
// rippled's exact error codes:
//   - Non-integer fee_mult_max → rpcHIGH_FEE with expected_field_message
//   - Negative fee_mult_max    → rpcINVALID_PARAMS with expected_field_message
//   - Non-integer fee_div_max  → rpcHIGH_FEE with expected_field_message
//   - Non-positive fee_div_max → rpcINVALID_PARAMS with expected_field_message
func parseFeeOptions(params json.RawMessage) (feeOptions, *types.RpcError) {
	opts := defaultFeeOptions()

	if len(params) == 0 {
		return opts, nil
	}

	// Parse into a generic map to inspect types
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return opts, nil // If we can't parse, let the main handler catch it
	}

	// Parse fee_mult_max
	if multRaw, ok := raw["fee_mult_max"]; ok {
		mult, err := parsePositiveIntParam(multRaw, "fee_mult_max", false)
		if err != nil {
			return opts, err
		}
		opts.Mult = mult
	}

	// Parse fee_div_max
	if divRaw, ok := raw["fee_div_max"]; ok {
		div, err := parsePositiveIntParam(divRaw, "fee_div_max", true)
		if err != nil {
			return opts, err
		}
		opts.Div = div
	}

	return opts, nil
}

// parsePositiveIntParam validates a JSON value as a positive integer.
// strictPositive=true means the value must be > 0 (for fee_div_max);
// strictPositive=false means the value must be >= 0 (for fee_mult_max).
//
// Matches rippled's checkFee() validation:
//   - If not an integer type → rpcHIGH_FEE
//   - If negative (or <=0 for strictPositive) → rpcINVALID_PARAMS
func parsePositiveIntParam(raw json.RawMessage, fieldName string, strictPositive bool) (int, *types.RpcError) {
	// Try to parse as a number
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&num); err != nil {
		// Not a valid JSON number → rpcHIGH_FEE
		return 0, types.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
	}

	// Check if it's an integer (no decimal point, no exponent notation)
	str := num.String()
	val, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		// Could be a float like "1.5" or too large
		if _, fErr := strconv.ParseFloat(str, 64); fErr == nil {
			// It's a valid float but not an integer → rpcHIGH_FEE
			return 0, types.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
		}
		// Not a number at all → rpcHIGH_FEE
		return 0, types.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
	}

	// Range check
	if val > math.MaxInt32 || val < math.MinInt32 {
		return 0, types.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
	}

	intVal := int(val)

	if strictPositive {
		// fee_div_max must be > 0
		if intVal <= 0 {
			return 0, types.RpcErrorExpectedField(fieldName, "a positive integer")
		}
	} else {
		// fee_mult_max must be >= 0 (rippled checks mult < 0)
		if intVal < 0 {
			return 0, types.RpcErrorExpectedField(fieldName, "a positive integer")
		}
	}

	return intVal, nil
}

// signResult holds the output of the signing operation.
type signResult struct {
	TxMap  map[string]any // The transaction JSON map with SigningPubKey, TxnSignature, and hash
	TxBlob string         // The hex-encoded signed transaction blob
}

// signTransactionJSON takes a raw tx_json and signing credentials, derives the
// keypair, auto-fills missing fields (unless offline), signs the transaction,
// and returns the signed tx map + blob. This is the shared logic used by both
// the "sign" and "submit" RPC methods.
//
// The feeOpts parameter controls auto-fee behavior: if Fee is not present in
// tx_json and auto-fill is active, the network fee is computed and checked
// against the limit feeDefault * feeOpts.Mult / feeOpts.Div. unlimited
// mirrors rippled's isUnlimited(role) load-scaling carve-out.
func signTransactionJSON(ctx context.Context, services *types.ServiceContainer, txJSON json.RawMessage, creds signCredentials, offline bool, unlimited bool, apiVersion int, feeOpts feeOptions) (*signResult, *types.RpcError) {
	// Check if ledger service is available (needed for auto-filling fields)
	if !offline && (services == nil || services.Ledger == nil) {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Parse credentials and derive keypair using the shared helper
	privateKey, publicKey, rpcErr := parseCredentialsAndDeriveKeypair(
		creds.Secret,
		creds.Seed,
		creds.SeedHex,
		creds.Passphrase,
		creds.KeyType,
		apiVersion,
	)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Derive address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to derive address: %v", err))
	}

	// Parse the transaction JSON
	var txMap map[string]any
	if err := json.Unmarshal(txJSON, &txMap); err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid tx_json: %v", err))
	}

	// Verify the account matches the signing key
	if txAccount, ok := txMap["Account"].(string); ok {
		if txAccount != address {
			return nil, types.RpcErrorInvalidParams("Account in tx_json does not match signing key")
		}
	} else {
		txMap["Account"] = address
	}

	// Fill in missing fields if not offline. Order matches rippled's
	// transactionPreProcessImpl (TransactionSign.cpp:454-505): source
	// account existence, then Sequence, NetworkID, and Fee.
	if !offline {
		// The source account must exist in the current ledger, whether or
		// not Sequence is supplied (rpcSRC_ACT_NOT_FOUND).
		if _, err := services.Ledger.GetAccountInfo(ctx, address, "current"); err != nil {
			if errors.Is(err, svcerr.ErrAccountNotFound) {
				return nil, types.RpcErrorSrcActNotFound("Source account not found.")
			}
			return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to read source account: %v", err))
		}

		// Auto-fill Sequence from the open ledger / TxQ; a present
		// TicketSequence supplies the sequence instead (Sequence = 0).
		if _, ok := txMap["Sequence"]; !ok {
			_, hasTicket := txMap["TicketSequence"]
			seq, err := services.Ledger.GetAutofillSequence(address, hasTicket)
			if err != nil {
				if errors.Is(err, svcerr.ErrAccountNotFound) {
					return nil, types.RpcErrorSrcActNotFound("Source account not found.")
				}
				return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to autofill sequence: %v", err))
			}
			txMap["Sequence"] = seq
		}

		// Do NOT auto-fill LastLedgerSequence. Rippled's
		// transactionPreProcessImpl (TransactionSign.cpp:409-491) only
		// autofills Sequence / NetworkID / Fee / SigningPubKey /
		// TxnSignature; LastLedgerSequence is left to the caller, and
		// adding it server-side produces different signed bytes for the
		// same client tx_json.

		// Auto-fill NetworkID if not present and network ID > 1024.
		// Matches rippled's transactionPreProcessImpl() in TransactionSign.cpp:
		// legacy networks (ID <= 1024) must NOT include NetworkID;
		// new networks (ID > 1024) require it and it is auto-filled here.
		if _, ok := txMap["NetworkID"]; !ok {
			serverInfo := services.Ledger.GetServerInfo()
			if serverInfo.NetworkID > 1024 {
				txMap["NetworkID"] = serverInfo.NetworkID
			}
		}

		// Auto-fill Fee if not present: load-scaled, escalation-aware
		// network fee with a feeDefault * fee_mult_max / fee_div_max
		// ceiling. Matches rippled checkFee() → getCurrentNetworkFee().
		if _, ok := txMap["Fee"]; !ok {
			probe, mErr := json.Marshal(txMap)
			if mErr != nil {
				return nil, types.RpcErrorInternal("Failed to marshal tx_json for fee autofill")
			}
			fee, feeErr := services.Ledger.GetAutofillFee(probe, unlimited, feeOpts.Mult, feeOpts.Div)
			if feeErr != nil {
				var hfe *svcerr.HighFeeError
				if errors.As(feeErr, &hfe) {
					return nil, types.RpcErrorHighFee(hfe.Error())
				}
				return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to autofill fee: %v", feeErr))
			}
			txMap["Fee"] = formatUint64AsString(fee)
		}
	} else {
		// Offline callers must supply Sequence and Fee themselves
		// (rippled TransactionSign.cpp:451-452 and checkFee with
		// doAutoFill == false).
		if _, ok := txMap["Sequence"]; !ok {
			return nil, types.RpcErrorMissingField("tx_json.Sequence")
		}
		if _, ok := txMap["Fee"]; !ok {
			return nil, types.RpcErrorMissingField("tx_json.Fee")
		}
	}

	txMap["SigningPubKey"] = publicKey

	txBytes, err := json.Marshal(txMap)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to marshal transaction: %v", err))
	}

	transaction, err := tx.ParseJSON(txBytes)
	if err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Failed to parse transaction: %v", err))
	}

	txCommon := transaction.GetCommon()
	txCommon.SigningPubKey = publicKey

	signature, err := tx.SignTransaction(transaction, privateKey)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to sign transaction: %v", err))
	}

	txMap["TxnSignature"] = signature

	txBlob, err := binarycodec.Encode(txMap)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to encode transaction: %v", err))
	}

	txHash := CalculateTxHash(txBlob)
	txMap["hash"] = txHash

	return &signResult{
		TxMap:  txMap,
		TxBlob: txBlob,
	}, nil
}
