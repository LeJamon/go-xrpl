package state

import (
	"bytes"
	"fmt"
)

// CompareAccountIDs compares two 20-byte account IDs lexicographically.
// Returns -1, 0, or 1.
func CompareAccountIDs(a, b [20]byte) int {
	return bytes.Compare(a[:], b[:])
}

// CompareAccountIDsForLine compares two account IDs for trust line ordering.
// The "low" account is the one that sorts first lexicographically.
func CompareAccountIDsForLine(a, b [20]byte) int {
	return bytes.Compare(a[:], b[:])
}

// FormatDrops formats a uint64 drops value as a string
func FormatDrops(drops uint64) string {
	return fmt.Sprintf("%d", drops)
}

// SubtractAmount subtracts b from a, returning the result.
// Both amounts must be the same type (both XRP or same IOU currency).
func SubtractAmount(a, b Amount) Amount {
	if a.IsNative() {
		aDrops := a.Drops()
		bDrops := b.Drops()
		if aDrops >= bDrops {
			return NewXRPAmountFromInt(aDrops - bDrops)
		}
		return NewXRPAmountFromInt(0)
	}
	result, _ := a.Sub(b)
	return result
}

// ApplyTransferFee applies a transfer rate to an amount.
// transferRate is the rate as uint32 (1000000000 = no fee, 1100000000 = 10% fee).
func ApplyTransferFee(amount Amount, transferRate uint32) Amount {
	if transferRate == 0 || transferRate == 1000000000 {
		return amount
	}
	if amount.IsNative() {
		return amount // No transfer fee on XRP
	}
	return amount.MulRatio(transferRate, 1000000000, true)
}

// EncodeAccountIDSafe encodes a 20-byte account ID, returning empty string on error
func EncodeAccountIDSafe(accountID [20]byte) string {
	s, _ := EncodeAccountID(accountID)
	return s
}

// CalculateQuality calculates the quality (exchange rate) for an offer.
// Quality = TakerPays / TakerGets
func CalculateQuality(takerPays, takerGets Amount) uint64 {
	return GetRate(takerPays, takerGets)
}
