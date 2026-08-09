package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

const (
	loadAdmissionSeedHex        = "00000000000000000000000000000000"
	loadAdmissionSigningAccount = "r9zRhGr7b6xPekLvT6wP4qNdWMryaumZS7"
	loadAdmissionAccount        = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	loadAdmissionSigner         = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
)

func signingEnabledHandlerContext(ctx *types.RpcContext) *types.RpcContext {
	return ctx
}

func signingEnabledTestGraph(services *types.ServiceContainer) *types.ServiceGraph {
	if services == nil {
		services = &types.ServiceContainer{}
	}
	services.Capabilities.SigningEnabled = true
	return types.NewTestServiceGraph(services)
}

type loadAdmissionLedger struct {
	types.LedgerService
	accountLookups   int
	sequenceFills    int
	feeFills         int
	transactionSends int
	serverInfo       *types.LedgerServerInfo
}

func (l *loadAdmissionLedger) GetAccountInfo(context.Context, string, string) (*types.AccountInfo, error) {
	l.accountLookups++
	return &types.AccountInfo{Sequence: 1}, nil
}

func (l *loadAdmissionLedger) GetAutofillSequence(string, bool) (uint32, error) {
	l.sequenceFills++
	return 1, nil
}

func (l *loadAdmissionLedger) GetAutofillFee([]byte, bool, int, int) (uint64, error) {
	l.feeFills++
	return 10, nil
}

func (l *loadAdmissionLedger) SubmitTransaction([]byte, string) (*types.SubmitResult, error) {
	l.transactionSends++
	return &types.SubmitResult{}, nil
}

func (l *loadAdmissionLedger) GetCurrentFees() (uint64, uint64, uint64) {
	return 10, 0, 0
}

func (l *loadAdmissionLedger) GetServerInfo() types.LedgerServerInfo {
	if l.serverInfo != nil {
		return *l.serverInfo
	}
	return types.LedgerServerInfo{Standalone: true}
}

func signingLoadParams(offline bool) json.RawMessage {
	offlineField := ""
	if offline {
		offlineField = `,"offline":true`
	}
	return json.RawMessage(`{"seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519"` + offlineField + `,"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionSigningAccount + `","Sequence":1,"Fee":"10"}}`)
}

func assertTooBusy(t *testing.T, rpcErr *rpcerrors.RpcError) {
	t.Helper()
	if rpcErr == nil || rpcErr.Code != rpcerrors.RpcTOO_BUSY || rpcErr.ErrorString != "tooBusy" {
		t.Fatalf("error = %#v, want rpcTOO_BUSY", rpcErr)
	}
}

func TestSigningLoadAdmissionTracksLiveClusterAndLocalState(t *testing.T) {
	track := feetrack.New()
	services := &types.ServiceContainer{IsLoadedCluster: track.IsLoadedCluster}
	if services.IsLoadedCluster() {
		t.Fatal("new fee track reports loaded")
	}

	track.SetRemoteFee(feetrack.LoadBase + 1)
	if services.IsLoadedCluster() {
		t.Fatal("remote-only load reports cluster loaded")
	}

	track.SetClusterFee(feetrack.LoadBase + 1)
	if !services.IsLoadedCluster() {
		t.Fatal("cluster fee elevation was not observed through callback")
	}
	track.SetClusterFee(feetrack.LoadBase)
	if services.IsLoadedCluster() {
		t.Fatal("cleared cluster fee still reports loaded")
	}

	if changed := track.RaiseLocalFee(); changed {
		t.Fatal("first local raise unexpectedly changed the fee")
	}
	if !services.IsLoadedCluster() {
		t.Fatal("armed local raise was not observed through callback")
	}
}

func TestSignRejectsClusterOnlyAndArmedLocalLoad(t *testing.T) {
	for _, test := range []struct {
		name  string
		apply func(*feetrack.LoadFeeTrack)
	}{
		{
			name: "cluster only",
			apply: func(track *feetrack.LoadFeeTrack) {
				track.SetClusterFee(feetrack.LoadBase + 1)
			},
		},
		{
			name: "armed local raise",
			apply: func(track *feetrack.LoadFeeTrack) {
				track.RaiseLocalFee()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			track := feetrack.New()
			test.apply(track)
			ctx := &types.RpcContext{
				Context:    context.Background(),
				ApiVersion: 2,
				Services:   signingEnabledTestGraph(&types.ServiceContainer{IsLoadedCluster: track.IsLoadedCluster}),
			}
			_, rpcErr := (&SignMethod{}).Handle(signingEnabledHandlerContext(ctx), signingLoadParams(true))
			assertTooBusy(t, rpcErr)
		})
	}
}

func TestSignRejectsLoadedOnlineAndOfflineBeforeLedgerWork(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "online", true: "offline"}[offline], func(t *testing.T) {
			ledger := &loadAdmissionLedger{}
			services := &types.ServiceContainer{
				Ledger:          ledger,
				IsLoadedCluster: func() bool { return true },
			}
			ctx := &types.RpcContext{
				Context:    context.Background(),
				ApiVersion: 2,
				Services:   signingEnabledTestGraph(services),
			}

			_, rpcErr := (&SignMethod{}).Handle(signingEnabledHandlerContext(ctx), signingLoadParams(offline))
			assertTooBusy(t, rpcErr)
			if ledger.accountLookups != 0 || ledger.sequenceFills != 0 || ledger.feeFills != 0 || ledger.transactionSends != 0 {
				t.Fatalf("downstream calls = lookup:%d sequence:%d fee:%d submit:%d", ledger.accountLookups, ledger.sequenceFills, ledger.feeFills, ledger.transactionSends)
			}
		})
	}
}

