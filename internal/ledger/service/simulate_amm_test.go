package service_test

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/keylet"
)

func signedBlob(t *testing.T, env *testenv.TestEnv, txn tx.Transaction, signer *testenv.Account) []byte {
	t.Helper()
	env.SignWith(txn, signer)
	txMap, err := txn.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	hexStr, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return blob
}

func mustApply(t *testing.T, svc *service.Service, blob []byte) {
	t.Helper()
	res := submitBlob(t, svc, blob, true)
	if !res.Result.IsSuccess() {
		t.Fatalf("submit result = %v (%s), want a tes success", res.Result, res.Message)
	}
}

func closeLedger(t *testing.T, svc *service.Service) {
	t.Helper()
	if _, err := svc.AcceptLedger(context.Background()); err != nil {
		t.Fatalf("AcceptLedger: %v", err)
	}
}

func accountSeq(t *testing.T, svc *service.Service, address string) uint32 {
	t.Helper()
	info, err := svc.GetAccountInfo(context.Background(), address, "current")
	if err != nil {
		t.Fatalf("GetAccountInfo(%s): %v", address, err)
	}
	return info.Sequence
}

// TestService_SimulateTransaction_AMMCreateUsesParentHash is the simulate-path
// counterpart of the openledger AMMCreate parent-hash regression tests. It drives
// an AMMCreate through Service.SimulateTransaction — which runs a full Apply
// against an open-ledger snapshot — and asserts the simulated AMM pseudo-account
// lands at the address derived from the open ledger's real parent hash. Before
// the fix, SimulateTransaction's EngineConfig left ParentHash unset, so simulating
// an AMMCreate reported a different AMM account than the network would produce.
func TestService_SimulateTransaction_AMMCreateUsesParentHash(t *testing.T) {
	// AMMCreate requires AMM + fixUniversalNumber, both VoteDefaultNo and so
	// absent from the default genesis set — enable them explicitly, else the
	// AMMCreate is temDISABLED.
	cfg := service.DefaultConfig()
	cfg.GenesisConfig.Amendments = append(cfg.GenesisConfig.Amendments,
		amendment.FeatureAMM, amendment.FeatureFixUniversalNumber)
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := testenv.MasterAccount()
	gw := testenv.NewAccount("gateway")
	alice := testenv.NewAccount("alice")

	// Submit + close are separate ledgers: SubmitTransaction applies to the
	// openLedgerView, and only AcceptLedger folds the result into the
	// s.openLedger snapshot that SimulateTransaction reads.

	// Ledger 1: fund gateway and alice from the master account.
	masterSeq := accountSeq(t, svc, master.Address)
	mustApply(t, svc, signedBlob(t, env, payment.Pay(master, gw, 100_000_000).Sequence(masterSeq).Build(), master))
	mustApply(t, svc, signedBlob(t, env, payment.Pay(master, alice, 200_000_000).Sequence(masterSeq+1).Build(), master))
	closeLedger(t, svc)

	// Ledger 2: gateway enables rippling (so the AMM can hold its USD), alice
	// trusts gateway USD, and gateway funds her with USD.
	gwSeq := accountSeq(t, svc, gw.Address)
	aliceSeq := accountSeq(t, svc, alice.Address)
	mustApply(t, svc, signedBlob(t, env, accountset.AccountSet(gw).DefaultRipple().Sequence(gwSeq).Build(), gw))
	mustApply(t, svc, signedBlob(t, env, trustset.TrustUSD(alice, gw, "1000").Sequence(aliceSeq).Build(), alice))
	mustApply(t, svc, signedBlob(t, env, payment.PayIssued(gw, alice, gw.IOU("USD", 100)).Sequence(gwSeq+1).Build(), gw))
	closeLedger(t, svc)

	// Build the AMMCreate to simulate (not submitted). AMMCreate's fee is one
	// owner-reserve increment (2 XRP at genesis), which the open-ledger fee check
	// in SimulateTransaction enforces.
	amount1 := ammtest.XRPAmount(50)
	amount2 := gw.IOU("USD", 50)
	ammTx := ammtest.AMMCreate(alice, amount1, amount2).Fee("2000000").Build()
	ammSeq := accountSeq(t, svc, alice.Address)
	ammTx.GetCommon().Sequence = &ammSeq

	// Derive the correct (real parent hash) and buggy (zero hash) AMM
	// pseudo-account addresses against the open-ledger view simulate snapshots.
	view := svc.GetOpenLedger()
	asset1 := tx.Asset{Currency: amount1.Currency, Issuer: amount1.Issuer}
	asset2 := tx.Asset{Currency: amount2.Currency, Issuer: amount2.Issuer}
	ammKeylet := coreamm.ComputeAMMKeylet(asset1, asset2)
	wantAddr := coreamm.PseudoAccountAddress(view, view.ParentHash(), ammKeylet.Key)
	zeroAddr := coreamm.PseudoAccountAddress(view, [32]byte{}, ammKeylet.Key)
	if wantAddr == zeroAddr {
		t.Fatal("open-ledger ParentHash() is zero — test cannot distinguish the fix")
	}

	result, err := svc.SimulateTransaction(ammTx)
	if err != nil {
		t.Fatalf("SimulateTransaction: %v", err)
	}
	if !result.Result.IsSuccess() {
		t.Fatalf("AMMCreate simulate result = %v (%s), want a tes success", result.Result, result.Message)
	}
	if result.Metadata == nil {
		t.Fatal("simulate returned nil metadata")
	}

	wantKey := keylet.Account(wantAddr).Key
	zeroKey := keylet.Account(zeroAddr).Key
	wantIdx := strings.ToUpper(hex.EncodeToString(wantKey[:]))
	zeroIdx := strings.ToUpper(hex.EncodeToString(zeroKey[:]))
	var createdAccounts []string
	for _, n := range result.Metadata.AffectedNodes {
		if n.NodeType == "CreatedNode" && n.LedgerEntryType == "AccountRoot" {
			createdAccounts = append(createdAccounts, strings.ToUpper(n.LedgerIndex))
		}
	}

	hasWant, hasZero := false, false
	for _, idx := range createdAccounts {
		switch idx {
		case wantIdx:
			hasWant = true
		case zeroIdx:
			hasZero = true
		}
	}
	if !hasWant {
		t.Errorf("simulated AMMCreate did not create the AMM pseudo-account at the real-parent-hash address %s; created AccountRoots = %v", wantIdx, createdAccounts)
	}
	if hasZero {
		t.Errorf("simulated AMMCreate created the AMM pseudo-account at the zero-parent-hash address %s — ParentHash not threaded into the simulate EngineConfig", zeroIdx)
	}
}
