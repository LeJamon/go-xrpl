package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sponsorRulesLedger struct {
	*mockLedgerServiceSubmit
	rules *amendment.Rules
}

func (s *sponsorRulesLedger) TransactionRules() *amendment.Rules {
	return s.rules
}

func sponsorSigningContext(rules *amendment.Rules) *types.RpcContext {
	return &types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			Ledger: &sponsorRulesLedger{
				mockLedgerServiceSubmit: newMockLedgerServiceSubmit(),
				rules:                   rules,
			},
			Capabilities: types.RPCCapabilities{SigningEnabled: true},
		}),
	}
}

func TestSign_SponsorSignatureTargetUsesRulesSpecificRolePrefix(t *testing.T) {
	primary := signOffline(t, json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"offline": true
	}`))
	primaryTx := primary["tx_json"].(map[string]any)
	primaryPubKey := primaryTx["SigningPubKey"]
	primarySignature := primaryTx["TxnSignature"]
	delete(primaryTx, "hash")
	delete(primaryTx, "DeliverMax")

	params := func(txMap map[string]any) json.RawMessage {
		request, err := json.Marshal(map[string]any{
			"tx_json":          txMap,
			"passphrase":       "counterparty phrase",
			"key_type":         "secp256k1",
			"offline":          true,
			"signature_target": "SponsorSignature",
		})
		require.NoError(t, err)
		return request
	}

	legacyResult := signOffline(t, params(primaryTx))
	legacyTx := legacyResult["tx_json"].(map[string]any)
	legacySponsor, ok := legacyTx["SponsorSignature"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, primaryPubKey, legacyTx["SigningPubKey"])
	assert.Equal(t, primarySignature, legacyTx["TxnSignature"])
	assert.NotEmpty(t, legacySponsor["SigningPubKey"])
	assert.NotEmpty(t, legacySponsor["TxnSignature"])

	cleanupRules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	result, rpcErr := (&handlers.SignMethod{}).Handle(
		sponsorSigningContext(cleanupRules), params(primaryTx))
	require.Nil(t, rpcErr)
	cleanupTx := result.(map[string]any)["tx_json"].(map[string]any)
	cleanupResult := result.(map[string]any)
	cleanupSponsor, ok := cleanupTx["SponsorSignature"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, primaryPubKey, cleanupTx["SigningPubKey"])
	assert.Equal(t, primarySignature, cleanupTx["TxnSignature"])
	assert.NotEqual(t, legacySponsor["TxnSignature"], cleanupSponsor["TxnSignature"],
		"sponsor role signature must switch from STX to SPN when cleanup is enabled")

	results := map[string]map[string]any{
		"legacy":  legacyResult,
		"cleanup": cleanupResult,
	}
	for name, txMap := range map[string]map[string]any{
		"legacy":  legacyTx,
		"cleanup": cleanupTx,
	} {
		t.Run(name, func(t *testing.T) {
			blob, err := hex.DecodeString(results[name]["tx_blob"].(string))
			require.NoError(t, err)
			parsed, err := txcore.ParseFromBinary(blob)
			require.NoError(t, err)
			sponsor := parsed.GetCommon().SponsorSignature
			require.NotNil(t, sponsor)
			rules := (*amendment.Rules)(nil)
			if name == "cleanup" {
				rules = cleanupRules
			}
			require.NoError(t, sign.VerifySponsorSignatureWithRules(parsed, sponsor, true, rules))
			assert.Equal(t, txMap["SponsorSignature"], sponsor.ToMap())
		})
	}
}

func TestSign_SignatureTargetRejectsUnknownInnerObject(t *testing.T) {
	handler := &handlers.SignMethod{}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}
	_, rpcErr := handler.Handle(signingEnabledContext(ctx), json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "masterpassphrase",
		"key_type": "secp256k1",
		"offline": true,
		"signature_target": "SomeOtherSignature"
	}`))
	require.NotNil(t, rpcErr)
	assert.Equal(t, "SomeOtherSignature", rpcErr.Message)
}

func TestSignFor_SponsorSignatureTargetUsesRulesSpecificRolePrefix(t *testing.T) {
	primary := signOffline(t, json.RawMessage(`{
		"tx_json": {
			"TransactionType": "Payment",
			"Account": "rHSXa2gvfegaC7767QZGFZjebqkWKMCkTf",
			"Destination": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"Amount": "1000000",
			"Fee": "10",
			"Sequence": 1
		},
		"passphrase": "primary signer phrase",
		"key_type": "secp256k1",
		"offline": true
	}`))
	primaryTx := primary["tx_json"].(map[string]any)
	delete(primaryTx, "TxnSignature")
	delete(primaryTx, "hash")
	delete(primaryTx, "DeliverMax")

	params := func(txMap map[string]any) json.RawMessage {
		request, err := json.Marshal(map[string]any{
			"account":          "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"tx_json":          txMap,
			"passphrase":       "masterpassphrase",
			"key_type":         "secp256k1",
			"signature_target": "SponsorSignature",
		})
		require.NoError(t, err)
		return request
	}

	legacyResult, rpcErr := (&handlers.SignForMethod{}).Handle(
		signingEnabledContext(&types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1}),
		params(primaryTx))
	require.Nil(t, rpcErr)
	legacyTx := legacyResult.(map[string]any)["tx_json"].(map[string]any)
	legacySponsor := legacyTx["SponsorSignature"].(map[string]any)

	cleanupRules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	cleanupResult, rpcErr := (&handlers.SignForMethod{}).Handle(
		sponsorSigningContext(cleanupRules), params(primaryTx))
	require.Nil(t, rpcErr)
	cleanupTx := cleanupResult.(map[string]any)["tx_json"].(map[string]any)
	cleanupSponsor := cleanupTx["SponsorSignature"].(map[string]any)
	assert.NotEmpty(t, legacySponsor["Signers"])
	assert.NotEmpty(t, cleanupSponsor["Signers"])
	assert.NotEqual(t, legacyResult.(map[string]any)["tx_blob"], cleanupResult.(map[string]any)["tx_blob"],
		"sponsor multisigning signature must switch prefix when cleanup is enabled")

	for name, response := range map[string]map[string]any{
		"legacy":  legacyResult.(map[string]any),
		"cleanup": cleanupResult.(map[string]any),
	} {
		t.Run(name, func(t *testing.T) {
			blob, err := hex.DecodeString(response["tx_blob"].(string))
			require.NoError(t, err)
			parsed, err := txcore.ParseFromBinary(blob)
			require.NoError(t, err)
			sponsor := parsed.GetCommon().SponsorSignature
			require.NotNil(t, sponsor)
			rules := (*amendment.Rules)(nil)
			if name == "cleanup" {
				rules = cleanupRules
			}
			require.NoError(t, sign.VerifySponsorSignatureWithRules(parsed, sponsor, true, rules))
		})
	}
}