func TestOnlineSigningRejectsStaleLedgerBeforeLoad(t *testing.T) {
	methods := []struct {
		name   string
		params json.RawMessage
		handle func(*types.RpcContext, json.RawMessage) (any, *rpcerrors.RpcError)
	}{
		{
			name:   "sign",
			params: signingLoadParams(false),
			handle: (&SignMethod{}).Handle,
		},
		{
			name:   "sign for",
			params: json.RawMessage(`{"account":"` + loadAdmissionSigner + `","seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`),
			handle: (&SignForMethod{}).Handle,
		},
	}

	apiVersions := []struct {
		name    string
		version int
	}{
		{name: "api v1", version: types.ApiVersion1},
		{name: "api v2", version: types.ApiVersion2},
	}
	for _, method := range methods {
		for _, api := range apiVersions {
			t.Run(method.name+" "+api.name, func(t *testing.T) {
				ledger := &loadAdmissionLedger{serverInfo: &types.LedgerServerInfo{}}
				loadChecks := 0
				ctx := &types.RpcContext{
					Context:    context.Background(),
					ApiVersion: api.version,
					Services: signingEnabledTestGraph(&types.ServiceContainer{
						Ledger: ledger,
						IsLoadedCluster: func() bool {
							loadChecks++
							return true
						},
					}),
				}

				_, rpcErr := method.handle(signingEnabledHandlerContext(ctx), method.params)
				if rpcErr == nil {
					t.Fatal("expected stale-ledger error")
				}
				wantCode := rpcerrors.RpcNOT_SYNCED
				wantToken := "notSynced"
				if api.version == types.ApiVersion1 {
					wantCode = rpcerrors.RpcNO_CURRENT
					wantToken = "noCurrent"
				}
				if rpcErr.Code != wantCode || rpcErr.ErrorString != wantToken {
					t.Fatalf("error = %#v, want %s (%d)", rpcErr, wantToken, wantCode)
				}
				if loadChecks != 0 || ledger.accountLookups != 0 {
					t.Fatalf("load checks = %d, account lookups = %d", loadChecks, ledger.accountLookups)
				}
			})
		}
	}
}

func TestSigningLoadAdmissionValidatesCredentialsAndShapeFirst(t *testing.T) {
	tests := []struct {
		name   string
		params json.RawMessage
		want   string
	}{
		{
			name:   "credentials",
			params: json.RawMessage(`{"offline":true,"tx_json":{"TransactionType":"AccountSet","Sequence":1,"Fee":"10"}}`),
			want:   "invalidParams",
		},
		{
			name:   "transaction type",
			params: json.RawMessage(`{"seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","offline":true,"tx_json":{"Sequence":1,"Fee":"10"}}`),
			want:   "invalidParams",
		},
		{
			name:   "account",
			params: json.RawMessage(`{"seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","offline":true,"tx_json":{"TransactionType":"AccountSet","Account":7,"Sequence":1,"Fee":"10"}}`),
			want:   "srcActMalformed",
		},
		{
			name:   "account missing",
			params: json.RawMessage(`{"seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","offline":true,"tx_json":{"TransactionType":"AccountSet","Sequence":1,"Fee":"10"}}`),
			want:   "srcActMissing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loadChecks := 0
			ctx := &types.RpcContext{
				Context:    context.Background(),
				ApiVersion: 2,
				Services: signingEnabledTestGraph(&types.ServiceContainer{IsLoadedCluster: func() bool {
					loadChecks++
					return true
				}}),
			}
			_, rpcErr := (&SignMethod{}).Handle(signingEnabledHandlerContext(ctx), test.params)
			if rpcErr == nil || rpcErr.ErrorString != test.want {
				t.Fatalf("error = %#v, want %s", rpcErr, test.want)
			}
			if loadChecks != 0 {
				t.Fatalf("load callback called %d times before validation completed", loadChecks)
			}
		})
	}
}

