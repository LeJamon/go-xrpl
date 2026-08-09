// Package feetrack_test tests fee scaling calculations under load.
// Reference: rippled/src/test/app/LoadFeeTrack_test.cpp
//
// In rippled, LoadFeeTrack tracks the local and network load factors,
// and scaleFeeLoad() scales a transaction fee based on the current load.
// In the Go engine, fee escalation is handled by ScaleFeeLevel() in the
// txq package, which computes escalated fees based on transaction count
// relative to the expected count.
package feetrack_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/require"
)

// TestFeeScaling_NoLoad verifies that under default conditions (no load
// escalation), fees scale linearly as identity.
// Reference: rippled LoadFeeTrack_test.cpp lines 34-86
//
// Rippled tests scaleFeeLoad() with a fresh LoadFeeTrack (no load):
//   - scaleFeeLoad(0) == 0
//   - scaleFeeLoad(10000) == 10000
//   - scaleFeeLoad(1) == 1
//
// In the Go engine, we verify the equivalent: ToFeeLevel → ToDrops round-trip
// preserves the original fee when no escalation is active.
func TestFeeScaling_NoLoad(t *testing.T) {
	const baseFee = uint64(10) // Default reference fee: 10 drops

	t.Run("zero fee scales to zero", func(t *testing.T) {
		level := txq.ToFeeLevel(0, baseFee)
		drops := level.ToDrops(baseFee)
		require.Equal(t, uint64(0), drops)
	})

	t.Run("base fee round-trips exactly", func(t *testing.T) {
		level := txq.ToFeeLevel(baseFee, baseFee)
		require.Equal(t, txq.FeeLevel(txq.BaseLevel), level, "base fee should produce BaseLevel (256)")
		drops := level.ToDrops(baseFee)
		require.Equal(t, baseFee, drops)
	})

	t.Run("large fee round-trips", func(t *testing.T) {
		fee := uint64(10000)
		level := txq.ToFeeLevel(fee, baseFee)
		drops := level.ToDrops(baseFee)
		require.Equal(t, fee, drops)
	})

	t.Run("sub-base fee truncates in round-trip", func(t *testing.T) {
		// In rippled, scaleFeeLoad(1) = 1 with no load (load factor = 1).
		// In the Go engine, ToFeeLevel/ToDrops uses integer division:
		// ToFeeLevel(1, 10) = 1*256/10 = 25, ToDrops(10) = 25*10/256 = 0
		// Sub-base-fee amounts lose precision in the fee level representation.
		fee := uint64(1)
		level := txq.ToFeeLevel(fee, baseFee)
		require.Equal(t, txq.FeeLevel(25), level) // 1*256/10 = 25
		drops := level.ToDrops(baseFee)
		require.Equal(t, uint64(0), drops) // 25*10/256 = 0 (truncated)
	})
}

// TestFeeScaling_NoLoad_10xBaseFee verifies identity scaling with 10x base fee.
// Reference: rippled LoadFeeTrack_test.cpp lines 53-69 (fees.base = reference_fee * 10)
func TestFeeScaling_NoLoad_10xBaseFee(t *testing.T) {
	const baseFee = uint64(100) // 10x reference fee

	t.Run("zero fee scales to zero", func(t *testing.T) {
		level := txq.ToFeeLevel(0, baseFee)
		drops := level.ToDrops(baseFee)
		require.Equal(t, uint64(0), drops)
	})

	t.Run("large fee round-trips", func(t *testing.T) {
		fee := uint64(10000)
		level := txq.ToFeeLevel(fee, baseFee)
		drops := level.ToDrops(baseFee)
		require.Equal(t, fee, drops)
	})

	t.Run("sub-base fee truncates in round-trip", func(t *testing.T) {
		// Same truncation as default base fee: 1*256/100 = 2, 2*100/256 = 0
		fee := uint64(1)
		level := txq.ToFeeLevel(fee, baseFee)
		require.Equal(t, txq.FeeLevel(2), level) // 1*256/100 = 2
		drops := level.ToDrops(baseFee)
		require.Equal(t, uint64(0), drops) // 2*100/256 = 0 (truncated)
	})
}
