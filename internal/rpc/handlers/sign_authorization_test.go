package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type signingAuthorizationLedger struct {
	types.LedgerService
	accounts map[string]*types.AccountInfo
}

func (l *signingAuthorizationLedger) GetAccountInfo(_ context.Context, account, _ string) (*types.AccountInfo, error) {
	info, ok := l.accounts[account]
	if !ok {
		return nil, svcerr.ErrAccountNotFound
	}
	copy := *info
	return &copy, nil
}

func (l *signingAuthorizationLedger) GetServerInfo() types.LedgerServerInfo {
	return types.LedgerServerInfo{Standalone: true}
}

func signingAuthorizationContext(ledger types.LedgerService) *types.RpcContext {
	return &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleUser,
		ApiVersion: types.ApiVersion2,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			Ledger:       ledger,
			Capabilities: types.RPCCapabilities{SigningEnabled: true},
		}),
	}
}

func authorizationSignParams(account string, delegate any) json.RawMessage {
	txJSON := map[string]any{
		"TransactionType": "AccountSet",
		"Account":         account,
		"Sequence":        1,
		"Fee":             "10",
	}
	if delegate != nil {
		txJSON["Delegate"] = delegate
	}
	params, _ := json.Marshal(map[string]any{
		"seed_hex": loadAdmissionSeedHex,
		"key_type": "ed25519",
		"tx_json":  txJSON,
	})
	return params
}

func authorizationSignForParams(signer, txAccount string) json.RawMessage {
	params, _ := json.Marshal(map[string]any{
		"account":  signer,
		"seed_hex": loadAdmissionSeedHex,
		"key_type": "ed25519",
		"tx_json": map[string]any{
			"TransactionType": "AccountSet",
			"Account":         txAccount,
			"Sequence":        1,
			"Fee":             "10",
			"SigningPubKey":   "",
		},
	})
	return params
}

func requireSigningDeprecation(t *testing.T, rpcErr *types.RpcError) {
	t.Helper()
	if rpcErr == nil || rpcErr.Extra["deprecated"] != signingDeprecation {
		t.Fatalf("error = %#v, want signing deprecation", rpcErr)
	}
}

func TestSigningCapabilityGuardPrecedesParsingAndLoad(t *testing.T) {
	methods := []struct {
		name   string
		handle func(*types.RpcContext, json.RawMessage) (any, *types.RpcError)
	}{
		{name: "sign", handle: (&SignMethod{}).Handle},
		{name: "sign_for", handle: (&SignForMethod{}).Handle},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			loadedCalled := false
			ctx := &types.RpcContext{
				Role:       types.RoleIdentified,
				ApiVersion: types.ApiVersion2,
				Services: types.NewTestServiceGraph(&types.ServiceContainer{IsLoadedCluster: func() bool {
					loadedCalled = true
					return true
				}}),
			}
			_, rpcErr := method.handle(ctx, json.RawMessage(`{`))
			if rpcErr == nil || rpcErr.Code != types.RpcNOT_SUPPORTED || rpcErr.Message != "Signing is not supported by this server." {
				t.Fatalf("error = %#v, want signing notSupported", rpcErr)
			}
			if rpcErr.Extra != nil {
				t.Fatalf("guard error extras = %#v, want none", rpcErr.Extra)
			}
			if loadedCalled || ctx.LoadCost != 0 {
				t.Fatalf("guard reached load admission: called=%v cost=%d", loadedCalled, ctx.LoadCost)
			}
		})
	}
}

func TestSigningCapabilityAdminBypassAndDeprecation(t *testing.T) {
	for _, method := range []struct {
		name   string
		handle func(*types.RpcContext, json.RawMessage) (any, *types.RpcError)
	}{
		{name: "sign", handle: (&SignMethod{}).Handle},
		{name: "sign_for", handle: (&SignForMethod{}).Handle},
	} {
		t.Run(method.name, func(t *testing.T) {
			ctx := &types.RpcContext{Role: types.RoleAdmin, ApiVersion: types.ApiVersion2}
			_, rpcErr := method.handle(ctx, nil)
			if rpcErr == nil || rpcErr.Code != types.RpcINVALID_PARAMS {
				t.Fatalf("error = %#v, want invalidParams after admin bypass", rpcErr)
			}
			requireSigningDeprecation(t, rpcErr)
		})
	}

	ctx := signingAuthorizationContext(nil)
	params := json.RawMessage(`{"seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","offline":true,"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionSigningAccount + `","Sequence":1,"Fee":"10"}}`)
	result, rpcErr := (&SignMethod{}).Handle(ctx, params)
	if rpcErr != nil {
		t.Fatalf("offline sign: %v", rpcErr)
	}
	response := result.(map[string]any)
	if response["deprecated"] != signingDeprecation {
		t.Fatalf("deprecated = %#v", response["deprecated"])
	}
}

func TestSignSigningKeyAuthorization(t *testing.T) {
	tests := []struct {
		name      string
		account   string
		info      *types.AccountInfo
		wantCode  int
		wantToken string
	}{
		{name: "master", account: loadAdmissionSigningAccount, info: &types.AccountInfo{}},
		{name: "regular key", account: loadAdmissionAccount, info: &types.AccountInfo{RegularKey: loadAdmissionSigningAccount}},
		{name: "disabled master", account: loadAdmissionSigningAccount, info: &types.AccountInfo{Flags: entry.LsfDisableMaster}, wantCode: types.RpcMASTER_DISABLED, wantToken: "masterDisabled"},
		{name: "wrong key", account: loadAdmissionAccount, info: &types.AccountInfo{}, wantCode: types.RpcBAD_SECRET, wantToken: "badSecret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := &signingAuthorizationLedger{accounts: map[string]*types.AccountInfo{test.account: test.info}}
			result, rpcErr := (&SignMethod{}).Handle(signingAuthorizationContext(ledger), authorizationSignParams(test.account, nil))
			if test.wantCode == 0 {
				if rpcErr != nil {
					t.Fatalf("sign: %v", rpcErr)
				}
				if result.(map[string]any)["deprecated"] != signingDeprecation {
					t.Fatal("successful sign omitted deprecation")
				}
				return
			}
			if rpcErr == nil || rpcErr.Code != test.wantCode || rpcErr.ErrorString != test.wantToken {
				t.Fatalf("error = %#v, want %d/%s", rpcErr, test.wantCode, test.wantToken)
			}
			requireSigningDeprecation(t, rpcErr)
		})
	}
}

