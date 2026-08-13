package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/txprojection"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// counterpartySignatureField is the only inner-object SField a signature_target
// may name, matching rippled (the LoanSet sfCounterpartySignature).
const counterpartySignatureField = "CounterpartySignature"

const (
	signingDeprecation       = "This command has been deprecated and will be removed in a future version of the server. Please migrate to a standalone signing tool."
	submitSigningDeprecation = "Signing support in the 'submit' command has been deprecated and will be removed in a future version of the server. Please migrate to a standalone signing tool."
)

// signCredentials holds the signing credential parameters common to both
// the sign and submit RPC methods.
type signCredentials struct {
	Secret     json.RawMessage `json:"secret,omitempty"`
	Seed       json.RawMessage `json:"seed,omitempty"`
	SeedHex    json.RawMessage `json:"seed_hex,omitempty"`
	Passphrase json.RawMessage `json:"passphrase,omitempty"`
	KeyType    json.RawMessage `json:"key_type,omitempty"`
}

func (c signCredentials) deriveKeypair(apiVersion int, params json.RawMessage) (string, string, string, *rpcerrors.RpcError) {
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
func parseFeeOptions(params json.RawMessage) (feeOptions, *rpcerrors.RpcError) {
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
func parsePositiveIntParam(raw json.RawMessage, fieldName string, strictPositive bool) (int, *rpcerrors.RpcError) {
	// Try to parse as a number
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&num); err != nil {
		// Not a valid JSON number → rpcHIGH_FEE
		return 0, rpcerrors.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
	}

	// Check if it's an integer (no decimal point, no exponent notation)
	str := num.String()
	val, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		// Could be a float like "1.5" or too large
		if _, fErr := strconv.ParseFloat(str, 64); fErr == nil {
			// It's a valid float but not an integer → rpcHIGH_FEE
			return 0, rpcerrors.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
		}
		// Not a number at all → rpcHIGH_FEE
		return 0, rpcerrors.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
	}

	// Range check
	if val > math.MaxInt32 || val < math.MinInt32 {
		return 0, rpcerrors.RpcErrorExpectedFieldHighFee(fieldName, "a positive integer")
	}

	intVal := int(val)

	if strictPositive {
		// fee_div_max must be > 0
		if intVal <= 0 {
			return 0, rpcerrors.RpcErrorExpectedField(fieldName, "a positive integer")
		}
	} else {
		// fee_mult_max must be >= 0 (rippled checks mult < 0)
		if intVal < 0 {
			return 0, rpcerrors.RpcErrorExpectedField(fieldName, "a positive integer")
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
	hash, _ := result.TxMap["hash"].(string)
	return txprojection.FormatResult(
		result.TxMap,
		result.TxBlob,
		hash,
		apiVersion,
		txprojection.PathSigned,
	)
}

func rejectDisabledSigning(ctx *types.RpcContext) *rpcerrors.RpcError {
	if ctx != nil && ctx.Role.IsAdmin() {
		return nil
	}
	if ctx != nil && ctx.Services != nil && ctx.Services.Capabilities().SigningEnabled {
		return nil
	}
	return rpcerrors.RpcErrorNotSupported("Signing is not supported by this server.")
}

func addSigningDeprecation(result any, rpcErr *rpcerrors.RpcError) (any, *rpcerrors.RpcError) {
	return addDeprecation(result, rpcErr, signingDeprecation)
}

func addDeprecation(result any, rpcErr *rpcerrors.RpcError, message string) (any, *rpcerrors.RpcError) {
	if rpcErr != nil {
		return result, rpcErr.WithExtra(map[string]any{"deprecated": message})
	}
	if response, ok := result.(map[string]any); ok {
		response["deprecated"] = message
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

func checkPayment(txMap map[string]any, params json.RawMessage, doPath bool, rpcCtx *types.RpcContext) *rpcerrors.RpcError {
	if txMap["TransactionType"] != "Payment" {
		return nil
	}

	if deliverMax, ok := txMap["DeliverMax"]; ok {
		if amount, present := txMap["Amount"]; present {
			if !reflect.DeepEqual(amount, deliverMax) {
				return rpcerrors.RpcErrorInvalidParams(
					"Cannot specify differing 'Amount' and 'DeliverMax'")
			}
		} else {
			txMap["Amount"] = deliverMax
		}
		delete(txMap, "DeliverMax")
	}

	amountValue, ok := txMap["Amount"]
	if !ok {
		return rpcerrors.RpcErrorMissingField("tx_json.Amount")
	}
	amountJSON, err := json.Marshal(amountValue)
	if err != nil {
		return rpcerrors.RpcErrorInvalidField("tx_json.Amount")
	}
	amount, err := state.AmountFromJSON(amountJSON)
	if err != nil {
		return rpcerrors.RpcErrorInvalidField("tx_json.Amount")
	}

	destinationValue, present := txMap["Destination"]
	destination, destinationIsString := destinationValue.(string)
	if !present {
		return rpcerrors.RpcErrorMissingField("tx_json.Destination")
	}
	if !destinationIsString || !addresscodec.IsValidClassicAddress(destination) {
		return rpcerrors.RpcErrorInvalidField("tx_json.Destination")
	}

	buildPath := jsonFieldPresent(params, "build_path")
	if buildPath && !doPath {
		return rpcerrors.RpcErrorInvalidParams(
			"Field 'build_path' not allowed in this context.")
	}
	if buildPath && amount.IsMPT() {
		if rpcCtx == nil || rpcCtx.Services == nil || rpcCtx.Services.Ledger() == nil {
			return rpcerrors.RpcErrorInvalidParams(
				"Field 'build_path' not allowed in this context.")
		}
		rulesSource, ok := rpcCtx.Services.Ledger().(types.TransactionRulesSource)
		if !ok {
			return rpcerrors.RpcErrorInvalidParams(
				"Field 'build_path' not allowed in this context.")
		}
		rules := rulesSource.TransactionRules()
		if rules == nil || !rules.MPTokensV2Enabled() {
			return rpcerrors.RpcErrorInvalidParams(
				"Field 'build_path' not allowed in this context.")
		}
	}
	_, pathsPresent := txMap["Paths"]
	if buildPath && pathsPresent {
		return rpcerrors.RpcErrorInvalidParams(
			"Cannot specify both 'tx_json.Paths' and 'build_path'")
	}
	var domainID *[32]byte
	if domain, ok := txMap["DomainID"]; ok {
		domainString, isString := domain.(string)
		decoded, decodeErr := hex.DecodeString(domainString)
		if !isString || decodeErr != nil || len(decoded) != 32 {
			return rpcerrors.RpcErrorDomainMalformed("Unable to parse 'DomainID'.")
		}
		var parsed [32]byte
		copy(parsed[:], decoded)
		domainID = &parsed
	}
	if buildPath {
		var sendMax *state.Amount
		if sendMaxValue, present := txMap["SendMax"]; present {
			sendMaxJSON, marshalErr := json.Marshal(sendMaxValue)
			if marshalErr != nil {
				return rpcerrors.RpcErrorInvalidField("tx_json.SendMax")
			}
			parsedSendMax, parseErr := state.AmountFromJSON(sendMaxJSON)
			if parseErr != nil {
				return rpcerrors.RpcErrorInvalidField("tx_json.SendMax")
			}
			sendMax = &parsedSendMax
			if parsedSendMax.IsNative() && amount.IsNative() {
				return rpcerrors.RpcErrorInvalidParams(
					"Cannot build XRP to XRP paths.")
			}
		} else if amount.IsNative() {
			// A missing SendMax defaults to Amount. This is only relevant to
			// the native/native rejection; issued amounts remain pathable.
			return rpcerrors.RpcErrorInvalidParams(
				"Cannot build XRP to XRP paths.")
		}
		if rpcCtx == nil || rpcCtx.Services == nil || rpcCtx.Services.Ledger() == nil {
			return rpcInternalInvariantError("payment: ledger service unavailable for path construction")
		}
		release, rpcErr := acquirePathfind(rpcCtx)
		if rpcErr != nil {
			return rpcErr
		}
		defer release()
		viewSource, ok := rpcCtx.Services.Ledger().(types.OpenLedgerViewSource)
		if !ok {
			return rpcInternalInvariantError("payment: open ledger view unavailable for path construction")
		}
		view, viewErr := viewSource.GetOpenLedgerView()
		if viewErr != nil || view == nil {
			if viewErr == nil {
				viewErr = errors.New("open ledger view is nil")
			}
			return rpcInternalError("payment: open ledger view unavailable", viewErr)
		}
		source, sourceOK := txMap["Account"].(string)
		if !sourceOK {
			return rpcInternalInvariantError("payment: source account unavailable for path construction")
		}
		_, sourceID, sourceErr := addresscodec.DecodeClassicAddressToAccountID(source)
		if sourceErr != nil {
			return rpcInternalError("payment: source account decode failed", sourceErr)
		}
		_, destinationID, destinationErr := addresscodec.DecodeClassicAddressToAccountID(destination)
		if destinationErr != nil {
			return rpcInternalError("payment: destination account decode failed", destinationErr)
		}
		var sourceAccountID, destinationAccountID [20]byte
		copy(sourceAccountID[:], sourceID)
		copy(destinationAccountID[:], destinationID)
		if sendMax == nil {
			defaultSendMax := amount
			if !defaultSendMax.IsNative() && !defaultSendMax.IsMPT() {
				defaultSendMax.Issuer = source
			}
			sendMax = &defaultSendMax
		}
		request := pathfinder.NewPathRequest(sourceAccountID, destinationAccountID, amount, sendMax, nil, false)
		request.SetDomainID(domainID)
		pathResult := request.Execute(view)
		if pathResult == nil {
			return rpcInternalInvariantError("payment: path construction returned no result")
		}
		if pathResult.SourceCurrencyOverflow {
			return rpcInternalInvariantError("payment: source currency limit exceeded")
		}
		if len(pathResult.Alternatives) == 0 {
			return nil
		}
		paths := pathResult.Alternatives[0].PathsComputed
		if len(paths) == 0 {
			return nil
		}
		encodedPaths, marshalErr := json.Marshal(paths)
		if marshalErr != nil {
			return rpcInternalError("payment: path serialization failed", marshalErr)
		}
		var pathJSON []any
		if unmarshalErr := json.Unmarshal(encodedPaths, &pathJSON); unmarshalErr != nil {
			return rpcInternalError("payment: path serialization failed", unmarshalErr)
		}
		txMap["Paths"] = pathJSON
	}

	return nil
}

func rpcErrorAlreadyMultisigned() *rpcerrors.RpcError {
	return rpcerrors.NewRpcError(
		rpcerrors.RpcALREADY_MULTISIG, "alreadyMultisig", "alreadyMultisig", "Already multisigned.")
}

func rpcErrorAlreadySingleSigned() *rpcerrors.RpcError {
	return rpcerrors.NewRpcError(
		rpcerrors.RpcALREADY_SINGLE_SIG, "alreadySingleSig", "alreadySingleSig", "Already single-signed.")
}

func submitWithFailHard(ledger types.TransactionSubmission, txJSON []byte, txBlob string, failHard bool) (*types.SubmitResult, error) {
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
func signTransactionJSON(rpcCtx *types.RpcContext, txJSON json.RawMessage, creds signCredentials, offline bool, rawParams json.RawMessage, signatureTarget string) (*signResult, *rpcerrors.RpcError) {
	ctx := rpcCtx.Context
	services := rpcCtx.Services
	apiVersion := rpcCtx.ApiVersion

	// Parse credentials and derive keypair using the shared helper
	privateKey, publicKey, _, rpcErr := creds.deriveKeypair(apiVersion, rawParams)
	if rpcErr != nil {
		return nil, rpcErr
	}
	signatureTargetPresent := jsonFieldPresent(rawParams, "signature_target")
	if signatureTargetPresent && signatureTarget != counterpartySignatureField {
		return nil, rpcerrors.RpcErrorInvalidParams(signatureTarget)
	}
	if len(txJSON) == 0 {
		return nil, rpcerrors.RpcErrorMissingField("tx_json")
	}

	// Check if ledger service is available (needed for auto-filling fields)
	// only after credentials have entered the shared signing preprocessing.
	if !offline && (services == nil || services.Ledger() == nil) {
		return nil, rpcInternalInvariantError("sign: ledger service unavailable")
	}

	// Derive address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if err != nil {
		return nil, rpcInternalError("sign: account address derivation failed", err)
	}

	// Parse the transaction JSON
	var txValue any
	decoder := json.NewDecoder(bytes.NewReader(txJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&txValue); err != nil {
		return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid tx_json: %v", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid tx_json: expected object")
	}
	txMap, ok := txValue.(map[string]any)
	if !ok {
		return nil, rpcerrors.RpcErrorExpectedField("tx_json", "object")
	}
	// signature_target directs the signature into a nested inner object instead
	// of the top level. Only CounterpartySignature is a valid target; any other
	// field name is rejected with the field name as the message, matching
	// rippled TransactionSign.cpp.
	// srcAddress is the account whose Sequence/Fee are autofilled and whose
	// existence is checked. Without a target it is the signing key's account,
	// which must match a supplied Account (rippled checkTxJsonFields →
	// rpcSRC_ACT_MALFORMED, then acctMatchesPubKey). With a target the signature
	// belongs to the counterparty, so account and secret need not correspond:
	// the caller's Account (the primary signer) is the source and its ownership
	// is not checked.
	if _, ok := txMap["TransactionType"]; !ok {
		return nil, rpcerrors.RpcErrorMissingField("tx_json.TransactionType")
	}

	txAccountValue, accountPresent := txMap["Account"]
	if !accountPresent {
		return nil, rpcerrors.RpcErrorSrcActMissing("Missing field 'tx_json.Account'.")
	}
	txAccount, ok := txAccountValue.(string)
	if !ok || !types.IsValidClassicAddress(txAccount) {
		return nil, rpcerrors.RpcErrorSrcActMalformed("Invalid field 'tx_json.Account'.")
	}
	if rpcErr := rejectOnlineSigningWithoutCurrentLedger(services, offline, apiVersion); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := rejectSigningWhenLoaded(services, rpcCtx.Role.IsUnlimited()); rpcErr != nil {
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
		sourceAccountInfo, err = services.Ledger().GetAccountInfo(ctx, srcAddress, "current")
		if err != nil {
			if errors.Is(err, svcerr.ErrAccountNotFound) {
				return nil, rpcerrors.RpcErrorSrcActNotFound("Source account not found.")
			}
			return nil, rpcInternalError("sign: source account lookup failed", err)
		}

		// Auto-fill Sequence from the open ledger / TxQ; a present
		// TicketSequence supplies the sequence instead (Sequence = 0).
		if _, ok := txMap["Sequence"]; !ok {
			_, hasTicket := txMap["TicketSequence"]
			seq, err := services.LedgerMutation().GetAutofillSequence(srcAddress, hasTicket)
			if err != nil {
				if errors.Is(err, svcerr.ErrAccountNotFound) {
					return nil, rpcerrors.RpcErrorSrcActNotFound("Source account not found.")
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
			serverInfo := services.Ledger().GetServerInfo()
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
			fee, feeErr := services.LedgerMutation().GetAutofillFee(probe, rpcCtx.Role.IsUnlimited(), feeOpts.Mult, feeOpts.Div)
			if feeErr != nil {
				var hfe *svcerr.HighFeeError
				if errors.As(feeErr, &hfe) {
					return nil, rpcerrors.RpcErrorHighFee(hfe.Error())
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
			return nil, rpcerrors.RpcErrorMissingField("tx_json.Sequence")
		}
		if _, ok := txMap["Fee"]; !ok {
			return nil, rpcerrors.RpcErrorMissingField("tx_json.Fee")
		}
	}
	if rpcErr := checkPayment(txMap, rawParams, !offline, rpcCtx); rpcErr != nil {
		return nil, rpcErr
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
				return nil, rpcerrors.RpcErrorSrcActMalformed("Invalid field 'tx_json.Delegate'.")
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
		var rpcErr *rpcerrors.RpcError
		targetObj, rpcErr = signatureTargetObject(txMap, signatureTarget)
		if rpcErr != nil {
			return nil, rpcErr
		}
		targetObj["SigningPubKey"] = publicKey
	}

	transaction, rpcErr := preprocessTransaction(txMap, transactionPreprocessOptions{mode: transactionPreprocessSign})
	if rpcErr != nil {
		return nil, rpcErr
	}

	signature, err := sign.SignTransaction(transaction, privateKey)
	if err != nil {
		return nil, rpcInternalError("sign: transaction signing failed", err)
	}

	if !signatureTargetPresent {
		transaction.GetCommon().TxnSignature = signature
	} else {
		if transaction.GetCommon().CounterpartySignature == nil {
			return nil, rpcInternalInvariantError("sign: counterparty signature target unavailable")
		}
		transaction.GetCommon().CounterpartySignature.TxnSignature = signature
	}

	canonicalMap, err := flattenCanonicalTransaction(transaction, txMap)
	if err != nil {
		return nil, rpcInternalError("sign: transaction flattening failed", err)
	}
	normalizeSignerResponseContainers(canonicalMap)
	txBlob, err := binarycodec.Encode(canonicalMap)
	if err != nil {
		return nil, rpcInternalError("sign: transaction encoding failed", err)
	}

	txHash := CalculateTxHash(txBlob)
	canonicalMap["hash"] = txHash

	return &signResult{
		TxMap:  canonicalMap,
		TxBlob: txBlob,
	}, nil
}

func authorizeSigningKey(ctx *types.RpcContext, account, derivedAccount string, requireAccount bool) *rpcerrors.RpcError {
	var accountInfo *types.AccountInfo
	if ctx != nil && ctx.Services != nil && ctx.Services.Ledger() != nil {
		info, err := ctx.Services.Ledger().GetAccountInfo(ctx.Context, account, "current")
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

func signingKeyAuthorization(account, derivedAccount string, accountInfo *types.AccountInfo, requireAccount bool) *rpcerrors.RpcError {
	if accountInfo == nil {
		if requireAccount {
			return rpcerrors.RpcErrorDelegateActNotFound()
		}
		if derivedAccount == account {
			return nil
		}
		return rpcerrors.RpcErrorBadSecret()
	}
	if derivedAccount == account {
		if accountInfo.Flags&entry.LsfDisableMaster != 0 {
			return rpcerrors.RpcErrorMasterDisabled()
		}
		return nil
	}
	if accountInfo.RegularKey == derivedAccount {
		return nil
	}
	return rpcerrors.RpcErrorBadSecret()
}

func rejectSigningWhenLoaded(services *types.ServiceGraph, unlimited bool) *rpcerrors.RpcError {
	if unlimited || services == nil || services.IsLoadedCluster() == nil || !services.IsLoadedCluster()() {
		return nil
	}
	return rpcerrors.RpcErrorTooBusy()
}

func rejectOnlineSigningWithoutCurrentLedger(services *types.ServiceGraph, offline bool, apiVersion int) *rpcerrors.RpcError {
	if offline || services == nil || services.Ledger() == nil {
		return nil
	}
	info := services.Ledger().GetServerInfo()
	if info.Standalone || !types.ValidatedLedgerStale(info) {
		return nil
	}
	return types.CurrentLedgerUnavailable(apiVersion)
}

func signatureTargetObject(txMap map[string]any, target string) (map[string]any, *rpcerrors.RpcError) {
	value, exists := txMap[target]
	if !exists {
		object := make(map[string]any)
		txMap[target] = object
		return object, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid field 'tx_json.%s'.", target))
	}
	return object, nil
}

func parseTransactionForSigning(txMap map[string]any) (tx.Transaction, *rpcerrors.RpcError) {
	blob, err := binarycodec.EncodeBytes(txMap)
	if err != nil {
		return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Failed to parse transaction: %v", err))
	}
	transaction, err := tx.ParseFromBinary(blob)
	if err != nil {
		return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Failed to parse transaction: %v", err))
	}
	return transaction, nil
}
