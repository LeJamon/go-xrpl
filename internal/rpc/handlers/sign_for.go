package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// SignForMethod handles the sign_for RPC method
// This adds a signature to a transaction for multi-signing
type SignForMethod struct{ BaseHandler }

func (m *SignForMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	var request struct {
		signingRequest
		Account string `json:"account"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	// Validate the signer's account before tx_json, matching rippled's
	// transactionSignFor order. An unparseable account is srcActMalformed
	// ("Invalid field 'account'."), not the generic actMalformed.
	if request.Account == "" {
		return nil, types.RPCErrorMissingField("account")
	}
	if !addresscodec.IsValidClassicAddress(request.Account) {
		return nil, types.RPCErrorSrcActMalformed("Invalid field 'account'.")
	}

	if len(request.TxJson) == 0 {
		return nil, types.RPCErrorMissingField("tx_json")
	}

	// signature_target directs the multi-signer into a nested inner object.
	// Only CounterpartySignature is a valid target; any other name is rejected
	// with the field name as the message, matching rippled TransactionSign.cpp.
	if request.SignatureTarget != "" && request.SignatureTarget != counterpartySignatureField {
		return nil, types.RPCErrorInvalidParams(request.SignatureTarget)
	}

	// Parse credentials and derive keypair using the shared helper
	privateKey, publicKey, rpcErr := request.signCredentials.deriveKeypair(ctx.ApiVersion)
	if rpcErr != nil {
		return nil, rpcErr
	}

	// Determine key type for signing (needed by signPayload)
	keyType := strings.ToLower(request.KeyType)
	if keyType == "" {
		keyType = "secp256k1"
	}

	// Parse the transaction JSON
	var txMap map[string]any
	if err := json.Unmarshal(request.TxJson, &txMap); err != nil {
		return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid tx_json: %v", err))
	}

	// Verify that Account field exists in transaction
	if _, ok := txMap["Account"]; !ok {
		return nil, types.RPCErrorMissingField("Account")
	}

	// On networks with ID > 1024, sign_for requires tx_json to carry a matching
	// integral NetworkID, else invalidParams. Unlike sign/submit — which autofill
	// a missing NetworkID — sign_for rejects, so a multisigner cannot sign for the
	// wrong network. Mirrors rippled checkNetworkID in transactionSignFor.
	if ctx.Services != nil && ctx.Services.Ledger != nil {
		if networkID := ctx.Services.Ledger.GetServerInfo().NetworkID; networkID > 1024 {
			v, ok := txMap["NetworkID"]
			if !ok {
				return nil, types.RPCErrorMissingField("tx_json.NetworkID")
			}
			n, ok := v.(float64)
			if !ok || n != math.Trunc(n) || n < 0 || uint32(n) != networkID {
				return nil, types.RPCErrorInvalidField("tx_json.NetworkID")
			}
		}
	}

	// For multi-signing, the top-level SigningPubKey must be empty. With a
	// signature_target the signature goes into a nested object, so an existing
	// top-level SigningPubKey (the primary signer's) is preserved.
	if request.SignatureTarget == "" {
		txMap["SigningPubKey"] = ""
	} else if _, ok := txMap["SigningPubKey"]; !ok {
		txMap["SigningPubKey"] = ""
	}

	// sigContainer holds the Signers array the new signature is appended to:
	// the transaction itself, or the nested CounterpartySignature object.
	sigContainer := txMap
	if request.SignatureTarget != "" {
		nested, _ := txMap[request.SignatureTarget].(map[string]any)
		if nested == nil {
			nested = map[string]any{"SigningPubKey": ""}
		}
		txMap[request.SignatureTarget] = nested
		sigContainer = nested
	}

	// Get existing signers array or create new one
	var signers []map[string]any
	if existingSigners, ok := sigContainer["Signers"].([]any); ok {
		for _, s := range existingSigners {
			if signer, ok := s.(map[string]any); ok {
				signers = append(signers, signer)
			}
		}
	}

	for _, signerWrapper := range signers {
		if signer, ok := signerWrapper["Signer"].(map[string]any); ok {
			if signer["Account"] == request.Account {
				return nil, types.RPCErrorInvalidParams("Account has already signed this transaction")
			}
		}
	}

	txMapForSigning := make(map[string]any)
	for k, v := range txMap {
		if k != "Signers" {
			txMapForSigning[k] = v
		}
	}

	// Encode for multisigning (adds the signer's account as suffix)
	signingPayload, err := binarycodec.EncodeForMultisigning(txMapForSigning, request.Account)
	if err != nil {
		if e := arraySizeRPCError(err); e != nil {
			return nil, e
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to encode for multisigning: %v", err))
	}

	// Sign the payload
	signature, err := signPayload(signingPayload, privateKey, keyType)
	if err != nil {
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to sign transaction: %v", err))
	}

	newSigner := map[string]any{
		"Signer": map[string]any{
			"Account":       request.Account,
			"SigningPubKey": publicKey,
			"TxnSignature":  signature,
		},
	}

	signers = append(signers, newSigner)

	// Sort signers by account (required by XRPL protocol)
	sort.Slice(signers, func(i, j int) bool {
		iAccount := ""
		jAccount := ""
		if s, ok := signers[i]["Signer"].(map[string]any); ok {
			iAccount, _ = s["Account"].(string)
		}
		if s, ok := signers[j]["Signer"].(map[string]any); ok {
			jAccount, _ = s["Account"].(string)
		}
		return iAccount < jAccount
	})

	sigContainer["Signers"] = signers

	txBlob, err := binarycodec.Encode(txMap)
	if err != nil {
		if e := arraySizeRPCError(err); e != nil {
			return nil, e
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to encode transaction: %v", err))
	}

	txHash := CalculateTxHash(txBlob)
	txMap["hash"] = txHash

	return formatSignResult(signResult{TxMap: txMap, TxBlob: txBlob}, ctx.ApiVersion), nil
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
