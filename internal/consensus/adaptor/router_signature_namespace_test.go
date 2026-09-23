package adaptor

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func newRouterRuleService(t *testing.T, cleanup bool) (*Adaptor, *ledgerservice.Service) {
	t.Helper()
	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Amendments = append(genesisConfig.Amendments,
		amendment.FeatureLendingProtocol,
		amendment.FeatureSingleAssetVault,
		amendment.FeatureMPTokensV1,
	)
	if cleanup {
		genesisConfig.Amendments = append(genesisConfig.Amendments, amendment.FeatureFixCleanup3_4_0)
	}
	svc, err := ledgerservice.New(ledgerservice.Config{
		Standalone:    false,
		Startup:       ledgerservice.StartupConfig{Mode: ledgerservice.StartupFresh},
		GenesisConfig: genesisConfig,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	svc.SetValidatedLedger(closed.Sequence(), closed.Hash())
	require.Eventually(t, func() bool {
		validated := svc.GetValidatedLedger()
		return validated != nil && validated.Hash() == closed.Hash()
	}, time.Second, time.Millisecond)
	_, err = svc.AcceptConsensusResult(context.Background(), closed, nil, nil, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, cleanup, svc.TransactionRules().FixCleanup3_4_0Enabled())

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	return New(Config{LedgerService: svc, Identity: identity}), svc
}

func routerMixedRuleService(t *testing.T, validatedCleanup bool) (*Adaptor, *ledgerservice.Service) {
	t.Helper()
	// Start with the opposite snapshot so the successor promotion below leaves
	// the requested validated/open rule pair.
	adaptor, svc := newRouterRuleService(t, !validatedCleanup)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	// Switching to a successor whose Amendments SLE has the opposite cleanup
	// bit leaves the published open view on the prior validated rules. Promoting
	// that successor then gives the router two real, independently published
	// rule snapshots.
	candidate := routerSuccessorWithCleanupRules(t, parent, validatedCleanup)
	require.NoError(t, svc.SwitchToPreferredLedger(candidate))
	svc.SetValidatedLedger(candidate.Sequence(), candidate.Hash())
	require.Eventually(t, func() bool {
		validated := svc.GetValidatedLedger()
		openRules := svc.TransactionRules()
		return validated != nil && validated.Hash() == candidate.Hash() &&
			validated.Rules().FixCleanup3_4_0Enabled() == validatedCleanup &&
			openRules.FixCleanup3_4_0Enabled() != validatedCleanup
	}, time.Second, time.Millisecond)

	return adaptor, svc
}

func routerSuccessorWithCleanupRules(t *testing.T, parent *ledger.Ledger, cleanup bool) *ledger.Ledger {
	t.Helper()
	next, err := ledger.NewOpen(parent, time.Now())
	require.NoError(t, err)

	ids := parent.Rules().EnabledIDs()
	filtered := make([][32]byte, 0, len(ids)+1)
	for _, id := range ids {
		if id == amendment.FeatureFixCleanup3_4_0 {
			if cleanup {
				filtered = append(filtered, id)
			}
			continue
		}
		filtered = append(filtered, id)
	}
	if cleanup && !parent.Rules().FixCleanup3_4_0Enabled() {
		filtered = append(filtered, amendment.FeatureFixCleanup3_4_0)
	}
	amendments, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{Amendments: filtered})
	require.NoError(t, err)
	require.NoError(t, next.Update(keylet.Amendments(), amendments))
	require.NoError(t, next.Close(parent.CloseTime().Add(10*time.Second), 0))
	require.Equal(t, cleanup, next.Rules().FixCleanup3_4_0Enabled())
	return next
}

func routerRoleSignedLoanSet(t *testing.T, cleanup bool) []byte {
	return routerRoleSignedLoanSetWithMemo(t, cleanup, "")
}

func routerRoleSignedLoanSetWithMemo(t *testing.T, cleanup bool, memoData string) []byte {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	primary := jtx.MasterAccount()
	counterparty := jtx.NewAccount("router-signature-counterparty")
	txn := lending.NewLoanSet(
		primary.Address,
		"1111111111111111111111111111111111111111111111111111111111111111",
		"1",
	)
	sequence := uint32(1)
	txn.GetCommon().Sequence = &sequence
	txn.GetCommon().Fee = "10"
	if memoData != "" {
		txn.GetCommon().Memos = []tx.MemoWrapper{{Memo: tx.Memo{MemoData: memoData}}}
	}
	env.SignWith(txn, primary)

	rules := amendment.EmptyRules()
	if cleanup {
		rules = amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	}
	roleSignature, err := txsign.SignCounterpartyWithRules(
		txn,
		counterparty.PublicKeyHex(),
		"00"+counterparty.PrivateKeyHex(),
		rules,
	)
	require.NoError(t, err)
	txn.GetCommon().CounterpartySignature = roleSignature

	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexBlob, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexBlob)
	require.NoError(t, err)
	return blob
}