func TestSignDelegateSigningKeyAuthorization(t *testing.T) {
	source := &types.AccountInfo{}
	tests := []struct {
		name      string
		delegate  any
		accounts  map[string]*types.AccountInfo
		wantCode  int
		wantToken string
	}{
		{
			name:     "delegate master",
			delegate: loadAdmissionSigningAccount,
			accounts: map[string]*types.AccountInfo{loadAdmissionAccount: source, loadAdmissionSigningAccount: {}},
		},
		{
			name:      "missing delegate",
			delegate:  loadAdmissionSigningAccount,
			accounts:  map[string]*types.AccountInfo{loadAdmissionAccount: source},
			wantCode:  types.RpcDELEGATE_ACT_NOT_FOUND,
			wantToken: "delegateActNotFound",
		},
		{
			name:      "malformed delegate",
			delegate:  7,
			accounts:  map[string]*types.AccountInfo{loadAdmissionAccount: source},
			wantCode:  types.RpcSRC_ACT_MALFORMED,
			wantToken: "srcActMalformed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := &signingAuthorizationLedger{accounts: test.accounts}
			_, rpcErr := (&SignMethod{}).Handle(signingAuthorizationContext(ledger), authorizationSignParams(loadAdmissionAccount, test.delegate))
			if test.wantCode == 0 {
				if rpcErr != nil {
					t.Fatalf("sign: %v", rpcErr)
				}
				return
			}
			if rpcErr == nil || rpcErr.Code != test.wantCode || rpcErr.ErrorString != test.wantToken {
				t.Fatalf("error = %#v, want %d/%s", rpcErr, test.wantCode, test.wantToken)
			}
			if test.wantCode == types.RpcSRC_ACT_MALFORMED && rpcErr.Message != "Invalid field 'tx_json.Delegate'." {
				t.Fatalf("message = %q", rpcErr.Message)
			}
			requireSigningDeprecation(t, rpcErr)
		})
	}
}

func TestSignForSigningKeyAuthorization(t *testing.T) {
	tests := []struct {
		name      string
		signer    string
		txAccount string
		accounts  map[string]*types.AccountInfo
		wantCode  int
	}{
		{
			name:      "absent master",
			signer:    loadAdmissionSigningAccount,
			txAccount: loadAdmissionAccount,
			accounts:  map[string]*types.AccountInfo{loadAdmissionAccount: {}},
		},
		{
			name:      "regular key",
			signer:    loadAdmissionAccount,
			txAccount: loadAdmissionSigner,
			accounts: map[string]*types.AccountInfo{
				loadAdmissionAccount: {RegularKey: loadAdmissionSigningAccount},
				loadAdmissionSigner:  {},
			},
		},
		{
			name:      "disabled master",
			signer:    loadAdmissionSigningAccount,
			txAccount: loadAdmissionAccount,
			accounts: map[string]*types.AccountInfo{
				loadAdmissionSigningAccount: {Flags: entry.LsfDisableMaster},
				loadAdmissionAccount:        {},
			},
			wantCode: types.RpcMASTER_DISABLED,
		},
		{
			name:      "wrong key",
			signer:    loadAdmissionSigner,
			txAccount: loadAdmissionAccount,
			accounts: map[string]*types.AccountInfo{
				loadAdmissionSigner:  {},
				loadAdmissionAccount: {},
			},
			wantCode: types.RpcBAD_SECRET,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := &signingAuthorizationLedger{accounts: test.accounts}
			_, rpcErr := (&SignForMethod{}).Handle(signingAuthorizationContext(ledger), authorizationSignForParams(test.signer, test.txAccount))
			if test.wantCode == 0 {
				if rpcErr != nil {
					t.Fatalf("sign_for: %v", rpcErr)
				}
				return
			}
			if rpcErr == nil || rpcErr.Code != test.wantCode {
				t.Fatalf("error = %#v, want code %d", rpcErr, test.wantCode)
			}
			requireSigningDeprecation(t, rpcErr)
		})
	}
}
