package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
)

// SignForMethod handles the sign_for RPC method
// This adds a signature to a transaction for multi-signing
type SignForMethod struct{ baseHandler }

func (m *SignForMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (result any, rpcErr *types.RpcError) {
	if rpcErr := rejectDisabledSigning(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	defer func() {
		result, rpcErr = addSigningDeprecation(result, rpcErr)
	}()

	setLoadHeavy(ctx)
	var request struct {
		signingRequest
		Account string `json:"account"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	// Validate the signer's account before tx_json, matching rippled's
	// transactionSignFor order. An unparseable account is srcActMalformed
	// ("Invalid field 'account'."), not the generic actMalformed.
	if request.Account == "" {
		return nil, types.RpcErrorMissingField("account")
	}
	if !addresscodec.IsValidClassicAddress(request.Account) {
		return nil, types.RpcErrorSrcActMalformed("Invalid field 'account'.")
	}

	if len(request.TxJson) == 0 {
		return nil, types.RpcErrorMissingField("tx_json")
	}

	signatureTargetPresent := jsonFieldPresent(params, "signature_target")
	var txMap map[string]any
	decoder := json.NewDecoder(bytes.NewReader(request.TxJson))
	decoder.UseNumber()
	if err := decoder.Decode(&txMap); err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid tx_json: %v", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, types.RpcErrorInvalidParams("Invalid tx_json: expected object")
	}
	if txMap == nil {
		return nil, types.RpcErrorExpectedField("tx_json", "object")
	}

	// On networks with ID > 1024, sign_for requires tx_json to carry a matching
	// integral NetworkID, else invalidParams. Unlike sign/submit — which autofill
	// a missing NetworkID — sign_for rejects, so a multisigner cannot sign for the
	// wrong network. Mirrors rippled checkNetworkID in transactionSignFor.
	if ctx.Services != nil && ctx.Services.Ledger() != nil {
		if networkID := ctx.Services.Ledger().GetServerInfo().NetworkID; networkID > 1024 {
			v, ok := txMap["NetworkID"]
			if !ok {
				return nil, types.RpcErrorMissingField("tx_json.NetworkID")
			}
			if n, ok := integralNetworkID(v); !ok || n != networkID {
				return nil, types.RpcErrorInvalidField("tx_json.NetworkID")
			}
		}
	}

	// Supply the required key field when absent. A signature target preserves an
	// existing primary key; ordinary multi-signing requires the field to be empty.
	if _, ok := txMap["SigningPubKey"]; !ok {
		txMap["SigningPubKey"] = ""
	}
	if _, ok := txMap["Sequence"]; !ok {
		return nil, types.RpcErrorMissingField("tx_json.Sequence")
	}
	if !signatureTargetPresent {
		signingPubKey, ok := txMap["SigningPubKey"].(string)
		if !ok || signingPubKey != "" {
			return nil, types.RpcErrorInvalidParams(
				"When multi-signing 'tx_json.SigningPubKey' must be empty.")
		}
	}

	privateKey, publicKey, _, rpcErr := request.signCredentials.deriveKeypair(ctx.ApiVersion, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if signatureTargetPresent && request.SignatureTarget != counterpartySignatureField {
		return nil, types.RpcErrorInvalidParams(request.SignatureTarget)
	}
	if rpcErr := validateSigningTxJSONShape(txMap); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := rejectOnlineSigningWithoutCurrentLedger(ctx.Services, request.Offline, ctx.ApiVersion); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := rejectSigningWhenLoaded(ctx.Services, ctx.Role.IsUnlimited()); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := validateSignForPreConflict(txMap, params); rpcErr != nil {
		return nil, rpcErr
	}
	if _, ok := txMap["TxnSignature"]; ok {
		return nil, rpcErrorAlreadySingleSigned()
	}

	if signatureTargetPresent {
		nested, targetErr := signatureTargetObject(txMap, request.SignatureTarget)
		if targetErr != nil {
			return nil, targetErr
		}
		nested["SigningPubKey"] = ""
	}
	transaction, rpcErr := preprocessTransaction(txMap, transactionPreprocessOptions{
		mode:            transactionPreprocessSignFor,
		preserveSigners: true,
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	feePayer := transaction.GetCommon().Account
	if transaction.GetCommon().Delegate != "" {
		feePayer = transaction.GetCommon().Delegate
	}
	derivedAccount, derivationErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKey)
	if derivationErr != nil {
		return nil, rpcInternalError("sign_for: account address derivation failed", derivationErr)
	}
	if rpcErr := authorizeSigningKey(ctx, request.Account, derivedAccount, false); rpcErr != nil {
		return nil, rpcErr
	}

	common := transaction.GetCommon()
	signers := common.Signers
	if signatureTargetPresent {
		if common.CounterpartySignature == nil {
			return nil, types.RpcErrorInvalidParams("Invalid field 'tx_json.CounterpartySignature'.")
		}
		signers = common.CounterpartySignature.Signers
	}
	canonicalSigners, signerErr := normalizeTypedSigners(signers, feePayer)
	if signerErr != nil {
		return nil, signerErr
	}

	// Sign the canonical transaction representation. The signature target only
	// changes the multisigning preimage; the parsed transaction remains the
	// object that receives the new signer and is flattened below.
	var signature string
	var err error
	if signatureTargetPresent {
		signature, err = sign.SignTransactionForMultiSignTarget(transaction, request.Account, privateKey)
	} else {
		signature, err = sign.SignTransactionForMultiSign(transaction, request.Account, privateKey)
	}
	if err != nil {
		return nil, rpcInternalError("sign_for: multisigning payload signing failed", err)
	}

	canonicalAccount, accountErr := canonicalAccountID(request.Account)
	if accountErr != nil {
		return nil, types.RpcErrorInvalidParams("Invalid field 'account'.")
	}
	canonicalSigners = append(canonicalSigners, tx.SignerWrapper{Signer: tx.Signer{
		Account:       canonicalAccount,
		SigningPubKey: publicKey,
		TxnSignature:  signature,
	}})
	canonicalSigners, signerErr = normalizeTypedSigners(canonicalSigners, feePayer)
	if signerErr != nil {
		return nil, signerErr
	}

	if signatureTargetPresent {
		common.CounterpartySignature.Signers = canonicalSigners
	} else {
		common.Signers = canonicalSigners
	}

	canonicalMap, err := flattenCanonicalTransaction(transaction, txMap)
	if err != nil {
		return nil, rpcInternalError("sign_for: transaction flattening failed", err)
	}
	normalizeSignerResponseContainers(canonicalMap)
	txBlob, err := binarycodec.Encode(canonicalMap)
	if err != nil {
		return nil, rpcInternalError("sign_for: transaction encoding failed", err)
	}

	txHash := CalculateTxHash(txBlob)
	canonicalMap["hash"] = txHash

	return formatSignResult(signResult{TxMap: canonicalMap, TxBlob: txBlob}, ctx.ApiVersion), nil
}

func integralNetworkID(value any) (uint32, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseUint(string(number), 10, 32)
		return uint32(parsed), err == nil
	case float64:
		if number < 0 || number > math.MaxUint32 || number != math.Trunc(number) {
			return 0, false
		}
		return uint32(number), true
	case uint32:
		return number, true
	case uint64:
		if number > math.MaxUint32 {
			return 0, false
		}
		return uint32(number), true
	case int:
		if number < 0 {
			return 0, false
		}
		return uint32(number), uint64(number) <= math.MaxUint32
	case int64:
		if number < 0 || uint64(number) > math.MaxUint32 {
			return 0, false
		}
		return uint32(number), true
	default:
		return 0, false
	}
}

func validateSignForPreConflict(txMap map[string]any, params json.RawMessage) *types.RpcError {
	if _, ok := txMap["Fee"]; !ok {
		return types.RpcErrorMissingField("tx_json.Fee")
	}
	transactionType := txMap["TransactionType"]
	if transactionType != "Payment" {
		return nil
	}
	return checkPayment(txMap, params, false, nil)
}

func validateSigningTxJSONShape(txMap map[string]any) *types.RpcError {
	if _, ok := txMap["TransactionType"]; !ok {
		return types.RpcErrorMissingField("tx_json.TransactionType")
	}
	account, ok := txMap["Account"].(string)
	if !ok || !addresscodec.IsValidClassicAddress(account) {
		if _, present := txMap["Account"]; !present {
			return types.RpcErrorSrcActMissing("Missing field 'tx_json.Account'.")
		}
		return types.RpcErrorSrcActMalformed("Invalid field 'tx_json.Account'.")
	}
	return nil
}

func normalizeSigners(value any, feePayer string) ([]map[string]any, *types.RpcError) {
	const malformedSigners = "Signers array may only contain Signer entries."

	var values []any
	switch signers := value.(type) {
	case []any:
		values = signers
	case []map[string]any:
		values = make([]any, len(signers))
		for i := range signers {
			values[i] = signers[i]
		}
	default:
		return nil, types.RpcErrorInvalidParams(malformedSigners)
	}

	type signerEntry struct {
		wrapper map[string]any
		account string
		id      []byte
	}
	entries := make([]signerEntry, 0, len(values))
	for _, value := range values {
		wrapper, ok := value.(map[string]any)
		if !ok || len(wrapper) != 1 {
			return nil, types.RpcErrorInvalidParams(malformedSigners)
		}
		signer, ok := wrapper["Signer"].(map[string]any)
		if !ok || len(signer) != 3 {
			return nil, types.RpcErrorInvalidParams(malformedSigners)
		}
		for _, field := range []string{"Account", "SigningPubKey", "TxnSignature"} {
			if _, ok := signer[field].(string); !ok {
				return nil, types.RpcErrorInvalidParams(malformedSigners)
			}
		}
		account, ok := signer["Account"].(string)
		if !ok {
			return nil, types.RpcErrorInvalidParams(malformedSigners)
		}
		canonicalAccount, err := canonicalAccountID(account)
		if err != nil {
			return nil, types.RpcErrorInvalidParams(malformedSigners)
		}
		_, id, err := addresscodec.DecodeClassicAddressToAccountID(canonicalAccount)
		if err != nil {
			return nil, types.RpcErrorInvalidParams(malformedSigners)
		}
		signer["Account"] = canonicalAccount
		entries = append(entries, signerEntry{wrapper: wrapper, account: canonicalAccount, id: id})
	}

	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].id, entries[j].id) < 0
	})
	for i := 1; i < len(entries); i++ {
		if bytes.Equal(entries[i-1].id, entries[i].id) {
			return nil, types.RpcErrorInvalidParams(
				"Duplicate Signers:Signer:Account entries (" + entries[i].account + ") are not allowed.")
		}
	}

	canonicalFeePayer, err := canonicalAccountID(feePayer)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid field 'tx_json.Account'.")
	}
	_, feePayerID, err := addresscodec.DecodeClassicAddressToAccountID(canonicalFeePayer)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid field 'tx_json.Account'.")
	}
	result := make([]map[string]any, len(entries))
	for i, entry := range entries {
		if bytes.Equal(entry.id, feePayerID) {
			return nil, types.RpcErrorInvalidParams(
				"A Signer may not be the transaction's Account (" + feePayer + ").")
		}
		result[i] = entry.wrapper
	}
	return result, nil
}

func normalizeTypedSigners(signers []tx.SignerWrapper, feePayer string) ([]tx.SignerWrapper, *types.RpcError) {
	value := make([]any, len(signers))
	for i, signer := range signers {
		value[i] = map[string]any{"Signer": map[string]any{
			"Account":       signer.Signer.Account,
			"SigningPubKey": signer.Signer.SigningPubKey,
			"TxnSignature":  signer.Signer.TxnSignature,
		}}
	}
	normalized, rpcErr := normalizeSigners(value, feePayer)
	if rpcErr != nil {
		return nil, rpcErr
	}
	result := make([]tx.SignerWrapper, len(normalized))
	for i, wrapper := range normalized {
		inner := wrapper["Signer"].(map[string]any)
		result[i] = tx.SignerWrapper{Signer: tx.Signer{
			Account:       inner["Account"].(string),
			SigningPubKey: inner["SigningPubKey"].(string),
			TxnSignature:  inner["TxnSignature"].(string),
		}}
	}
	return result, nil
}

func (m *SignForMethod) RequiredRole() types.Role {
	return types.RoleUser
}
