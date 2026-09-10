package adaptor

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
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
		Standalone:    true,
		Startup:       ledgerservice.StartupConfig{Mode: ledgerservice.StartupFresh},
		GenesisConfig: genesisConfig,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.Equal(t, cleanup, svc.TransactionRules().FixCleanup3_4_0Enabled())

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	return New(Config{LedgerService: svc, Identity: identity}), svc
}

func routerRoleSignedAccountSet(t *testing.T, cleanup bool) []byte {
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

func routerTransactionMessage(blob []byte, peer peermanagement.PeerID) *peermanagement.InboundMessage {
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
	legacyAdaptor, _ := newRouterRuleService(t, false)
	cleanupAdaptor, _ := newRouterRuleService(t, true)
	router := newTestRouter(&mockEngine{}, legacyAdaptor, nil)
	blob := routerRoleSignedAccountSet(t, true)

	legacy := router.handleTransaction(routerTransactionMessage(blob, 1))
	require.Error(t, legacy.submitError)
	require.ErrorIs(t, legacy.submitError, txengine.ErrInvalidSignature)
	require.Equal(t, resource.FeeInvalidSignature(), legacy.charge)
	require.Equal(t, "transaction-invalid-signature", legacy.chargeContext)

	router.adaptor = cleanupAdaptor
	cleanup := router.handleTransaction(routerTransactionMessage(blob, 2))
	require.NoError(t, cleanup.submitError)
	require.NotEqual(t, "transaction-known-bad", cleanup.chargeContext)
	require.NotEqual(t, "transaction-known-bad-signature", cleanup.chargeContext)
	require.Equal(t, openledger.ResultSuccess, cleanup.submitResult)
}

func TestRouterSignatureSuppressionRechecksLegacySignatureAfterCleanupTransition(t *testing.T) {
	legacyAdaptor, _ := newRouterRuleService(t, false)
	cleanupAdaptor, _ := newRouterRuleService(t, true)
	router := newTestRouter(&mockEngine{}, legacyAdaptor, nil)
	blob := routerRoleSignedAccountSet(t, false)

	legacy := router.handleTransaction(routerTransactionMessage(blob, 1))
	require.NoError(t, legacy.submitError)
	require.Equal(t, openledger.ResultSuccess, legacy.submitResult)

	router.adaptor = cleanupAdaptor
	cleanup := router.handleTransaction(routerTransactionMessage(blob, 2))
	require.Error(t, cleanup.submitError)
	require.ErrorIs(t, cleanup.submitError, txengine.ErrInvalidSignature)
	require.Equal(t, resource.FeeInvalidSignature(), cleanup.charge)
	require.Equal(t, "transaction-invalid-signature", cleanup.chargeContext)

	knownBad := router.handleTransaction(routerTransactionMessage(blob, 3))
	require.NoError(t, knownBad.submitError)
	require.Equal(t, resource.FeeUselessData(), knownBad.charge)
	require.Equal(t, "transaction-known-bad", knownBad.chargeContext)
}