func TestSigningLoadAdmissionUnlimitedExemption(t *testing.T) {
	loadChecks := 0
	loaded := func() bool {
		loadChecks++
		return true
	}

	t.Run("sign", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: 2,
			Services:   signingEnabledTestGraph(&types.ServiceContainer{IsLoadedCluster: loaded}),
		}
		result, rpcErr := (&SignMethod{}).Handle(signingEnabledHandlerContext(ctx), signingLoadParams(true))
		if rpcErr != nil {
			t.Fatalf("unlimited offline sign: %v", rpcErr)
		}
		if result == nil {
			t.Fatal("unlimited offline sign returned no result")
		}
	})

	t.Run("sign for", func(t *testing.T) {
		params := json.RawMessage(`{"account":"` + loadAdmissionSigningAccount + `","seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: 2,
			Services:   signingEnabledTestGraph(&types.ServiceContainer{IsLoadedCluster: loaded}),
		}
		_, rpcErr := (&SignForMethod{}).Handle(signingEnabledHandlerContext(ctx), params)
		if rpcErr != nil {
			t.Fatalf("unlimited sign_for: %v", rpcErr)
		}
	})

	t.Run("submit multisigned", func(t *testing.T) {
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: 2,
			Services: signingEnabledTestGraph(&types.ServiceContainer{
				Ledger:          &loadAdmissionLedger{},
				IsLoadedCluster: loaded,
			}),
		}
		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr != nil && rpcErr.ErrorString == "tooBusy" {
			t.Fatalf("unlimited submit_multisigned was load-gated: %#v", rpcErr)
		}
	})

	if loadChecks != 0 {
		t.Fatalf("unlimited requests consulted load callback %d times", loadChecks)
	}
}

func TestCredentialedSubmitAndMultisignMethodsRejectLoaded(t *testing.T) {
	t.Run("credentialed submit", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Services: signingEnabledTestGraph(&types.ServiceContainer{
				Ledger:          ledger,
				IsLoadedCluster: func() bool { return true },
			}),
		}
		_, rpcErr := (&SubmitMethod{}).Handle(signingEnabledHandlerContext(ctx), signingLoadParams(false))
		assertTooBusy(t, rpcErr)
		if ledger.accountLookups != 0 || ledger.sequenceFills != 0 || ledger.feeFills != 0 || ledger.transactionSends != 0 {
			t.Fatalf("credentialed submit performed downstream work: %#v", ledger)
		}
	})

	t.Run("sign for", func(t *testing.T) {
		params := json.RawMessage(`{"account":"` + loadAdmissionSigner + `","seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Services:   signingEnabledTestGraph(&types.ServiceContainer{IsLoadedCluster: func() bool { return true }}),
		}
		_, rpcErr := (&SignForMethod{}).Handle(signingEnabledHandlerContext(ctx), params)
		assertTooBusy(t, rpcErr)
	})

	t.Run("submit multisigned", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Services: signingEnabledTestGraph(&types.ServiceContainer{
				Ledger:          ledger,
				IsLoadedCluster: func() bool { return true },
			}),
		}
		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		assertTooBusy(t, rpcErr)
		if ledger.accountLookups != 0 {
			t.Fatalf("account lookups = %d, want 0", ledger.accountLookups)
		}
	})
}

