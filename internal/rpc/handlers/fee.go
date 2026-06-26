package handlers

import (
	"encoding/json"
	"math/bits"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// FeeMethod handles the fee RPC method.
// Mirrors rippled TxQ::doRPC (TxQ.cpp:1860-1909): emits queue
// occupancy, fee levels, and per-level drops derived from the live
// TxQ snapshot. When the TxQ hook isn't wired (standalone tests,
// pre-startup) the handler falls back to rippled's idle-state
// defaults — reference_level=256, drops fields equal to base_fee.
type FeeMethod struct{}

const (
	feeBaseLevel        uint64 = 256 // rippled TxQ.h baseLevel
	feeDefaultExpected         = 32  // minimumTxnInLedger (non-standalone)
	feeStandaloneExpect        = 1000
	feeLedgersInQueue          = 20
)

func (m *FeeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	baseFee, _, _ := ctx.Services.Ledger.GetCurrentFees()
	currentLedgerIndex := ctx.Services.Ledger.GetCurrentLedgerIndex()

	metrics := snapshotTxQ(ctx.Services, ctx.Services.Ledger.IsStandalone())

	effectiveBase := effectiveBaseFee(baseFee, metrics)
	openFee := dropsFromLevel(metrics.OpenLedgerFeeLevel, effectiveBase)
	if effectiveBase != 0 && levelFromDrops(openFee, effectiveBase) < metrics.OpenLedgerFeeLevel {
		openFee += 1
	}
	medianFee := dropsFromLevel(metrics.MedFeeLevel, baseFee)
	minFeeBase := baseFee
	if metrics.TxQMaxSize != nil && metrics.TxCount >= *metrics.TxQMaxSize {
		minFeeBase = effectiveBase
	}
	minimumFee := dropsFromLevel(metrics.MinProcessingFeeLevel, minFeeBase)

	response := map[string]any{
		"current_ledger_size":  strconv.FormatUint(uint64(metrics.TxInLedger), 10),
		"current_queue_size":   strconv.FormatUint(uint64(metrics.TxCount), 10),
		"expected_ledger_size": strconv.FormatUint(uint64(metrics.TxPerLedger), 10),
		"ledger_current_index": currentLedgerIndex,
		"levels": map[string]any{
			"reference_level":   strconv.FormatUint(metrics.ReferenceFeeLevel, 10),
			"minimum_level":     strconv.FormatUint(metrics.MinProcessingFeeLevel, 10),
			"median_level":      strconv.FormatUint(metrics.MedFeeLevel, 10),
			"open_ledger_level": strconv.FormatUint(metrics.OpenLedgerFeeLevel, 10),
		},
		"drops": map[string]any{
			"base_fee":        strconv.FormatUint(baseFee, 10),
			"median_fee":      strconv.FormatUint(medianFee, 10),
			"minimum_fee":     strconv.FormatUint(minimumFee, 10),
			"open_ledger_fee": strconv.FormatUint(openFee, 10),
		},
	}
	if metrics.TxQMaxSize != nil {
		response["max_queue_size"] = strconv.FormatUint(uint64(*metrics.TxQMaxSize), 10)
	}

	return response, nil
}

// snapshotTxQ returns the active TxQ metrics, or rippled's idle-state
// defaults when the TxQ hook isn't wired.
func snapshotTxQ(services *types.ServiceContainer, standalone bool) types.TxQFeeMetrics {
	if services != nil && services.TxQFeeMetrics != nil {
		return services.TxQFeeMetrics()
	}
	expected := uint32(feeDefaultExpected)
	if standalone {
		expected = feeStandaloneExpect
	}
	maxQueue := expected * feeLedgersInQueue
	return types.TxQFeeMetrics{
		TxCount:               0,
		TxQMaxSize:            &maxQueue,
		TxInLedger:            0,
		TxPerLedger:           expected,
		ReferenceFeeLevel:     feeBaseLevel,
		MinProcessingFeeLevel: feeBaseLevel,
		MedFeeLevel:           feeBaseLevel,
		OpenLedgerFeeLevel:    feeBaseLevel,
	}
}

// effectiveBaseFee mirrors rippled TxQ.cpp:1891-1895: when baseFee is
// 0 drops but escalation has kicked in, treat the base fee as 1 drop
// so the level→drops math doesn't collapse to 0.
func effectiveBaseFee(baseFee uint64, m types.TxQFeeMetrics) uint64 {
	if baseFee == 0 && m.OpenLedgerFeeLevel != m.ReferenceFeeLevel {
		return 1
	}
	return baseFee
}

// dropsFromLevel converts a fee level back to drops: (level * baseFee) / baseLevel.
// Mirrors rippled toDrops() with 128-bit intermediate to avoid overflow.
func dropsFromLevel(level, baseFee uint64) uint64 {
	hi, lo := bits.Mul64(level, baseFee)
	if hi >= feeBaseLevel {
		return ^uint64(0)
	}
	quo, _ := bits.Div64(hi, lo, feeBaseLevel)
	return quo
}

// levelFromDrops converts drops to a fee level: (drops * baseLevel) / baseFee.
// Used to detect when integer division truncated openFee below the
// snapshot's open-ledger level so we can round up by one drop.
func levelFromDrops(drops, baseFee uint64) uint64 {
	if baseFee == 0 {
		return 0
	}
	hi, lo := bits.Mul64(drops, feeBaseLevel)
	if hi >= baseFee {
		return ^uint64(0)
	}
	quo, _ := bits.Div64(hi, lo, baseFee)
	return quo
}

func (m *FeeMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *FeeMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *FeeMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
