package handlers

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
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
	if request.Seed != "seed" || request.KeyType != "ed25519" || !request.Offline || !request.BuildPath || request.SignatureTarget != counterpartySignatureField || len(request.TxJson) == 0 {
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