func TestMultisignLoadAdmissionValidationPrecedence(t *testing.T) {
	t.Run("sign for credentials", func(t *testing.T) {
		loadChecks := 0
		params := json.RawMessage(`{"account":"` + loadAdmissionSigner + `","tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Services: signingEnabledTestGraph(&types.ServiceContainer{IsLoadedCluster: func() bool {
				loadChecks++
				return true
			}}),
		}
		_, rpcErr := (&SignForMethod{}).Handle(signingEnabledHandlerContext(ctx), params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" {
			t.Fatalf("error = %#v, want credential validation error", rpcErr)
		}
		if loadChecks != 0 {
			t.Fatalf("load callback called %d times", loadChecks)
		}
	})

	t.Run("sign for transaction shape", func(t *testing.T) {
		loadChecks := 0
		params := json.RawMessage(`{"account":"` + loadAdmissionSigner + `","seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","tx_json":{"Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Services: signingEnabledTestGraph(&types.ServiceContainer{IsLoadedCluster: func() bool {
				loadChecks++
				return true
			}}),
		}
		_, rpcErr := (&SignForMethod{}).Handle(signingEnabledHandlerContext(ctx), params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" {
			t.Fatalf("error = %#v, want transaction validation error", rpcErr)
		}
		if loadChecks != 0 {
			t.Fatalf("load callback called %d times", loadChecks)
		}
	})

	t.Run("submit multisigned transaction shape", func(t *testing.T) {
		loadChecks := 0
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context: context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{
				Ledger: ledger,
				IsLoadedCluster: func() bool {
					loadChecks++
					return true
				},
			}),
		}
		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" {
			t.Fatalf("error = %#v, want transaction validation error", rpcErr)
		}
		if loadChecks != 0 || ledger.accountLookups != 0 {
			t.Fatalf("load checks = %d, account lookups = %d", loadChecks, ledger.accountLookups)
		}
	})

	t.Run("submit multisigned sequence value", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":"bad","Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context: context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{
				Ledger:          ledger,
				IsLoadedCluster: func() bool { return true },
			}),
		}
		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		assertTooBusy(t, rpcErr)
		if ledger.accountLookups != 0 {
			t.Fatalf("account lookups = %d, want 0", ledger.accountLookups)
		}
	})
}