func routerSignedPaymentWithMemo(t *testing.T, memoData string) []byte {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	primary := jtx.MasterAccount()
	destination := jtx.NewAccount("router-local-check-destination")
	txn := payment.Pay(primary, destination, 100_000_000).
		Sequence(1).
		WithMemo("", memoData, "").
		Build()
	env.SignWith(txn, primary)

	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexBlob, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexBlob)
	require.NoError(t, err)
	return blob
}

func routerTransactionMessage(t *testing.T, blob []byte, peer peermanagement.PeerID) *peermanagement.InboundMessage {
	t.Helper()
	txMsg := &message.Transaction{
		RawTransaction: blob,
		Status:         message.TxStatusNew,
	}
	return &peermanagement.InboundMessage{
		PeerID:  peer,
		Type:    message.TypeTransaction,
		Payload: encodePayload(t, txMsg),
	}
}

func routerFetchedTransactionMessage(blob []byte, peer peermanagement.PeerID) *peermanagement.InboundMessage {
	return &peermanagement.InboundMessage{
		PeerID: peer,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: blob,
			Status:         message.TxStatusNew,
		},
	}
}

func TestRouterSignatureSuppressionRetriesAfterCleanupTransition(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		initialValidatedCleanup bool
		nextCleanup             bool
		signatureCleanup        bool
	}{
		{
			name:                    "legacy validated to cleanup validated",
			initialValidatedCleanup: false,
			nextCleanup:             true,
			signatureCleanup:        true,
		},
		{
			name:                    "cleanup validated to legacy validated",
			initialValidatedCleanup: true,
			nextCleanup:             false,
			signatureCleanup:        false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor, _ := routerMixedRuleService(t, tc.initialValidatedCleanup)
			nextAdaptor, _ := newRouterRuleService(t, tc.nextCleanup)
			router := newTestRouter(&mockEngine{}, adaptor, nil)
			blob := routerRoleSignedLoanSet(t, tc.signatureCleanup)

			direct := router.handleTransaction(routerTransactionMessage(t, blob, 1))
			require.Error(t, direct.submitError)
			require.ErrorIs(t, direct.submitError, txengine.ErrInvalidSignature)
			require.Equal(t, resource.FeeInvalidSignature(), direct.charge)
			require.Equal(t, "transaction-invalid-signature", direct.chargeContext)

			// The changed validated snapshot selects a new namespace immediately;
			// the old scoped failure must not become a rules-independent BAD.
			router.adaptor = nextAdaptor
			retried := router.handleTransaction(routerTransactionMessage(t, blob, 2))
			require.NoError(t, retried.submitError)
			require.Equal(t, openledger.ResultSuccess, retried.submitResult)
			require.True(t, retried.relayed)
			require.Zero(t, retried.charge.Cost())
		})
	}
}

