package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// counterpartySignatureField is the only inner-object SField a signature_target
// may name, matching rippled (the LoanSet sfCounterpartySignature).
const counterpartySignatureField = "CounterpartySignature"

const signingDeprecation = "This command has been deprecated and will be removed in a future version of the server. Please migrate to a standalone signing tool."

// signCredentials holds the signing credential parameters common to both
// the sign and submit RPC methods.
type signCredentials struct {
	Secret     json.RawMessage `json:"secret,omitempty"`
	Seed       json.RawMessage `json:"seed,omitempty"`
	SeedHex    json.RawMessage `json:"seed_hex,omitempty"`
	Passphrase json.RawMessage `json:"passphrase,omitempty"`
	KeyType    json.RawMessage `json:"key_type,omitempty"`
}

func (c signCredentials) any(params json.RawMessage) bool {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(params, &fields)
	for _, field := range []string{"secret", "seed", "seed_hex", "passphrase"} {
		if _, ok := fields[field]; ok {
			return true
		}
	}
	return false
}

func (c signCredentials) deriveKeypair(apiVersion int, params json.RawMessage) (string, string, string, *types.RpcError) {
	return parseCredentialsAndDeriveKeypair(apiVersion, params)
}

type signingRequest struct {
	signCredentials
	TxJson          json.RawMessage `json:"tx_json"`
	Offline         bool            `json:"offline,omitempty"`
	BuildPath       bool            `json:"build_path,omitempty"`
	SignatureTarget string          `json:"signature_target,omitempty"`
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

func formatSignResult(result signResult, apiVersion int) map[string]any {
	txprojection.InjectDeliverMax(result.TxMap, apiVersion)
	response := map[string]any{
		"tx_blob": result.TxBlob,
		"tx_json": result.TxMap,
	}
	if apiVersion > 1 {
		if hash, ok := result.TxMap["hash"].(string); ok {
			response["hash"] = hash
		}
	}
	return response
}

func rejectDisabledSigning(ctx *types.RpcContext) *types.RpcError {
	if ctx != nil && ctx.Role == types.RoleAdmin {
		return nil
	}
	if ctx != nil && ctx.Services != nil && ctx.Services.Capabilities.SigningEnabled {
		return nil
	}
	return types.RpcErrorNotSupported("Signing is not supported by this server.")
}

func addSigningDeprecation(result any, rpcErr *types.RpcError) (any, *types.RpcError) {
	if rpcErr != nil {
		return result, rpcErr.WithExtra(map[string]any{"deprecated": signingDeprecation})
	}
	if response, ok := result.(map[string]any); ok {
		response["deprecated"] = signingDeprecation
	}
	return result, nil
}

func jsonFieldPresent(params json.RawMessage, field string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return false
	}
	_, ok := fields[field]
	return ok
}

func rpcErrorAlreadyMultisigned() *types.RpcError {
	return types.NewRpcError(
		types.RpcALREADY_MULTISIG, "alreadyMultisig", "alreadyMultisig", "Already multisigned.")
}

func rpcErrorAlreadySingleSigned() *types.RpcError {
	return types.NewRpcError(
		types.RpcALREADY_SINGLE_SIG, "alreadySingleSig", "alreadySingleSig", "Already single-signed.")
}

func submitWithFailHard(ledger types.LedgerService, txJSON []byte, txBlob string, failHard bool) (*types.SubmitResult, error) {
	if failHard {
		if submitter, ok := ledger.(types.FailHardSubmitter); ok {
			return submitter.SubmitTransactionFailHard(txJSON, txBlob)
		}
	}
	return ledger.SubmitTransaction(txJSON, txBlob)
}