func TestSubmitMultisignedUsesCanonicalFieldParsingAfterAdmission(t *testing.T) {
	t.Run("numeric string sequence and numeric fee", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":"1","Fee":50,"SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.Message != "Missing field 'tx_json.Signers'." {
			t.Fatalf("error = %#v, want missing Signers after canonical parsing", rpcErr)
		}
	})

	t.Run("sequence parse precedes transaction signature", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":"4294967296","Fee":"10","SigningPubKey":"","TxnSignature":""}}`)
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "Field 'tx_json.Sequence' has invalid data." {
			t.Fatalf("error = %#v, want Sequence parse failure", rpcErr)
		}
	})

	t.Run("numeric transaction type", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":3,"Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.Message != "Missing field 'tx_json.Signers'." {
			t.Fatalf("error = %#v, want missing Signers after numeric TransactionType", rpcErr)
		}
	})

	t.Run("numeric string transaction type", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"3","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.Message != "Missing field 'tx_json.Signers'." {
			t.Fatalf("error = %#v, want missing Signers after numeric-string TransactionType", rpcErr)
		}
	})

	t.Run("local checks precede transaction signature", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params, err := json.Marshal(map[string]any{"tx_json": map[string]any{
			"TransactionType": "AccountSet",
			"Account":         loadAdmissionAccount,
			"Sequence":        1,
			"Fee":             "10",
			"SigningPubKey":   "",
			"TxnSignature":    "",
			"Memos": []any{map[string]any{"Memo": map[string]any{
				"MemoData": strings.Repeat("AA", 1020),
			}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "The memo exceeds the maximum allowed size." {
			t.Fatalf("error = %#v, want local-check failure", rpcErr)
		}
	})

	t.Run("template errors are returned verbatim before transaction signature", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Destination":"` + loadAdmissionSigner + `","Sequence":1,"Fee":"10","SigningPubKey":"","TxnSignature":""}}`)
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "Field 'Destination' found in disallowed location." {
			t.Fatalf("error = %#v, want verbatim template failure", rpcErr)
		}
	})

	t.Run("local MPT fee rejection precedes transaction signature", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params, err := json.Marshal(map[string]any{"tx_json": map[string]any{
			"TransactionType": "AccountSet",
			"Account":         loadAdmissionAccount,
			"Sequence":        1,
			"Fee": map[string]any{
				"value":           "10",
				"mpt_issuance_id": strings.Repeat("A", 48),
			},
			"SigningPubKey": "",
			"TxnSignature":  "",
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "Amount can not be MPT." {
			t.Fatalf("error = %#v, want local MPT rejection", rpcErr)
		}
	})

	t.Run("issued fee uses the XRP fee error", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"1/USD/` + loadAdmissionSigner + `","SigningPubKey":"","TxnSignature":""}}`)
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.ErrorString != "invalidParams" || rpcErr.Message != "Invalid Fee field.  Fees must be specified in XRP." {
			t.Fatalf("error = %#v, want XRP fee rejection", rpcErr)
		}
	})

	t.Run("sorts signers by account ID", func(t *testing.T) {
		accounts := []string{loadAdmissionSigner, loadAdmissionSigningAccount}
		_, firstID, err := addresscodec.DecodeClassicAddressToAccountID(accounts[0])
		if err != nil {
			t.Fatal(err)
		}
		_, secondID, err := addresscodec.DecodeClassicAddressToAccountID(accounts[1])
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Compare(firstID, secondID) < 0 {
			accounts[0], accounts[1] = accounts[1], accounts[0]
		}
		signers := make([]any, len(accounts))
		for i, account := range accounts {
			signers[i] = map[string]any{"Signer": map[string]any{
				"Account":       account,
				"SigningPubKey": "",
				"TxnSignature":  "",
			}}
		}
		params, err := json.Marshal(map[string]any{"tx_json": map[string]any{
			"TransactionType": "AccountSet",
			"Account":         loadAdmissionAccount,
			"Sequence":        1,
			"Fee":             "10",
			"SigningPubKey":   "",
			"Signers":         signers,
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: &loadAdmissionLedger{}}),
		}

		result, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr != nil {
			t.Fatalf("submit_multisigned: %#v", rpcErr)
		}
		response := result.(map[string]any)
		txJSON := response["tx_json"].(map[string]any)
		gotSigners := txJSON["Signers"].([]any)
		previousID := []byte(nil)
		for _, wrapped := range gotSigners {
			signer := wrapped.(map[string]any)["Signer"].(map[string]any)
			account := signer["Account"].(string)
			_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(account)
			if err != nil {
				t.Fatal(err)
			}
			if previousID != nil && bytes.Compare(previousID, accountID) >= 0 {
				t.Fatalf("Signers not sorted by AccountID: %v", gotSigners)
			}
			previousID = accountID
		}
	})

	t.Run("delegate is the fee payer", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params, err := json.Marshal(map[string]any{"tx_json": map[string]any{
			"TransactionType": "AccountSet",
			"Account":         loadAdmissionAccount,
			"Delegate":        loadAdmissionSigner,
			"Sequence":        1,
			"Fee":             "10",
			"SigningPubKey":   "",
			"Signers": []any{map[string]any{"Signer": map[string]any{
				"Account":       loadAdmissionSigner,
				"SigningPubKey": "",
				"TxnSignature":  "",
			}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := &types.RpcContext{
			Context:  context.Background(),
			Services: signingEnabledTestGraph(&types.ServiceContainer{Ledger: ledger}),
		}

		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		if rpcErr == nil || rpcErr.Message != "A Signer may not be the transaction's Account ("+loadAdmissionSigner+")." {
			t.Fatalf("error = %#v, want delegated fee-payer rejection", rpcErr)
		}
	})
}

func TestSubmitWithoutCredentialsDoesNotConsultSigningLoad(t *testing.T) {
	for _, test := range []struct {
		name   string
		params json.RawMessage
	}{
		{name: "blob", params: json.RawMessage(`{"tx_blob":"00"}`)},
		{name: "unsigned json", params: json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet"}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			loadChecks := 0
			ctx := &types.RpcContext{
				Context: context.Background(),
				Services: signingEnabledTestGraph(&types.ServiceContainer{
					Ledger: &loadAdmissionLedger{},
					IsLoadedCluster: func() bool {
						loadChecks++
						return true
					},
				}),
			}
			_, rpcErr := (&SubmitMethod{}).Handle(ctx, test.params)
			if rpcErr != nil && rpcErr.ErrorString == "tooBusy" {
				t.Fatalf("unsigned submit was load-gated: %#v", rpcErr)
			}
			if loadChecks != 0 {
				t.Fatalf("load callback called %d times", loadChecks)
			}
		})
	}
}
