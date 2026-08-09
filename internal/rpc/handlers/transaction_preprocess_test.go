package handlers

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodectypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func signerValue(account string) map[string]any {
	return map[string]any{"Signer": map[string]any{
		"Account":       account,
		"SigningPubKey": "",
		"TxnSignature":  "",
	}}
}

func TestNormalizeSignersStrictAndLossless(t *testing.T) {
	first := signerValue(loadAdmissionSigner)
	second := signerValue(loadAdmissionSigningAccount)
	_, firstID, _ := addresscodec.DecodeClassicAddressToAccountID(loadAdmissionSigner)
	_, secondID, _ := addresscodec.DecodeClassicAddressToAccountID(loadAdmissionSigningAccount)
	if bytes.Compare(firstID, secondID) < 0 {
		first, second = second, first
	}
	valid := []any{first, second}
	for _, test := range []struct {
		name       string
		value      any
		feePayer   string
		wantError  string
		wantSorted bool
	}{
		{name: "non-array", value: map[string]any{}, feePayer: loadAdmissionAccount, wantError: "Signers array may only contain Signer entries."},
		{name: "wrapper extra field", value: []any{map[string]any{"Signer": map[string]any{
			"Account": loadAdmissionSigner, "SigningPubKey": "", "TxnSignature": "", "Extra": "x",
		}, "Other": map[string]any{}}}, feePayer: loadAdmissionAccount, wantError: "Signers array may only contain Signer entries."},
		{name: "wrong wrapper key", value: []any{map[string]any{"NotSigner": map[string]any{}}}, feePayer: loadAdmissionAccount, wantError: "Signers array may only contain Signer entries."},
		{name: "inner missing field", value: []any{map[string]any{"Signer": map[string]any{
			"Account": loadAdmissionSigner, "SigningPubKey": "",
		}}}, feePayer: loadAdmissionAccount, wantError: "Signers array may only contain Signer entries."},
		{name: "inner wrong type", value: []any{map[string]any{"Signer": map[string]any{
			"Account": loadAdmissionSigner, "SigningPubKey": 1, "TxnSignature": "",
		}}}, feePayer: loadAdmissionAccount, wantError: "Signers array may only contain Signer entries."},
		{name: "invalid account", value: []any{signerValue("not-an-account")}, feePayer: loadAdmissionAccount, wantError: "Signers array may only contain Signer entries."},
		{name: "duplicate", value: []any{signerValue(loadAdmissionSigner), signerValue(loadAdmissionSigner)}, feePayer: loadAdmissionAccount, wantError: "Duplicate Signers:Signer:Account entries (" + loadAdmissionSigner + ") are not allowed."},
		{name: "delegated self-sign", value: []any{signerValue(loadAdmissionSigner)}, feePayer: loadAdmissionSigner, wantError: "A Signer may not be the transaction's Account (" + loadAdmissionSigner + ")."},
		{name: "unsorted decoded account IDs", value: valid, feePayer: loadAdmissionAccount, wantSorted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, rpcErr := normalizeSigners(test.value, test.feePayer)
			if test.wantError != "" {
				if rpcErr == nil || rpcErr.Message != test.wantError {
					t.Fatalf("error = %#v, want %q", rpcErr, test.wantError)
				}
				return
			}
			if rpcErr != nil {
				t.Fatalf("normalizeSigners: %v", rpcErr)
			}
			if !test.wantSorted {
				return
			}
			if len(got) != len(valid) {
				t.Fatalf("got %d signers, want %d", len(got), len(valid))
			}
			for i := 1; i < len(got); i++ {
				previous := got[i-1]["Signer"].(map[string]any)["Account"].(string)
				current := got[i]["Signer"].(map[string]any)["Account"].(string)
				_, previousID, _ := addresscodec.DecodeClassicAddressToAccountID(previous)
				_, currentID, _ := addresscodec.DecodeClassicAddressToAccountID(current)
				if bytes.Compare(previousID, currentID) >= 0 {
					t.Fatalf("accounts are not sorted by decoded ID: %v", got)
				}
			}
			if !reflect.DeepEqual(got[0], valid[0]) && !reflect.DeepEqual(got[0], valid[1]) {
				t.Fatal("normalized signer lost fields")
			}
		})
	}

	got, rpcErr := normalizeSigners([]any{}, loadAdmissionAccount)
	if rpcErr != nil || len(got) != 0 {
		t.Fatalf("empty array = %#v, %#v; want empty success", got, rpcErr)
	}
}

