package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
)

type submitRecorder struct {
	types.LedgerService
	regularCalls  int
	failHardCalls int
}

func (s *submitRecorder) SubmitTransaction([]byte, ...string) (*types.SubmitResult, error) {
	s.regularCalls++
	return &types.SubmitResult{}, nil
}

func (s *submitRecorder) SubmitTransactionFailHard([]byte, string) (*types.SubmitResult, error) {
	s.failHardCalls++
	return &types.SubmitResult{}, nil
}

type regularSubmitRecorder struct {
	types.LedgerService
	calls int
}

func (s *regularSubmitRecorder) SubmitTransaction([]byte, ...string) (*types.SubmitResult, error) {
	s.calls++
	return &types.SubmitResult{}, nil
}

func TestSigningRequestUnmarshal(t *testing.T) {
	var request signingRequest
	if err := json.Unmarshal([]byte(`{"tx_json":{"TransactionType":"Payment"},"seed":"seed","key_type":"ed25519","offline":true,"build_path":true,"signature_target":"CounterpartySignature"}`), &request); err != nil {
		t.Fatal(err)
	}
	if string(request.Seed) != `"seed"` || string(request.KeyType) != `"ed25519"` || !request.Offline || !request.BuildPath || request.SignatureTarget != counterpartySignatureField || len(request.TxJson) == 0 {
		t.Fatalf("unexpected signing request: %+v", request)
	}
}

func TestFormatSignResult(t *testing.T) {
	for _, test := range []struct {
		name       string
		apiVersion int
	}{{name: "v1", apiVersion: 1}, {name: "v2", apiVersion: 2}} {
		t.Run(test.name, func(t *testing.T) {
			txMap := map[string]any{"TransactionType": "Payment", "Amount": "10", "hash": "HASH"}
			response := formatSignResult(signResult{TxMap: txMap, TxBlob: "BLOB"}, test.apiVersion)
			if response["tx_blob"] != "BLOB" || txMap["DeliverMax"] != "10" {
				t.Fatalf("unexpected response: %#v", response)
			}
			_, hasRootHash := response["hash"]
			_, hasAmount := txMap["Amount"]
			if hasRootHash != (test.apiVersion > 1) || hasAmount != (test.apiVersion == 1) {
				t.Fatalf("api %d response = %#v", test.apiVersion, response)
			}
		})
	}
}

func TestSubmitWithFailHard(t *testing.T) {
	recorder := &submitRecorder{}
	if _, err := submitWithFailHard(recorder, nil, "", true); err != nil {
		t.Fatal(err)
	}
	if recorder.failHardCalls != 1 || recorder.regularCalls != 0 {
		t.Fatalf("fail-hard calls = %d, regular calls = %d", recorder.failHardCalls, recorder.regularCalls)
	}

	fallback := &regularSubmitRecorder{}
	if _, err := submitWithFailHard(fallback, nil, "", true); err != nil {
		t.Fatal(err)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls = %d", fallback.calls)
	}
}

func TestSignTransactionJSONPreservesExplicitEmptyFields(t *testing.T) {
	params := json.RawMessage(`{
		"seed_hex":"00000000000000000000000000000000",
		"key_type":"ed25519"
	}`)
	tests := []struct {
		name     string
		txJSON   json.RawMessage
		field    string
		expected any
	}{
		{
			name:     "empty array",
			txJSON:   json.RawMessage(`{"TransactionType":"AccountSet","Account":"r9zRhGr7b6xPekLvT6wP4qNdWMryaumZS7","Fee":"10","Sequence":1,"Memos":[]}`),
			field:    "Memos",
			expected: []any{},
		},
		{
			name:     "empty blob",
			txJSON:   json.RawMessage(`{"TransactionType":"AccountSet","Account":"r9zRhGr7b6xPekLvT6wP4qNdWMryaumZS7","Fee":"10","Sequence":1,"Domain":""}`),
			field:    "Domain",
			expected: "",
		},
		{
			name:     "nested empty blob",
			txJSON:   json.RawMessage(`{"TransactionType":"AccountSet","Account":"r9zRhGr7b6xPekLvT6wP4qNdWMryaumZS7","Fee":"10","Sequence":1,"Memos":[{"Memo":{"MemoData":""}}]}`),
			field:    "Memos",
			expected: []any{map[string]any{"Memo": map[string]any{"MemoData": ""}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, rpcErr := signTransactionJSON(&types.RpcContext{Context: context.Background(), ApiVersion: 2}, test.txJSON, signCredentials{}, true, params, "")
			if rpcErr != nil {
				t.Fatalf("sign transaction: %v", rpcErr)
			}
			blob, err := hex.DecodeString(result.TxBlob)
			if err != nil {
				t.Fatalf("decode signed blob: %v", err)
			}
			decoded, err := binarycodec.DecodeBytes(blob)
			if err != nil {
				t.Fatalf("decode signed transaction: %v", err)
			}
			if got, ok := decoded[test.field]; !ok || !reflect.DeepEqual(got, test.expected) {
				t.Fatalf("decoded %s = %#v, want %#v", test.field, got, test.expected)
			}
			transaction, err := tx.ParseFromBinary(blob)
			if err != nil {
				t.Fatalf("parse signed transaction: %v", err)
			}
			if err := sign.VerifySignature(transaction, true); err != nil {
				t.Fatalf("verify signed transaction: %v", err)
			}
		})
	}
}