// signTransactionJSON takes a raw tx_json and signing credentials, derives the
// keypair, auto-fills missing fields (unless offline), signs the transaction,
// and returns the signed tx map + blob. This is the shared logic used by both
// the "sign" and "submit" RPC methods.
//
// rawParams carries the caller's request so fee_mult_max / fee_div_max are
// read only when Fee is actually autofilled — rippled's checkFee returns
// before inspecting them when Fee is present or offline.
func signTransactionJSON(rpcCtx *types.RpcContext, txJSON json.RawMessage, creds signCredentials, offline bool, rawParams json.RawMessage, signatureTarget string) (*signResult, *types.RpcError) {
	ctx := rpcCtx.Context
	services := rpcCtx.Services
	apiVersion := rpcCtx.ApiVersion
	// Check if ledger service is available (needed for auto-filling fields)
	if !offline && (services == nil || services.Ledger == nil) {
		return nil, rpcInternalInvariantError("sign: ledger service unavailable")
	}

	// Parse credentials and derive keypair using the shared helper
	privateKey, publicKey, _, rpcErr := creds.deriveKeypair(apiVersion, rawParams)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Derive address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if err != nil {
		return nil, rpcInternalError("sign: account address derivation failed", err)
	}

	// Parse the transaction JSON
	var txMap map[string]any
	if err := json.Unmarshal(txJSON, &txMap); err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid tx_json: %v", err))
	}
	if txMap == nil {
		return nil, types.RpcErrorExpectedField("tx_json", "object")
	}
	// signature_target directs the signature into a nested inner object instead
	// of the top level. Only CounterpartySignature is a valid target; any other
	// field name is rejected with the field name as the message, matching
	// rippled TransactionSign.cpp.
	signatureTargetPresent := jsonFieldPresent(rawParams, "signature_target")
	if signatureTargetPresent && signatureTarget != counterpartySignatureField {
		return nil, types.RpcErrorInvalidParams(signatureTarget)
	}

	// srcAddress is the account whose Sequence/Fee are autofilled and whose
	// existence is checked. Without a target it is the signing key's account,
	// which must match a supplied Account (rippled checkTxJsonFields →
	// rpcSRC_ACT_MALFORMED, then acctMatchesPubKey). With a target the signature
	// belongs to the counterparty, so account and secret need not correspond:
	// the caller's Account (the primary signer) is the source and its ownership
	// is not checked.
	if _, ok := txMap["TransactionType"]; !ok {
		return nil, types.RpcErrorMissingField("tx_json.TransactionType")
	}

	txAccountValue, accountPresent := txMap["Account"]
	if !accountPresent {
		return nil, types.RpcErrorSrcActMissing("Missing field 'tx_json.Account'.")
	}
	txAccount, ok := txAccountValue.(string)
	if !ok || !types.IsValidClassicAddress(txAccount) {
		return nil, types.RpcErrorSrcActMalformed("Invalid field 'tx_json.Account'.")
	}
	if rpcErr := rejectOnlineSigningWithoutCurrentLedger(services, offline, apiVersion); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := rejectSigningWhenLoaded(services, rpcCtx.Unlimited); rpcErr != nil {
		return nil, rpcErr
	}
	srcAddress := txAccount
	var sourceAccountInfo *types.AccountInfo

	// Fill in missing fields if not offline. Order matches rippled's
	// transactionPreProcessImpl (TransactionSign.cpp:454-505): source
	// account existence, then Sequence, NetworkID, and Fee.
	if !offline {
		// The source account must exist in the current ledger, whether or
		// not Sequence is supplied (rpcSRC_ACT_NOT_FOUND).
		var err error
		sourceAccountInfo, err = services.Ledger.GetAccountInfo(ctx, srcAddress, "current")
		if err != nil {
			if errors.Is(err, svcerr.ErrAccountNotFound) {
				return nil, types.RpcErrorSrcActNotFound("Source account not found.")
			}
			return nil, rpcInternalError("sign: source account lookup failed", err)
		}

		// Auto-fill Sequence from the open ledger / TxQ; a present
		// TicketSequence supplies the sequence instead (Sequence = 0).
		if _, ok := txMap["Sequence"]; !ok {
			_, hasTicket := txMap["TicketSequence"]
			seq, err := services.Ledger.GetAutofillSequence(srcAddress, hasTicket)
			if err != nil {
				if errors.Is(err, svcerr.ErrAccountNotFound) {
					return nil, types.RpcErrorSrcActNotFound("Source account not found.")
				}
				return nil, rpcInternalError("sign: sequence autofill failed", err)
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
			feeOpts, rpcErr := parseFeeOptions(rawParams)
			if rpcErr != nil {
				return nil, rpcErr
			}
			probe, mErr := json.Marshal(txMap)
			if mErr != nil {
				return nil, rpcInternalError("sign: fee probe marshaling failed", mErr)
			}
			fee, feeErr := services.Ledger.GetAutofillFee(probe, rpcCtx.Unlimited, feeOpts.Mult, feeOpts.Div)
			if feeErr != nil {
				var hfe *svcerr.HighFeeError
				if errors.As(feeErr, &hfe) {
					return nil, types.RpcErrorHighFee(hfe.Error())
				}
				return nil, rpcInternalError("sign: fee autofill failed", feeErr)
			}
			txMap["Fee"] = strconv.FormatUint(fee, 10)
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
	if _, ok := txMap["Signers"]; ok {
		return nil, rpcErrorAlreadyMultisigned()
	}
	if !signatureTargetPresent && !offline {
		authorizationAccount := txAccount
		delegatePresent := false
		if delegateValue, present := txMap["Delegate"]; present {
			delegatePresent = true
			delegate, ok := delegateValue.(string)
			if !ok || !addresscodec.IsValidClassicAddress(delegate) {
				return nil, types.RpcErrorSrcActMalformed("Invalid field 'tx_json.Delegate'.")
			}
			authorizationAccount = delegate
		}
		if !delegatePresent {
			if rpcErr := signingKeyAuthorization(authorizationAccount, address, sourceAccountInfo, false); rpcErr != nil {
				return nil, rpcErr
			}
		} else if rpcErr := authorizeSigningKey(rpcCtx, authorizationAccount, address, true); rpcErr != nil {
			return nil, rpcErr
		}
	}

	// Without a target the signing key is the transaction's own key, placed at
	// the top level. With a target the top-level SigningPubKey (the primary
	// signer's) is left untouched so the counterparty covers the same signing
	// payload; the counterparty's key goes into the nested object.
	var targetObj map[string]any
	if !signatureTargetPresent {
		txMap["SigningPubKey"] = publicKey
	} else {
		var rpcErr *types.RpcError
		targetObj, rpcErr = signatureTargetObject(txMap, signatureTarget)
		if rpcErr != nil {
			return nil, rpcErr
		}
		targetObj["SigningPubKey"] = publicKey
	}

	transaction, rpcErr := parseTransactionForSigning(txMap)
	if rpcErr != nil {
		return nil, rpcErr
	}

	signature, err := sign.SignTransaction(transaction, privateKey)
	if err != nil {
		return nil, rpcInternalError("sign: transaction signing failed", err)
	}

	if !signatureTargetPresent {
		txMap["TxnSignature"] = signature
	} else {
		targetObj["TxnSignature"] = signature
	}

	txBlob, err := binarycodec.Encode(txMap)
	if err != nil {
		return nil, rpcInternalError("sign: transaction encoding failed", err)
	}

	txHash := CalculateTxHash(txBlob)
	txMap["hash"] = txHash

	return &signResult{
		TxMap:  txMap,
		TxBlob: txBlob,
	}, nil
}

func authorizeSigningKey(ctx *types.RpcContext, account, derivedAccount string, requireAccount bool) *types.RpcError {
	var accountInfo *types.AccountInfo
	if ctx != nil && ctx.Services != nil && ctx.Services.Ledger != nil {
		info, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, account, "current")
		if err != nil {
			if !errors.Is(err, svcerr.ErrAccountNotFound) {
				return rpcInternalError("signing authorization account lookup failed", err)
			}
		} else {
			accountInfo = info
		}
	}

	return signingKeyAuthorization(account, derivedAccount, accountInfo, requireAccount)
}

func signingKeyAuthorization(account, derivedAccount string, accountInfo *types.AccountInfo, requireAccount bool) *types.RpcError {
	if accountInfo == nil {
		if requireAccount {
			return types.RpcErrorDelegateActNotFound()
		}
		if derivedAccount == account {
			return nil
		}
		return types.RpcErrorBadSecret()
	}
	if derivedAccount == account {
		if accountInfo.Flags&entry.LsfDisableMaster != 0 {
			return types.RpcErrorMasterDisabled()
		}
		return nil
	}
	if accountInfo.RegularKey == derivedAccount {
		return nil
	}
	return types.RpcErrorBadSecret()
}

func rejectSigningWhenLoaded(services *types.ServiceContainer, unlimited bool) *types.RpcError {
	if unlimited || services == nil || services.IsLoadedCluster == nil || !services.IsLoadedCluster() {
		return nil
	}
	return types.RpcErrorTooBusy()
}

func rejectOnlineSigningWithoutCurrentLedger(services *types.ServiceContainer, offline bool, apiVersion int) *types.RpcError {
	if offline || services == nil || services.Ledger == nil {
		return nil
	}
	info := services.Ledger.GetServerInfo()
	if info.Standalone || !types.ValidatedLedgerStale(info) {
		return nil
	}
	return types.CurrentLedgerUnavailable(apiVersion)
}

func signatureTargetObject(txMap map[string]any, target string) (map[string]any, *types.RpcError) {
	value, exists := txMap[target]
	if !exists {
		object := make(map[string]any)
		txMap[target] = object
		return object, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid field 'tx_json.%s'.", target))
	}
	return object, nil
}

func parseTransactionForSigning(txMap map[string]any) (tx.Transaction, *types.RpcError) {
	blob, err := binarycodec.EncodeBytes(txMap)
	if err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Failed to parse transaction: %v", err))
	}
	transaction, err := tx.ParseFromBinary(blob)
	if err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Failed to parse transaction: %v", err))
	}
	return transaction, nil
}