func TestPreprocessTransactionCanonicalizesAndValidatesSigners(t *testing.T) {
	base := func(signers any) map[string]any {
		return map[string]any{
			"TransactionType": "AccountSet",
			"Account":         loadAdmissionAccount,
			"Sequence":        1,
			"Fee":             "10",
			"SigningPubKey":   "",
			"Signers":         signers,
		}
	}
	preprocess := func(signers any) (*types.RpcError, string) {
		transaction, rpcErr := preprocessTransaction(base(signers), transactionPreprocessOptions{
			mode:            transactionPreprocessSubmitMultisigned,
			preserveSigners: true,
		})
		if rpcErr != nil {
			return rpcErr, ""
		}
		return nil, transaction.GetCommon().Signers[0].Signer.Account
	}

	t.Run("non-array", func(t *testing.T) {
		rpcErr, _ := preprocess(map[string]any{})
		if rpcErr == nil || rpcErr.Message != "Field 'tx_json.Signers' is not a JSON array." {
			t.Fatalf("error = %#v", rpcErr)
		}
	})

	t.Run("malformed signer blob", func(t *testing.T) {
		value := signerValue(loadAdmissionSigner)
		value["Signer"].(map[string]any)["SigningPubKey"] = "not-hex"
		rpcErr, _ := preprocess([]any{value})
		want := "Error at 'tx_json.Signers.[0].Signer'. Field 'tx_json.Signers.[0].Signer.SigningPubKey' has invalid data."
		if rpcErr == nil || rpcErr.Message != want {
			t.Fatalf("error = %#v, want %q", rpcErr, want)
		}
	})

	t.Run("hex account ID", func(t *testing.T) {
		_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(loadAdmissionSigner)
		if err != nil {
			t.Fatal(err)
		}
		value := signerValue(strings.ToUpper(hex.EncodeToString(accountID)))
		rpcErr, account := preprocess([]any{value})
		if rpcErr != nil {
			t.Fatalf("preprocess: %v", rpcErr)
		}
		if account != loadAdmissionSigner {
			t.Fatalf("canonical account = %q, want %q", account, loadAdmissionSigner)
		}
	})

	t.Run("array limit", func(t *testing.T) {
		items := make([]any, binarycodectypes.MaxJSONArrayElements+1)
		for i := range items {
			items[i] = signerValue(loadAdmissionSigner)
		}
		rpcErr, _ := preprocess(items)
		want := fmt.Sprintf(
			"Field 'tx_json.Signers' exceeds allowed JSON array size of %d elements per field.",
			binarycodectypes.MaxJSONArrayElements)
		if rpcErr == nil || rpcErr.Message != want {
			t.Fatalf("error = %#v, want %q", rpcErr, want)
		}
	})
}

type paymentRulesLedger struct {
	loadAdmissionLedger
	rules *amendment.Rules
}

func (l *paymentRulesLedger) TransactionRules() *amendment.Rules {
	return l.rules
}

func TestCheckPaymentMPTGatePrecedesLaterValidation(t *testing.T) {
	ledger := &paymentRulesLedger{rules: amendment.NewRules(nil)}
	txMap := map[string]any{
		"TransactionType": "Payment",
		"Account":         loadAdmissionAccount,
		"Destination":     loadAdmissionSigner,
		"Amount": map[string]any{
			"mpt_issuance_id": strings.Repeat("A", 48),
			"value":           "1",
		},
		"Paths":    []any{},
		"DomainID": "00",
	}
	ctx := &types.RpcContext{Services: types.NewTestServiceGraph(&types.ServiceContainer{Ledger: ledger})}
	rpcErr := checkPayment(txMap, json.RawMessage(`{"build_path":true}`), true, ctx)
	if rpcErr == nil || rpcErr.Message != "Field 'build_path' not allowed in this context." {
		t.Fatalf("error = %#v", rpcErr)
	}
}

