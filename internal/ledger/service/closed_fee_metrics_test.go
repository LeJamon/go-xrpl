package service

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/protocol"
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
