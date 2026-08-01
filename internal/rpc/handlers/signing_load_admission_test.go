package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

const (
	loadAdmissionSeedHex        = "00000000000000000000000000000000"
	loadAdmissionSigningAccount = "r9zRhGr7b6xPekLvT6wP4qNdWMryaumZS7"
	loadAdmissionAccount        = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	loadAdmissionSigner         = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
)

type loadAdmissionLedger struct {
	types.LedgerService
	accountLookups   int
	sequenceFills    int
	feeFills         int
	transactionSends int
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

func (l *loadAdmissionLedger) SubmitTransaction([]byte, ...string) (*types.SubmitResult, error) {
	l.transactionSends++
	return &types.SubmitResult{}, nil
}

func (l *loadAdmissionLedger) GetCurrentFees() (uint64, uint64, uint64) {
	return 10, 0, 0
}

func signingLoadParams(offline bool) json.RawMessage {
	offlineField := ""
	if offline {
		offlineField = `,"offline":true`
	}
	return json.RawMessage(`{"seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519"` + offlineField + `,"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionSigningAccount + `","Sequence":1,"Fee":"10"}}`)
}

func assertTooBusy(t *testing.T, rpcErr *types.RpcError) {
	t.Helper()
	if rpcErr == nil || rpcErr.Code != types.RpcTOO_BUSY || rpcErr.ErrorString != "tooBusy" {
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
				Services:   &types.ServiceContainer{IsLoadedCluster: track.IsLoadedCluster},
			}
			_, rpcErr := (&SignMethod{}).Handle(ctx, signingLoadParams(true))
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
				Services:   services,
			}

			_, rpcErr := (&SignMethod{}).Handle(ctx, signingLoadParams(offline))
			assertTooBusy(t, rpcErr)
			if ledger.accountLookups != 0 || ledger.sequenceFills != 0 || ledger.feeFills != 0 || ledger.transactionSends != 0 {
				t.Fatalf("downstream calls = lookup:%d sequence:%d fee:%d submit:%d", ledger.accountLookups, ledger.sequenceFills, ledger.feeFills, ledger.transactionSends)
			}
		})
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
				Services: &types.ServiceContainer{IsLoadedCluster: func() bool {
					loadChecks++
					return true
				}},
			}
			_, rpcErr := (&SignMethod{}).Handle(ctx, test.params)
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
			ApiVersion: 2,
			Unlimited:  true,
			Services:   &types.ServiceContainer{IsLoadedCluster: loaded},
		}
		result, rpcErr := (&SignMethod{}).Handle(ctx, signingLoadParams(true))
		if rpcErr != nil {
			t.Fatalf("unlimited offline sign: %v", rpcErr)
		}
		if result == nil {
			t.Fatal("unlimited offline sign returned no result")
		}
	})

	t.Run("sign for", func(t *testing.T) {
		params := json.RawMessage(`{"account":"` + loadAdmissionSigner + `","seed_hex":"` + loadAdmissionSeedHex + `","key_type":"ed25519","tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Unlimited:  true,
			Services:   &types.ServiceContainer{IsLoadedCluster: loaded},
		}
		_, rpcErr := (&SignForMethod{}).Handle(ctx, params)
		if rpcErr != nil {
			t.Fatalf("unlimited sign_for: %v", rpcErr)
		}
	})

	t.Run("submit multisigned", func(t *testing.T) {
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Unlimited:  true,
			Services: &types.ServiceContainer{
				Ledger:          &loadAdmissionLedger{},
				IsLoadedCluster: loaded,
			},
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
			Services: &types.ServiceContainer{
				Ledger:          ledger,
				IsLoadedCluster: func() bool { return true },
			},
		}
		_, rpcErr := (&SubmitMethod{}).Handle(ctx, signingLoadParams(false))
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
			Services:   &types.ServiceContainer{IsLoadedCluster: func() bool { return true }},
		}
		_, rpcErr := (&SignForMethod{}).Handle(ctx, params)
		assertTooBusy(t, rpcErr)
	})

	t.Run("submit multisigned", func(t *testing.T) {
		ledger := &loadAdmissionLedger{}
		params := json.RawMessage(`{"tx_json":{"TransactionType":"AccountSet","Account":"` + loadAdmissionAccount + `","Sequence":1,"Fee":"10","SigningPubKey":""}}`)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			ApiVersion: 2,
			Services: &types.ServiceContainer{
				Ledger:          ledger,
				IsLoadedCluster: func() bool { return true },
			},
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
			Services: &types.ServiceContainer{IsLoadedCluster: func() bool {
				loadChecks++
				return true
			}},
		}
		_, rpcErr := (&SignForMethod{}).Handle(ctx, params)
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
			Services: &types.ServiceContainer{IsLoadedCluster: func() bool {
				loadChecks++
				return true
			}},
		}
		_, rpcErr := (&SignForMethod{}).Handle(ctx, params)
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
			Services: &types.ServiceContainer{
				Ledger: ledger,
				IsLoadedCluster: func() bool {
					loadChecks++
					return true
				},
			},
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
			Services: &types.ServiceContainer{
				Ledger:          ledger,
				IsLoadedCluster: func() bool { return true },
			},
		}
		_, rpcErr := (&SubmitMultisignedMethod{}).Handle(ctx, params)
		assertTooBusy(t, rpcErr)
		if ledger.accountLookups != 0 {
			t.Fatalf("account lookups = %d, want 0", ledger.accountLookups)
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
				Services: &types.ServiceContainer{
					Ledger: &loadAdmissionLedger{},
					IsLoadedCluster: func() bool {
						loadChecks++
						return true
					},
				},
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