func TestRouterSignatureSuppressionRechecksRoleSignaturesAfterCleanupTransition(t *testing.T) {
	for _, tc := range []struct {
		name             string
		validatedCleanup bool
	}{
		{name: "validated legacy open cleanup", validatedCleanup: false},
		{name: "validated cleanup open legacy", validatedCleanup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor, _ := routerMixedRuleService(t, tc.validatedCleanup)
			for _, signatureCleanup := range []bool{false, true} {
				t.Run(map[bool]string{false: "legacy", true: "cleanup"}[signatureCleanup], func(t *testing.T) {
					router := newTestRouter(&mockEngine{}, adaptor, nil)
					blob := routerRoleSignedLoanSet(t, signatureCleanup)

					direct := router.handleTransaction(routerTransactionMessage(t, blob, 1))
					require.Error(t, direct.submitError)
					require.ErrorIs(t, direct.submitError, txengine.ErrInvalidSignature)
					require.Equal(t, resource.FeeInvalidSignature(), direct.charge)
					require.Equal(t, "transaction-invalid-signature", direct.chargeContext)

					// A predecoded frame is the shape emitted by the
					// TMTransactions fanout. It still gets the validated
					// signature check before open-ledger admission.
					fetchedRouter := newTestRouter(&mockEngine{}, adaptor, nil)
					fetched := fetchedRouter.handleTransaction(routerFetchedTransactionMessage(blob, 2))
					require.Error(t, fetched.submitError)
					require.ErrorIs(t, fetched.submitError, txengine.ErrInvalidSignature)
					require.Equal(t, resource.FeeInvalidSignature(), fetched.charge)
					require.Equal(t, "transaction-invalid-signature", fetched.chargeContext)
					require.False(t, fetched.relayed)
				})
			}
		})
	}
}

func TestRouterSignatureSuppressionRechecksGoodSignatureAfterCleanupTransition(t *testing.T) {
	for _, tc := range []struct {
		name           string
		initialClean   bool
		signatureClean bool
	}{
		{name: "legacy signature becomes invalid after cleanup", initialClean: false, signatureClean: false},
		{name: "cleanup signature becomes invalid after legacy", initialClean: true, signatureClean: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor, _ := newRouterRuleService(t, tc.initialClean)
			nextAdaptor, _ := newRouterRuleService(t, !tc.initialClean)
			router := newTestRouter(&mockEngine{}, adaptor, nil)
			blob := routerRoleSignedLoanSet(t, tc.signatureClean)

			good := router.handleTransaction(routerTransactionMessage(t, blob, 1))
			require.NoError(t, good.submitError)
			require.Equal(t, openledger.ResultSuccess, good.submitResult)
			require.True(t, good.relayed)

			router.adaptor = nextAdaptor
			bad := router.handleTransaction(routerTransactionMessage(t, blob, 2))
			require.Error(t, bad.submitError)
			require.ErrorIs(t, bad.submitError, txengine.ErrInvalidSignature)
			require.Equal(t, resource.FeeInvalidSignature(), bad.charge)
			require.Equal(t, "transaction-invalid-signature", bad.chargeContext)
			require.False(t, bad.relayed)
		})
	}
}

func TestRouterLegacyRoleLocalFailureRemainsRetryable(t *testing.T) {
	adaptor, _ := newRouterRuleService(t, false)
	router := newTestRouter(&mockEngine{}, adaptor, nil)
	blob := routerRoleSignedLoanSetWithMemo(t, false, strings.Repeat("AA", tx.MaxSerializedMemosSize))

	first := router.handleTransaction(routerFetchedTransactionMessage(blob, 1))
	require.ErrorIs(t, first.submitError, ledgerservice.ErrInvalidLocalTransaction)
	require.Equal(t, resource.FeeInvalidSignature(), first.charge)
	require.Equal(t, "transaction-local-checks", first.chargeContext)

	router.txSeen.now = func() time.Time { return time.Now().Add(transactionProcessInterval) }
	retry := router.handleTransaction(routerFetchedTransactionMessage(blob, 2))
	require.ErrorIs(t, retry.submitError, ledgerservice.ErrInvalidLocalTransaction)
	require.Equal(t, resource.FeeInvalidSignature(), retry.charge)
	require.Equal(t, "transaction-local-checks", retry.chargeContext)
}

