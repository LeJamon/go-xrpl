package handlers

import (
	"encoding/json"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/internal/tx"
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

// signResult holds the output of the signing operation.
type signResult struct {
	TxMap  map[string]interface{} // The transaction JSON map with SigningPubKey, TxnSignature, and hash
	TxBlob string                 // The hex-encoded signed transaction blob
}

// signTransactionJSON takes a raw tx_json and signing credentials, derives the
// keypair, auto-fills missing fields (unless offline), signs the transaction,
// and returns the signed tx map + blob. This is the shared logic used by both
// the "sign" and "submit" RPC methods.
func signTransactionJSON(txJSON json.RawMessage, creds signCredentials, offline bool, apiVersion int) (*signResult, *types.RpcError) {
	// Check if ledger service is available (needed for auto-filling fields)
	if !offline && (types.Services == nil || types.Services.Ledger == nil) {
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
		return nil, types.RpcErrorInternal("Failed to derive address: " + err.Error())
	}

	// Parse the transaction JSON
	var txMap map[string]interface{}
	if err := json.Unmarshal(txJSON, &txMap); err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid tx_json: " + err.Error())
	}

	// Verify the account matches the signing key
	if txAccount, ok := txMap["Account"].(string); ok {
		if txAccount != address {
			return nil, types.RpcErrorInvalidParams("Account in tx_json does not match signing key")
		}
	} else {
		// Set the account if not present
		txMap["Account"] = address
	}

	// Fill in missing fields if not offline
	if !offline {
		// Set Fee if not present
		if _, ok := txMap["Fee"]; !ok {
			baseFee, _, _ := types.Services.Ledger.GetCurrentFees()
			txMap["Fee"] = formatUint64AsString(baseFee)
		}

		// Set Sequence if not present
		if _, ok := txMap["Sequence"]; !ok {
			// Get account info to find current sequence
			info, err := types.Services.Ledger.GetAccountInfo(address, "current")
			if err != nil {
				return nil, types.RpcErrorInternal("Failed to get account sequence: " + err.Error())
			}
			txMap["Sequence"] = info.Sequence
		}

		// Set LastLedgerSequence if not present (current + 4)
		if _, ok := txMap["LastLedgerSequence"]; !ok {
			currentLedger := types.Services.Ledger.GetCurrentLedgerIndex()
			txMap["LastLedgerSequence"] = currentLedger + 4
		}
	}

	// Add the signing public key
	txMap["SigningPubKey"] = publicKey

	// Parse the transaction to get a proper Transaction object
	txBytes, err := json.Marshal(txMap)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to marshal transaction: " + err.Error())
	}

	transaction, err := tx.ParseJSON(txBytes)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Failed to parse transaction: " + err.Error())
	}

	// Update the common fields with signing key
	txCommon := transaction.GetCommon()
	txCommon.SigningPubKey = publicKey

	// Sign the transaction
	signature, err := tx.SignTransaction(transaction, privateKey)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to sign transaction: " + err.Error())
	}

	// Add signature to transaction map
	txMap["TxnSignature"] = signature

	// Encode the transaction to binary
	txBlob, err := binarycodec.Encode(txMap)
	if err != nil {
		return nil, types.RpcErrorInternal("Failed to encode transaction: " + err.Error())
	}

	// Calculate transaction hash
	txHash := CalculateTxHash(txBlob)

	// Add hash to response tx_json
	txMap["hash"] = txHash

	return &signResult{
		TxMap:  txMap,
		TxBlob: txBlob,
	}, nil
}
