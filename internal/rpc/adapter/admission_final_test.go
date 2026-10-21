package adapter

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txall "github.com/LeJamon/go-xrpl/internal/tx/all"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	lendingtx "github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

const (
	admissionAdapterSource       = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	admissionAdapterDestination  = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	admissionAdapterCheckID      = "ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB"
	admissionAdapterLoanBrokerID = "CDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCD"
)

func newAdmissionAdapterService(t *testing.T) *service.Service {
	t.Helper()
	txall.RegisterAll()
	cfg := genesis.DefaultConfig()
	cfg.Amendments = append(cfg.Amendments,
		amendment.FeatureLendingProtocol,
		amendment.FeatureSingleAssetVault,
		amendment.FeatureMPTokensV1,
	)
	svc, err := service.New(service.Config{
		Standalone:    true,
		Startup:       service.StartupConfig{Mode: service.StartupFresh},
		GenesisConfig: cfg,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	return svc
}

func signedAdmissionBlob(t *testing.T, txType string) []byte {
	t.Helper()
	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair(bytes.Repeat([]byte{0x42}, 16), false)
	require.NoError(t, err)

	overCap := tx.NewXRPAmount(int64(binarytypes.MaxDrops) + 1)
	var transaction tx.Transaction
	switch txType {
	case "AccountSet":
		transaction = tx.NewBaseTx(tx.TypeAccountSet, protocol.ZeroAccount)
	case "CheckCreate":
		transaction = checktx.NewCheckCreate(admissionAdapterSource, admissionAdapterDestination, overCap)
	case "CheckCash":
		cash := checktx.NewCheckCash(admissionAdapterSource, admissionAdapterCheckID)
		cash.SetExactAmount(overCap)
		transaction = cash
	case "LoanBrokerCoverDeposit":
		transaction = lendingtx.NewLoanBrokerCoverDeposit(admissionAdapterSource, admissionAdapterLoanBrokerID, overCap)
	case "LoanBrokerCoverWithdraw":
		transaction = lendingtx.NewLoanBrokerCoverWithdraw(admissionAdapterSource, admissionAdapterLoanBrokerID, overCap)
	default:
		t.Fatalf("unsupported transaction type %q", txType)
	}

	common := transaction.GetCommon()
	common.Fee = "10"
	sequence := uint32(1)
	common.Sequence = &sequence
	common.SigningPubKey = publicKey
	common.TxnSignature, err = txsign.SignTransaction(transaction, privateKey)
	require.NoError(t, err)
	require.Empty(t, txsign.CheckSTTxSignature(transaction, amendment.AllSupportedRules(), true))
	blob, err := tx.SerializeTransaction(transaction)
	require.NoError(t, err)
	return blob
}

func TestAdapterSubmissionZeroSourceReachesEngineTER(t *testing.T) {
	svc := newAdmissionAdapterService(t)
	result, err := NewLedgerServiceAdapter(svc).SubmitTransaction(nil, hex.EncodeToString(signedAdmissionBlob(t, "AccountSet")))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, ter.TemBAD_SRC_ACCOUNT.String(), result.EngineResult)
	require.Equal(t, int(ter.TemBAD_SRC_ACCOUNT), result.EngineResultCode)
	require.False(t, result.Applied)
}

func TestAdapterSubmissionOverCapNativeAmountsReachEngineTER(t *testing.T) {
	svc := newAdmissionAdapterService(t)
	for _, txType := range []string{
		"CheckCreate",
		"CheckCash",
		"LoanBrokerCoverDeposit",
		"LoanBrokerCoverWithdraw",
	} {
		t.Run(txType, func(t *testing.T) {
			result, err := NewLedgerServiceAdapter(svc).SubmitTransaction(nil, hex.EncodeToString(signedAdmissionBlob(t, txType)))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, ter.TemBAD_AMOUNT.String(), result.EngineResult)
			require.Equal(t, int(ter.TemBAD_AMOUNT), result.EngineResultCode)
			require.False(t, result.Applied)
		})
	}
}
