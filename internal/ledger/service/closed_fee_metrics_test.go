package service

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestClosedLedgerFeeMetricsDispatchCustomFee(t *testing.T) {
	all.RegisterAll()

	service, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { service.Stop() })

	const reserveIncrement = uint64(2_000_000)
	create := amm.NewAMMCreate(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		tx.NewXRPAmount(1_000_000),
		tx.NewIssuedAmount(1_000_000, -6, "USD", "r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59"),
		0,
	)
	create.GetCommon().Fee = strconv.FormatUint(reserveIncrement, 10)
	create.GetCommon().SetSequence(1)

	raw, err := tx.SerializeTransaction(create)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	blob, err := tx.CreateTxWithMetaBlob(raw, &tx.Metadata{TransactionResult: ter.TesSUCCESS})
	if err != nil {
		t.Fatalf("CreateTxWithMetaBlob: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(create)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	if err := service.openLedger.AddTransactionWithMeta(hash, blob); err != nil {
		t.Fatalf("AddTransactionWithMeta: %v", err)
	}

	ctx := &closedLedgerCtx{
		ledger:           service.openLedger,
		baseFee:          10,
		reserveBase:      10_000_000,
		reserveIncrement: reserveIncrement,
	}
	config := ctx.feeConfig()
	if config.ReserveIncrement != reserveIncrement {
		t.Fatalf("ReserveIncrement = %d, want %d", config.ReserveIncrement, reserveIncrement)
	}
	wantCloseTime := protocol.ToRippleTime(service.openLedger.ParentCloseTime())
	if config.ParentCloseTime != wantCloseTime {
		t.Fatalf("ParentCloseTime = %d, want %d", config.ParentCloseTime, wantCloseTime)
	}
	levels := ctx.GetTransactionFeeLevels()
	if len(levels) != 1 || levels[0] != txq.FeeLevel(txq.BaseLevel) {
		t.Fatalf("fee levels = %v, want [%d]", levels, txq.BaseLevel)
	}
}

func feeFailureLedger(t *testing.T) (*Service, *ledger.Ledger, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Standalone = true
	cfg.Startup.Mode = StartupFresh
	cfg.GenesisConfig = genesis.DefaultConfig()
	cfg.GenesisConfig.Amendments = [][32]byte{
		amendment.FeatureLendingProtocol, amendment.FeatureSingleAssetVault, amendment.FeatureMPTokensV1,
	}
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	current := svc.openLedgerView.Current()
	require.True(t, current.Rules().Enabled(amendment.FeatureLendingProtocol))
	require.False(t, current.Rules().Enabled(amendment.FeatureFixCleanup3_1_3))
	require.False(t, current.Rules().Enabled(amendment.FeatureFixCleanup3_4_0))
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	loanID, brokerID, vaultID := [32]byte{1}, [32]byte{2}, [32]byte{3}
	loan := &entry.Loan{}
	loan.SetOwnerNode("0")
	loan.SetLoanBrokerNode("0")
	loan.SetLoanBrokerID(hex.EncodeToString(brokerID[:]))
	loan.SetLoanSequence(1)
	loan.SetBorrower(account)
	loan.SetStartDate(1)
	loan.SetPaymentInterval(60)
	loan.SetPeriodicPayment("0")
	loan.SetPaymentRemaining(10)
	loan.SetNextPaymentDueDate(^uint32(0))
	loan.SetFlags(0)
	raw, err := loan.Encode()
	require.NoError(t, err)
	require.NoError(t, current.Insert(keylet.LoanByID(loanID), raw))
	broker := &entry.LoanBroker{}
	broker.SetSequence(1)
	broker.SetOwnerNode("0")
	broker.SetVaultNode("0")
	broker.SetVaultID(hex.EncodeToString(vaultID[:]))
	broker.SetAccount(account)
	broker.SetOwner(account)
	broker.SetLoanSequence(1)
	broker.SetFlags(0)
	raw, err = broker.Encode()
	require.NoError(t, err)
	require.NoError(t, current.Insert(keylet.LoanBrokerByID(brokerID), raw))
	vault := &entry.Vault{}
	vault.SetSequence(1)
	vault.SetOwnerNode("0")
	vault.SetOwner(account)
	vault.SetAccount(account)
	vault.SetAsset(map[string]any{"currency": "XRP"})
	vault.SetShareMPTID(strings.Repeat("0", 48))
	vault.SetWithdrawalPolicy(1)
	vault.SetFlags(0)
	raw, err = vault.Encode()
	require.NoError(t, err)
	require.NoError(t, current.Insert(keylet.VaultByID(vaultID), raw))
	return svc, current, hex.EncodeToString(loanID[:])
}

func TestClosedLedgerFeeMetricsExcludeCalculationFailures(t *testing.T) {
	for _, allFailed := range []bool{false, true} {
		name := "mixed"
		if allFailed {
			name = "all failed"
		}
		t.Run(name, func(t *testing.T) {
			_, current, loanID := feeFailureLedger(t)
			ctx := &closedLedgerCtx{ledger: current, baseFee: 10}
			var expected []txq.FeeLevel
			count := 3
			if allFailed {
				count = 1
			}
			for i := 0; i < count; i++ {
				pay := lending.NewLoanPay("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", loanID, tx.NewXRPAmount(10))
				pay.SetSequence(uint32(i + 1))
				pay.Fee = "1000000"
				if i > 0 {
					pay.SetFlags(lending.TfLoanFullPayment)
					pay.Fee = strconv.Itoa(i * 10)
					expected = append(expected, txq.FeeLevel(i*256))
				}
				raw, err := tx.SerializeTransaction(pay)
				require.NoError(t, err)
				parsed, err := tx.ParseFromBinary(raw)
				require.NoError(t, err)
				fee, err := sign.CalculateBaseFee(parsed, current, ctx.feeConfig())
				if i == 0 {
					var failure *ter.ResultError
					require.ErrorAs(t, err, &failure)
					require.Equal(t, ter.TefEXCEPTION, failure.Code)
					require.Zero(t, fee)
				} else {
					require.NoError(t, err)
					require.Equal(t, uint64(10), fee)
				}
				blob, err := tx.CreateTxWithMetaBlob(raw, &tx.Metadata{TransactionResult: ter.TesSUCCESS})
				require.NoError(t, err)
				hash, err := tx.ComputeTransactionHash(parsed)
				require.NoError(t, err)
				require.NoError(t, current.AddTransactionWithMeta(hash, blob))
			}
			require.ElementsMatch(t, expected, ctx.GetTransactionFeeLevels())
			require.Equal(t, uint32(count), ctx.GetTransactionCount())
			queue, err := txq.New(txq.DefaultConfig())
			require.NoError(t, err)
			require.Equal(t, uint64(count), queue.ProcessClosedLedger(ctx, false))
		})
	}
}
