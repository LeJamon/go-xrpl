package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/stretchr/testify/require"
)

type signingRulesLedger struct {
	*mockLedgerServiceSubmit
	rules     []*amendment.Rules
	ruleReads int
}

func (s *signingRulesLedger) TransactionRules() *amendment.Rules {
	index := min(s.ruleReads, len(s.rules)-1)
	s.ruleReads++
	return s.rules[index]
}

func signingRulesContext(rules ...*amendment.Rules) (*types.RpcContext, *signingRulesLedger) {
	ledger := &signingRulesLedger{mockLedgerServiceSubmit: newMockLedgerServiceSubmit(), rules: rules}
	ledger.standalone = false
	ctx := &types.RpcContext{
		Context: context.Background(), ApiVersion: types.ApiVersion1,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			Ledger: ledger, Capabilities: types.RPCCapabilities{SigningEnabled: true},
		}),
	}
	return ctx, ledger
}

func signingRulesRequest(t *testing.T, request map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	return raw
}

func signingRulesPrimary(t *testing.T, multi bool) map[string]any {
	t.Helper()
	transaction := map[string]any{
		"TransactionType": "LoanSet", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"LoanBrokerID": strings.Repeat("0", 64), "PrincipalRequested": "1",
		"Fee": "30", "Sequence": 1,
	}
	request := map[string]any{
		"tx_json": transaction, "passphrase": "masterpassphrase", "key_type": "secp256k1", "offline": true,
	}
	ctx, _ := signingRulesContext(amendment.EmptyRules())
	var result any
	var rpcErr *rpcerrors.RpcError
	if multi {
		transaction["Account"] = "rHSXa2gvfegaC7767QZGFZjebqkWKMCkTf"
		transaction["SigningPubKey"] = ""
		request["account"] = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		result, rpcErr = (&handlers.SignForMethod{}).Handle(ctx, signingRulesRequest(t, request))
	} else {
		result, rpcErr = (&handlers.SignMethod{}).Handle(ctx, signingRulesRequest(t, request))
	}
	require.Nil(t, rpcErr)
	fields := maps.Clone(result.(map[string]any)["tx_json"].(map[string]any))
	delete(fields, "hash")
	return fields
}

func signingRulesSigner(t *testing.T, algorithm string) string {
	t.Helper()
	result, rpcErr := (&handlers.WalletProposeMethod{}).Handle(&types.RpcContext{}, signingRulesRequest(t, map[string]any{
		"passphrase": "role signing rules", "key_type": algorithm,
	}))
	require.Nil(t, rpcErr)
	return result.(map[string]any)["account_id"].(string)
}

func requireSigningRulesResponse(t *testing.T, response map[string]any, rules, otherRules *amendment.Rules) {
	t.Helper()
	blob := response["tx_blob"].(string)
	raw, err := hex.DecodeString(blob)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(raw)
	require.NoError(t, err)
	require.Empty(t, sign.CheckSTTxSignature(parsed, rules, true))
	require.NotEmpty(t, sign.CheckSTTxSignature(parsed, otherRules, true))
	canonical, err := binarycodec.DecodeBytes(raw)
	require.NoError(t, err)
	canonical["hash"] = handlers.CalculateTxHash(blob)
	want, err := json.Marshal(canonical)
	require.NoError(t, err)
	got, err := json.Marshal(response["tx_json"])
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}

func TestSigningRPCRoleRulesSnapshot(t *testing.T) {
	oldRules := amendment.EmptyRules()
	newRules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	for _, cleanup := range []bool{false, true} {
		rules, otherRules := oldRules, newRules
		if cleanup {
			rules, otherRules = newRules, oldRules
		}
		for _, target := range []string{"CounterpartySignature", "SponsorSignature"} {
			for _, algorithm := range []string{"ed25519", "secp256k1"} {
				for _, method := range []string{"sign", "sign_for", "submit", "submit_multisigned", "submit_blob"} {
					t.Run(fmt.Sprintf("cleanup=%t/%s/%s/%s", cleanup, target, algorithm, method), func(t *testing.T) {
						ctx, ledger := signingRulesContext(rules, otherRules)
						multi := method == "sign_for" || method == "submit_multisigned"
						request := map[string]any{
							"tx_json":    signingRulesPrimary(t, multi),
							"passphrase": "role signing rules", "key_type": algorithm,
							"offline": true, "signature_target": target,
						}
						var result any
						var rpcErr *rpcerrors.RpcError
						if method == "submit_multisigned" || method == "submit_blob" {
							prepCtx, _ := signingRulesContext(rules)
							if multi {
								request["account"] = signingRulesSigner(t, algorithm)
								result, rpcErr = (&handlers.SignForMethod{}).Handle(prepCtx, signingRulesRequest(t, request))
							} else {
								result, rpcErr = (&handlers.SignMethod{}).Handle(prepCtx, signingRulesRequest(t, request))
							}
							require.Nil(t, rpcErr)
							response := result.(map[string]any)
							if multi {
								fields := response["tx_json"].(map[string]any)
								delete(fields, "hash")
								request = map[string]any{"tx_json": fields}
							} else {
								request = map[string]any{"tx_blob": response["tx_blob"]}
							}
						}
						switch method {
						case "sign":
							result, rpcErr = (&handlers.SignMethod{}).Handle(ctx, signingRulesRequest(t, request))
						case "sign_for":
							request["account"] = signingRulesSigner(t, algorithm)
							result, rpcErr = (&handlers.SignForMethod{}).Handle(ctx, signingRulesRequest(t, request))
						case "submit", "submit_blob":
							result, rpcErr = (&handlers.SubmitMethod{}).Handle(ctx, signingRulesRequest(t, request))
							require.Equal(t, 1, ledger.submitCalls)
						case "submit_multisigned":
							result, rpcErr = (&handlers.SubmitMultisignedMethod{}).Handle(ctx, signingRulesRequest(t, request))
							require.Equal(t, 1, ledger.submitCalls)
						}
						require.Nil(t, rpcErr)
						require.Equal(t, 1, ledger.ruleReads)
						response := result.(map[string]any)
						requireSigningRulesResponse(t, response, rules, otherRules)
						if method == "sign" || method == "sign_for" || method == "submit" {
							require.NotEmpty(t, response["deprecated"])
						}
						if method == "sign" || method == "sign_for" {
							require.Len(t, response, 3)
						}
					})
				}
			}
		}
	}
}