func TestCheckPaymentValidationAndMutation(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"TransactionType": "Payment",
			"Amount":          "1",
			"Destination":     loadAdmissionSigner,
		}
	}
	for _, test := range []struct {
		name       string
		tx         func() map[string]any
		params     string
		doPath     bool
		wantError  string
		wantPath   bool
		wantAmount any
	}{
		{name: "non-payment", tx: func() map[string]any { return map[string]any{"TransactionType": "AccountSet"} }},
		{name: "missing amount", tx: func() map[string]any { tx := base(); delete(tx, "Amount"); return tx }, wantError: "Missing field 'tx_json.Amount'."},
		{name: "invalid amount", tx: func() map[string]any { tx := base(); tx["Amount"] = "not-an-amount"; return tx }, wantError: "Invalid field 'tx_json.Amount'."},
		{name: "missing destination", tx: func() map[string]any { tx := base(); delete(tx, "Destination"); return tx }, wantError: "Missing field 'tx_json.Destination'."},
		{name: "invalid destination", tx: func() map[string]any { tx := base(); tx["Destination"] = "not-an-account"; return tx }, wantError: "Invalid field 'tx_json.Destination'."},
		{name: "differing amount aliases", tx: func() map[string]any { tx := base(); tx["DeliverMax"] = "2"; return tx }, wantError: "Cannot specify differing 'Amount' and 'DeliverMax'"},
		{name: "deliver max mutation", tx: func() map[string]any { tx := base(); delete(tx, "Amount"); tx["DeliverMax"] = "2"; return tx }, wantAmount: "2"},
		{name: "build path forbidden", tx: base, params: `{"build_path":true}`, wantError: "Field 'build_path' not allowed in this context."},
		{name: "paths plus build path", tx: func() map[string]any { tx := base(); tx["Paths"] = []any{}; return tx }, params: `{"build_path":true}`, doPath: true, wantError: "Cannot specify both 'tx_json.Paths' and 'build_path'"},
		{name: "invalid domain", tx: func() map[string]any { tx := base(); tx["DomainID"] = "00"; return tx }, wantError: "Unable to parse 'DomainID'."},
		{name: "invalid send max", tx: base, params: `{"build_path":true}`, doPath: true, wantError: "Invalid field 'tx_json.SendMax'.", wantAmount: "1"},
		{name: "xrp to xrp", tx: func() map[string]any { return base() }, params: `{"build_path":true}`, doPath: true, wantError: "Cannot build XRP to XRP paths."},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := test.tx()
			if test.name == "invalid send max" {
				tx["SendMax"] = "not-an-amount"
			}
			rpcErr := checkPayment(tx, json.RawMessage(test.params), test.doPath, nil)
			if test.wantError != "" {
				if rpcErr == nil || rpcErr.Message != test.wantError {
					t.Fatalf("error = %#v, want %q", rpcErr, test.wantError)
				}
				return
			}
			if rpcErr != nil {
				t.Fatalf("checkPayment: %v", rpcErr)
			}
			if test.wantPath {
				t.Fatal("path construction unexpectedly succeeded without an open-ledger context")
			}
			if test.wantAmount != nil && tx["Amount"] != test.wantAmount {
				t.Fatalf("Amount = %#v, want %#v", tx["Amount"], test.wantAmount)
			}
		})
	}

	issued := map[string]any{
		"TransactionType": "Payment",
		"Amount":          "1/USD/" + loadAdmissionAccount,
		"Destination":     loadAdmissionSigner,
		"SendMax":         "2/USD/" + loadAdmissionAccount,
	}
	rpcErr := checkPayment(issued, json.RawMessage(`{"build_path":true}`), true, nil)
	if rpcErr == nil {
		t.Fatal("issued build_path unexpectedly succeeded without an open-ledger context")
	}
}

func TestSubmitMultisignedRejectsMalformedSignerBeforeBinaryEncoding(t *testing.T) {
	params, err := json.Marshal(map[string]any{"tx_json": map[string]any{
		"TransactionType": "AccountSet",
		"Account":         loadAdmissionAccount,
		"Sequence":        1,
		"Fee":             "10",
		"SigningPubKey":   "",
		"Signers": []any{map[string]any{"Signer": map[string]any{
			"Account":       loadAdmissionSigner,
			"SigningPubKey": "",
			"TxnSignature":  "",
			"Extra":         "must reject",
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := &types.RpcContext{
		Context:  context.Background(),
		Services: types.NewTestServiceGraph(&types.ServiceContainer{Ledger: &loadAdmissionLedger{}}),
	}
	_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
	if rpcErr == nil || rpcErr.Message != "Signers array may only contain Signer entries." {
		t.Fatalf("error = %#v, want strict signer error", rpcErr)
	}
}
