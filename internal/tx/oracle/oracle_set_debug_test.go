package oracle_test

import (
	"encoding/json"
	"fmt"
	"testing"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
	_ "github.com/LeJamon/goXRPLd/internal/tx/all"
)

func TestOracleSetRoundtrip(t *testing.T) {
	txJSON := `{
		"TransactionType": "OracleSet",
		"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"OracleDocumentID": 1,
		"Provider": "70726F7669646572",
		"AssetClass": "63757272656E6379",
		"LastUpdateTime": 1234567890,
		"Fee": "10",
		"Sequence": 1,
		"SigningPubKey": "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020",
		"PriceDataSeries": [
			{
				"PriceData": {
					"BaseAsset": "XRP",
					"QuoteAsset": "USD",
					"AssetPrice": 740,
					"Scale": 3
				}
			}
		]
	}`

	// Step 1: Parse JSON to OracleSet transaction
	transaction, err := tx.ParseJSON([]byte(txJSON))
	if err != nil {
		t.Fatalf("ParseJSON error: %v", err)
	}
	fmt.Printf("Transaction type: %s\n", transaction.TxType().String())

	// Step 2: Flatten
	txMap, err := transaction.Flatten()
	if err != nil {
		t.Fatalf("Flatten error: %v", err)
	}

	flatJSON, _ := json.MarshalIndent(txMap, "", "  ")
	fmt.Printf("Flattened: %s\n", flatJSON)

	// Step 3: EncodeForSigning
	txMapCopy := make(map[string]any)
	for k, v := range txMap {
		txMapCopy[k] = v
	}
	sigPayload, err := binarycodec.EncodeForSigning(txMapCopy)
	if err != nil {
		t.Fatalf("EncodeForSigning error: %v", err)
	}
	fmt.Printf("Signing payload length: %d\n", len(sigPayload))

	// Step 4: Encode full tx
	txMap2, _ := transaction.Flatten()
	txMap2["TxnSignature"] = "304402200102030405060708091011121314151617181920212223242526272829303132022033343536373839404142434445464748495051525354555657585960616263"
	blob, err := binarycodec.Encode(txMap2)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	fmt.Printf("Encoded blob length: %d\n", len(blob))

	fmt.Println("\nSUCCESS: Full round-trip worked!")
}

func TestOracleSetSignAndSubmitFlow(t *testing.T) {
	// Simulate the EXACT flow that sign-and-submit does:
	// 1. JSON from RPC → json.Unmarshal → map[string]interface{} (with float64 values)
	// 2. Add auto-fill fields to the map
	// 3. json.Marshal(txMap) → tx.ParseJSON → Transaction object (for signing)
	// 4. tx.SignTransaction → calls Flatten() → EncodeForSigning
	// 5. binarycodec.Encode(txMap) — the ORIGINAL map, NOT flattened

	// Step 1: Simulate RPC input
	rpcJSON := `{
		"TransactionType": "OracleSet",
		"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"OracleDocumentID": 1,
		"Provider": "70726F7669646572",
		"AssetClass": "63757272656E6379",
		"LastUpdateTime": 1234567890,
		"PriceDataSeries": [
			{
				"PriceData": {
					"BaseAsset": "XRP",
					"QuoteAsset": "USD",
					"AssetPrice": 740,
					"Scale": 3
				}
			}
		]
	}`

	var txMap map[string]interface{}
	if err := json.Unmarshal([]byte(rpcJSON), &txMap); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	// Step 2: Auto-fill
	txMap["Fee"] = "10"
	txMap["Sequence"] = float64(1) // json.Unmarshal produces float64
	txMap["LastLedgerSequence"] = float64(100)
	txMap["SigningPubKey"] = "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020"

	// Step 3: Parse the transaction
	txBytes, err := json.Marshal(txMap)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	fmt.Printf("txBytes for ParseJSON: %s\n\n", string(txBytes))

	transaction, err := tx.ParseJSON(txBytes)
	if err != nil {
		t.Fatalf("ParseJSON error: %v", err)
	}
	fmt.Printf("Parsed tx type: %s\n", transaction.TxType().String())

	// Step 4: Flatten + EncodeForSigning (this is what SignTransaction does internally)
	flatMap, err := transaction.Flatten()
	if err != nil {
		t.Fatalf("Flatten error: %v", err)
	}
	flatJSON, _ := json.MarshalIndent(flatMap, "", "  ")
	fmt.Printf("Flattened for signing: %s\n", flatJSON)

	sigPayload, err := binarycodec.EncodeForSigning(flatMap)
	if err != nil {
		t.Fatalf("EncodeForSigning error: %v", err)
	}
	fmt.Printf("Signing payload length: %d\n", len(sigPayload))

	// Step 5: Add fake signature and encode the ORIGINAL txMap
	txMap["TxnSignature"] = "304402200102030405060708091011121314151617181920212223242526272829303132022033343536373839404142434445464748495051525354555657585960616263"

	// Print types of values in PriceDataSeries in the map
	pds := txMap["PriceDataSeries"]
	fmt.Printf("\ntxMap PriceDataSeries type: %T\n", pds)
	if arr, ok := pds.([]interface{}); ok {
		for i, elem := range arr {
			fmt.Printf("  [%d] type: %T\n", i, elem)
			if m, ok := elem.(map[string]interface{}); ok {
				for k, v := range m {
					fmt.Printf("    %s: type=%T val=%v\n", k, v, v)
					if inner, ok := v.(map[string]interface{}); ok {
						for ik, iv := range inner {
							fmt.Printf("      %s: type=%T val=%v\n", ik, iv, iv)
						}
					}
				}
			}
		}
	}

	blob, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("Encode error: %v", err)
	}
	fmt.Printf("\nEncoded blob length: %d\n", len(blob))

	// Step 6: Decode the blob back and re-parse (this is what SubmitTransaction adapter does)
	decoded, err := binarycodec.Decode(blob)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	decodedJSON, _ := json.MarshalIndent(decoded, "", "  ")
	fmt.Printf("Decoded from blob: %s\n", decodedJSON)

	reJSON, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("Marshal decoded error: %v", err)
	}

	reParsed, err := tx.ParseJSON(reJSON)
	if err != nil {
		t.Fatalf("Re-ParseJSON error: %v", err)
	}
	fmt.Printf("Re-parsed tx type: %s\n", reParsed.TxType().String())

	fmt.Println("\nSUCCESS: Full sign-and-submit flow simulation worked!")
}