func TestRouterLegacyRoleLocalFailureDoesNotPoisonCleanupTransition(t *testing.T) {
	legacyAdaptor, _ := newRouterRuleService(t, false)
	router := newTestRouter(&mockEngine{}, legacyAdaptor, nil)
	blob := routerRoleSignedLoanSetWithMemo(t, false, strings.Repeat("AA", tx.MaxSerializedMemosSize))

	first := router.handleTransaction(routerFetchedTransactionMessage(blob, 1))
	require.ErrorIs(t, first.submitError, ledgerservice.ErrInvalidLocalTransaction)
	require.Equal(t, resource.FeeInvalidSignature(), first.charge)
	require.Equal(t, "transaction-local-checks", first.chargeContext)

	cleanupAdaptor, _ := newRouterRuleService(t, true)
	router.adaptor = cleanupAdaptor
	retry := router.handleTransaction(routerFetchedTransactionMessage(blob, 2))
	require.ErrorIs(t, retry.submitError, txengine.ErrInvalidSignature)
	require.Equal(t, resource.FeeInvalidSignature(), retry.charge)
	require.Equal(t, "transaction-invalid-signature", retry.chargeContext)
	require.False(t, retry.relayed)
}

func TestRouterValidatedLocalFailurePrecedesOpenSignature(t *testing.T) {
	oversizedMemo := strings.Repeat("AA", tx.MaxSerializedMemosSize)
	for _, validatedCleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "validated legacy", true: "validated cleanup"}[validatedCleanup], func(t *testing.T) {
			adaptor, _ := routerMixedRuleService(t, validatedCleanup)
			router := newTestRouter(&mockEngine{}, adaptor, nil)
			blob := routerRoleSignedLoanSetWithMemo(t, validatedCleanup, oversizedMemo)

			dispatch := router.handleTransaction(routerFetchedTransactionMessage(blob, 1))
			require.ErrorIs(t, dispatch.submitError, ledgerservice.ErrInvalidLocalTransaction)
			require.Equal(t, resource.FeeInvalidSignature(), dispatch.charge)
			require.Equal(t, "transaction-local-checks", dispatch.chargeContext)
			require.False(t, dispatch.relayed)
		})
	}
}

func TestRouterCleanupRoleAndOrdinaryLocalFailuresUsePublicBad(t *testing.T) {
	oversizedMemo := strings.Repeat("AA", tx.MaxSerializedMemosSize)
	for _, tc := range []struct {
		name    string
		cleanup bool
		role    bool
	}{
		{name: "cleanup role", cleanup: true, role: true},
		{name: "ordinary legacy", cleanup: false, role: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor, _ := newRouterRuleService(t, tc.cleanup)
			router := newTestRouter(&mockEngine{}, adaptor, nil)
			blob := routerSignedPaymentWithMemo(t, oversizedMemo)
			if tc.role {
				blob = routerRoleSignedLoanSetWithMemo(t, tc.cleanup, oversizedMemo)
			}

			first := router.handleTransaction(routerFetchedTransactionMessage(blob, 1))
			require.ErrorIs(t, first.submitError, ledgerservice.ErrInvalidLocalTransaction)
			require.Equal(t, resource.FeeInvalidSignature(), first.charge)
			require.Equal(t, "transaction-local-checks", first.chargeContext)

			duplicate := router.handleTransaction(routerFetchedTransactionMessage(blob, 2))
			require.NoError(t, duplicate.submitError)
			require.Equal(t, resource.FeeUselessData(), duplicate.charge)
			require.Equal(t, "transaction-known-bad", duplicate.chargeContext)
			require.False(t, duplicate.relayed)
		})
	}
}
