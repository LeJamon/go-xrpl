package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// SignForMethod handles the sign_for RPC method
// This adds a signature to a transaction for multi-signing
type SignForMethod struct{ BaseHandler }

func (m *SignForMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
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
	if err := json.Unmarshal(request.TxJson, &txMap); err != nil {
		return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid tx_json: %v", err))
	}
	if txMap == nil {
		return nil, types.RpcErrorExpectedField("tx_json", "object")
	}

	// On networks with ID > 1024, sign_for requires tx_json to carry a matching
	// integral NetworkID, else invalidParams. Unlike sign/submit — which autofill
	// a missing NetworkID — sign_for rejects, so a multisigner cannot sign for the
	// wrong network. Mirrors rippled checkNetworkID in transactionSignFor.
	if ctx.Services != nil && ctx.Services.Ledger != nil {
		if networkID := ctx.Services.Ledger.GetServerInfo().NetworkID; networkID > 1024 {
			v, ok := txMap["NetworkID"]
			if !ok {
				return nil, types.RpcErrorMissingField("tx_json.NetworkID")
			}
			n, ok := v.(float64)
			if !ok || n != math.Trunc(n) || n < 0 || uint32(n) != networkID {
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

	privateKey, publicKey, keyType, rpcErr := request.signCredentials.deriveKeypair(ctx.ApiVersion, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if signatureTargetPresent && request.SignatureTarget != counterpartySignatureField {
		return nil, types.RpcErrorInvalidParams(request.SignatureTarget)
	}
	if rpcErr := validateSigningTxJSONShape(txMap); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := rejectSigningWhenLoaded(ctx.Services, ctx.Unlimited); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := validateSignForPreConflict(txMap, params); rpcErr != nil {
		return nil, rpcErr
	}
	if _, ok := txMap["TxnSignature"]; ok {
		return nil, rpcErrorAlreadySingleSigned()
	}

	// sigContainer holds the Signers array the new signature is appended to:
	// the transaction itself, or the nested CounterpartySignature object.
	sigContainer := txMap
	if signatureTargetPresent {
		nested, targetErr := signatureTargetObject(txMap, request.SignatureTarget)
		if targetErr != nil {
			return nil, targetErr
		}
		nested["SigningPubKey"] = ""
		sigContainer = nested
	}
	transaction, rpcErr := parseTransactionForSigning(txMap)
	if rpcErr != nil {
		return nil, rpcErr
	}

	signers := make([]map[string]any, 0, 1)
	if existing, ok := sigContainer["Signers"]; ok {
		var signerErr *types.RpcError
		signers, signerErr = signerMaps(existing)
		if signerErr != nil {
			return nil, signerErr
		}
	}

	txMapForSigning := make(map[string]any)
	for k, v := range txMap {
		if k != "Signers" {
			txMapForSigning[k] = v
		}
	}

	// Encode for multisigning (adds the signer's account as suffix)
	var signingPayload string
	var err error
	if signatureTargetPresent {
		signingPayload, err = binarycodec.EncodeForMultisigningTarget(txMapForSigning, request.Account)
	} else {
		signingPayload, err = binarycodec.EncodeForMultisigning(txMapForSigning, request.Account)
	}
	if err != nil {
		return nil, rpcInternalError("sign_for: multisigning payload encoding failed", err)
	}

	// Sign the payload
	signature, err := signPayload(signingPayload, privateKey, keyType)
	if err != nil {
		return nil, rpcInternalError("sign_for: transaction signing failed", err)
	}

	newSigner := map[string]any{
		"Signer": map[string]any{
			"Account":       request.Account,
			"SigningPubKey": publicKey,
			"TxnSignature":  signature,
		},
	}

	signers = append(signers, newSigner)
	feePayer := transaction.GetCommon().Account
	if transaction.GetCommon().Delegate != "" {
		feePayer = transaction.GetCommon().Delegate
	}
	if signerErr := sortAndValidateSignerMaps(signers, feePayer); signerErr != nil {
		return nil, signerErr
	}

	sigContainer["Signers"] = signers

	txBlob, err := binarycodec.Encode(txMap)
	if err != nil {
		return nil, rpcInternalError("sign_for: transaction encoding failed", err)
	}

	txHash := CalculateTxHash(txBlob)
	txMap["hash"] = txHash

	return formatSignResult(signResult{TxMap: txMap, TxBlob: txBlob}, ctx.ApiVersion), nil
}

func validateSignForPreConflict(txMap map[string]any, params json.RawMessage) *types.RpcError {
	if _, ok := txMap["Fee"]; !ok {
		return types.RpcErrorMissingField("tx_json.Fee")
	}
	transactionType := txMap["TransactionType"]
	if transactionType != "Payment" {
		return nil
	}

	if deliverMax, ok := txMap["DeliverMax"]; ok {
		if amount, present := txMap["Amount"]; present && !reflect.DeepEqual(amount, deliverMax) {
			return types.RpcErrorInvalidParams("Cannot specify differing 'Amount' and 'DeliverMax'")
		}
		if _, present := txMap["Amount"]; !present {
			txMap["Amount"] = deliverMax
		}
		delete(txMap, "DeliverMax")
	}
	amount, ok := txMap["Amount"]
	if !ok {
		return types.RpcErrorMissingField("tx_json.Amount")
	}
	encodedAmount, err := json.Marshal(amount)
	if err != nil {
		return types.RpcErrorInvalidField("tx_json.Amount")
	}
	if _, err := state.AmountFromJSON(encodedAmount); err != nil {
		return types.RpcErrorInvalidField("tx_json.Amount")
	}
	destination, ok := txMap["Destination"].(string)
	if !ok || !addresscodec.IsValidClassicAddress(destination) {
		if _, present := txMap["Destination"]; !present {
			return types.RpcErrorMissingField("tx_json.Destination")
		}
		return types.RpcErrorInvalidField("tx_json.Destination")
	}
	if jsonFieldPresent(params, "build_path") {
		return types.RpcErrorInvalidParams("Field 'build_path' not allowed in this context.")
	}
	if domain, ok := txMap["DomainID"]; ok {
		domainString, ok := domain.(string)
		decoded, err := hex.DecodeString(domainString)
		if !ok || err != nil || len(decoded) != 32 {
			return types.RpcErrorDomainMalformed("Unable to parse 'DomainID'.")
		}
	}
	return nil
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

func signerMaps(value any) ([]map[string]any, *types.RpcError) {
	switch signers := value.(type) {
	case []any:
		result := make([]map[string]any, 0, len(signers))
		for _, value := range signers {
			signer, ok := value.(map[string]any)
			if !ok {
				return nil, types.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
			}
			result = append(result, signer)
		}
		return result, nil
	case []map[string]any:
		return append([]map[string]any(nil), signers...), nil
	default:
		return nil, types.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
	}
}

func sortAndValidateSignerMaps(signers []map[string]any, feePayer string) *types.RpcError {
	type signerEntry struct {
		wrapper map[string]any
		account string
		id      []byte
	}
	entries := make([]signerEntry, 0, len(signers))
	for _, wrapper := range signers {
		signer, ok := wrapper["Signer"].(map[string]any)
		if !ok {
			return types.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
		}
		account, ok := signer["Account"].(string)
		if !ok {
			return types.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
		}
		_, id, err := addresscodec.DecodeClassicAddressToAccountID(account)
		if err != nil {
			return types.RpcErrorInvalidParams("Signers array may only contain Signer entries.")
		}
		entries = append(entries, signerEntry{wrapper: wrapper, account: account, id: id})
	}

	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].id, entries[j].id) < 0
	})
	for i := 1; i < len(entries); i++ {
		if bytes.Equal(entries[i-1].id, entries[i].id) {
			return types.RpcErrorInvalidParams(
				"Duplicate Signers:Signer:Account entries (" + entries[i].account + ") are not allowed.")
		}
	}

	_, feePayerID, err := addresscodec.DecodeClassicAddressToAccountID(feePayer)
	if err != nil {
		return types.RpcErrorInvalidParams("Invalid field 'tx_json.Account'.")
	}
	for i, entry := range entries {
		if bytes.Equal(entry.id, feePayerID) {
			return types.RpcErrorInvalidParams(
				"A Signer may not be the transaction's Account (" + feePayer + ").")
		}
		signers[i] = entry.wrapper
	}
	return nil
}

// signPayload signs a hex-encoded payload with the given private key
func signPayload(payloadHex string, privateKeyHex string, keyType string) (string, error) {
	// Decode the payload
	payloadBytes, err := hex.DecodeString(payloadHex)
	if err != nil {
		return "", err
	}

	// Convert to string for crypto functions
	payloadStr := string(payloadBytes)

	var signature string

	if keyType == "ed25519" {
		algo := ed25519.Algorithm{}
		signature, err = algo.Sign(payloadStr, privateKeyHex)
	} else {
		algo := secp256k1.Algorithm{}
		signature, err = algo.Sign(payloadStr, privateKeyHex)
	}

	if err != nil {
		return "", err
	}

	return strings.ToUpper(signature), nil
}

func (m *SignForMethod) RequiredRole() types.Role {
	return types.RoleUser
}