func TestSigningConstructionRejectsMismatchedNestedRules(t *testing.T) {
	oldRules := amendment.EmptyRules()
	newRules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	for _, cleanup := range []bool{false, true} {
		rules, otherRules := oldRules, newRules
		if cleanup {
			rules, otherRules = newRules, oldRules
		}
		for _, target := range []string{"CounterpartySignature", "SponsorSignature"} {
			for _, method := range []string{"sign", "sign_for", "submit_multisigned", "submit", "submit_blob"} {
				t.Run(fmt.Sprintf("cleanup=%t/%s/%s", cleanup, target, method), func(t *testing.T) {
					multi := method == "sign_for" || method == "submit_multisigned"
					ctx, _ := signingRulesContext(otherRules)
					nestedRequest := map[string]any{
						"tx_json": signingRulesPrimary(t, multi), "passphrase": "role signing rules",
						"key_type": "ed25519", "offline": true, "signature_target": target,
					}
					var result any
					var rpcErr *rpcerrors.RpcError
					if multi {
						nestedRequest["account"] = signingRulesSigner(t, "ed25519")
						result, rpcErr = (&handlers.SignForMethod{}).Handle(ctx, signingRulesRequest(t, nestedRequest))
					} else {
						result, rpcErr = (&handlers.SignMethod{}).Handle(ctx, signingRulesRequest(t, nestedRequest))
					}
					require.Nil(t, rpcErr)
					fields := maps.Clone(result.(map[string]any)["tx_json"].(map[string]any))
					delete(fields, "hash")
					request := map[string]any{
						"tx_json": fields, "passphrase": "masterpassphrase", "key_type": "secp256k1", "offline": true,
					}
					if method == "submit_blob" {
						request = map[string]any{"tx_blob": result.(map[string]any)["tx_blob"]}
					}
					for _, skip := range []bool{false, true} {
						ctx, ledger := signingRulesContext(rules)
						ledger.standalone = skip
						switch method {
						case "sign":
							result, rpcErr = (&handlers.SignMethod{}).Handle(ctx, signingRulesRequest(t, request))
						case "submit", "submit_blob":
							result, rpcErr = (&handlers.SubmitMethod{}).Handle(ctx, signingRulesRequest(t, request))
						case "sign_for":
							request["account"] = signingRulesSigner(t, "secp256k1")
							request["passphrase"] = "role signing rules"
							result, rpcErr = (&handlers.SignForMethod{}).Handle(ctx, signingRulesRequest(t, request))
						case "submit_multisigned":
							result, rpcErr = (&handlers.SubmitMultisignedMethod{}).Handle(ctx, signingRulesRequest(t, request))
						}
						if skip {
							require.Nil(t, rpcErr)
							require.NotNil(t, result)
						} else {
							require.Nil(t, result)
							require.NotNil(t, rpcErr)
							if method == "submit_blob" {
								require.Equal(t, "invalidTransaction", rpcErr.ErrorString)
								require.Equal(t, "fails local checks: "+strings.TrimSuffix(target, "Signature")+": Invalid signature.", rpcErr.ErrorException)
							} else {
								require.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
								require.Equal(t, "internal", rpcErr.ErrorString)
								require.Equal(t, "Invalid signature.", rpcErr.Message)
							}
							require.Zero(t, ledger.submitCalls)
						}
					}
				})
			}
		}
	}
}
