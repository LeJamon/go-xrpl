package invariants

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/keylet"
)

// XLS-66 lending invariants, ported from rippled 3.1.0 InvariantCheck.cpp
// (ValidLoanBroker, ValidLoan). Both are enforcement-gated on
// featureLendingProtocol: while it is off the objects cannot exist, so the checks
// are inert.
//
// The 3.1.0 ValidLoanBroker also asserts CoverAvailable >= accountHolds(pseudo,
// asset) — a lower bound revised into an exact match by the 3.1.3 fixCleanup.
// That cross-asset balance comparison is deferred; the numeric and structural
// checks below are the safe subset.

const lsfLoanOverpaymentFlag uint32 = 0x00040000

// decodeEntry decodes a serialized SLE into its field map.
func decodeEntry(data []byte) (map[string]any, error) {
	return binarycodec.Decode(hex.EncodeToString(data))
}

// numFieldIsZero reports whether a NUMBER field is absent or zero.
func numFieldIsZero(fields map[string]any, key string) bool {
	v, ok := fields[key].(string)
	return !ok || v == "" || v == "0"
}

// numFieldIsNegative reports whether a NUMBER field is present and negative (the
// codec renders negatives with a leading '-').
func numFieldIsNegative(fields map[string]any, key string) bool {
	v, ok := fields[key].(string)
	return ok && strings.HasPrefix(v, "-")
}

// u32Field reads a UInt32 field, tolerating the codec's numeric representations.
func u32Field(fields map[string]any, key string) uint32 {
	switch v := fields[key].(type) {
	case uint32:
		return v
	case int:
		return uint32(v)
	case float64:
		return uint32(v)
	default:
		return 0
	}
}

func lendingViolation(name, msg string) *InvariantViolation {
	return &InvariantViolation{Name: name, Message: msg}
}

// checkValidLoan enforces the ValidLoan invariant on every modified/created Loan.
func checkValidLoan(entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureLendingProtocol) {
		return nil
	}
	for _, e := range entries {
		if e.EntryType != "Loan" || e.After == nil {
			continue
		}
		after, err := decodeEntry(e.After)
		if err != nil {
			return lendingViolation("ValidLoan", fmt.Sprintf("could not decode Loan: %v", err))
		}
		paymentRemaining := u32Field(after, "PaymentRemaining")
		allZero := numFieldIsZero(after, "TotalValueOutstanding") &&
			numFieldIsZero(after, "PrincipalOutstanding") &&
			numFieldIsZero(after, "ManagementFeeOutstanding")
		if paymentRemaining == 0 && !allZero {
			return lendingViolation("ValidLoan", "loan with zero payments remaining is not paid off")
		}
		if paymentRemaining != 0 && allZero {
			return lendingViolation("ValidLoan", "loan with payments remaining is fully paid off")
		}
		if e.Before != nil {
			if before, berr := decodeEntry(e.Before); berr == nil {
				if (u32Field(before, "Flags") & lsfLoanOverpaymentFlag) != (u32Field(after, "Flags") & lsfLoanOverpaymentFlag) {
					return lendingViolation("ValidLoan", "loan overpayment flag changed")
				}
			}
		}
		for _, f := range []string{"LoanServiceFee", "LatePaymentFee", "ClosePaymentFee", "PrincipalOutstanding", "TotalValueOutstanding", "ManagementFeeOutstanding"} {
			if numFieldIsNegative(after, f) {
				return lendingViolation("ValidLoan", f+" is negative")
			}
		}
	}
	return nil
}

// checkValidLoanBroker enforces the numeric and structural ValidLoanBroker
// checks on every modified/created LoanBroker.
func checkValidLoanBroker(entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureLendingProtocol) {
		return nil
	}
	for _, e := range entries {
		if e.EntryType != "LoanBroker" || e.After == nil {
			continue
		}
		after, err := decodeEntry(e.After)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode LoanBroker: %v", err))
		}
		if numFieldIsNegative(after, "DebtTotal") {
			return lendingViolation("ValidLoanBroker", "debt total is negative")
		}
		if numFieldIsNegative(after, "CoverAvailable") {
			return lendingViolation("ValidLoanBroker", "cover available is negative")
		}
		if e.Before != nil {
			if before, berr := decodeEntry(e.Before); berr == nil {
				if u32Field(before, "LoanSequence") > u32Field(after, "LoanSequence") {
					return lendingViolation("ValidLoanBroker", "loan sequence number decreased")
				}
			}
		}
		vaultID, ok := after["VaultID"].(string)
		if !ok {
			return lendingViolation("ValidLoanBroker", "loan broker has no vault ID")
		}
		vid, verr := hexDecode32(vaultID)
		if verr != nil {
			return lendingViolation("ValidLoanBroker", "loan broker vault ID is malformed")
		}
		if data, rerr := view.Read(keylet.VaultByID(vid)); rerr != nil || data == nil {
			return lendingViolation("ValidLoanBroker", "loan broker vault ID is invalid")
		}
	}
	return nil
}

// hexDecode32 decodes a 64-char hex string to a [32]byte.
func hexDecode32(s string) ([32]byte, error) {
	var h [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return h, err
	}
	if len(b) != 32 {
		return h, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(h[:], b)
	return h, nil
}
