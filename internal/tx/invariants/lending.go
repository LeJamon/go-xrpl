package invariants

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

// XLS-66 lending invariants, ported from rippled InvariantCheck.cpp
// (ValidLoanBroker, ValidLoan). Both are enforcement-gated on
// featureLendingProtocol: while it is off the objects cannot exist, so the checks
// are inert.
//
// ValidLoanBroker also compares CoverAvailable against the pseudo-account's
// holdings of the vault asset: a lower bound (>=, from 3.1.0), plus an exact upper
// bound (==) added by fixCleanup3_1_3. Pseudo-accounts carry no XRP reserve, so
// the XRP holdings are the full balance (rippled xrpLiquid + isPseudoAccount).

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

func checkValidLoanBroker(entries []InvariantEntry, view ReadView, rules *amendment.Rules, numberContexts ...state.NumberContext) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureLendingProtocol) {
		return nil
	}
	numberContext := tx.NumberContextForRules(rules)
	if len(numberContexts) > 0 {
		numberContext = numberContexts[0]
	}

	type brokerState struct {
		before []byte
		after  []byte
	}

	brokers := make(map[[32]byte]brokerState)
	var unkeyedBrokers []brokerState
	var lines, mpts [][]byte
	addBroker := func(id [32]byte) {
		if id == ([32]byte{}) {
			return
		}
		if _, ok := brokers[id]; !ok {
			brokers[id] = brokerState{}
		}
	}

	for _, e := range entries {
		if e.After == nil {
			continue
		}
		switch e.EntryType {
		case "LoanBroker":
			broker := brokerState{before: e.Before, after: e.After}
			if e.Key == ([32]byte{}) {
				unkeyedBrokers = append(unkeyedBrokers, broker)
			} else {
				brokers[e.Key] = broker
			}
		case "AccountRoot":
			account, err := state.ParseAccountRoot(e.After)
			if err != nil {
				return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode AccountRoot: %v", err))
			}
			if account.HasLoanBrokerID() {
				addBroker(account.LoanBrokerID)
			}
		case "RippleState":
			lines = append(lines, e.After)
		case "MPToken":
			mpts = append(mpts, e.After)
		}
	}

	addBrokerForAccount := func(accountID [20]byte) *InvariantViolation {
		data, err := view.Read(keylet.Account(accountID))
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not read account: %v", err))
		}
		if data == nil {
			return nil
		}
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode AccountRoot: %v", err))
		}
		if account.HasLoanBrokerID() {
			addBroker(account.LoanBrokerID)
		}
		return nil
	}

	for _, data := range lines {
		line, err := state.ParseRippleState(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode RippleState: %v", err))
		}
		for _, address := range []string{line.LowLimit.Issuer, line.HighLimit.Issuer} {
			accountID, err := state.DecodeAccountID(address)
			if err != nil {
				return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode trust line account: %v", err))
			}
			if violation := addBrokerForAccount(accountID); violation != nil {
				return violation
			}
		}
	}

	for _, data := range mpts {
		token, err := state.ParseMPToken(data)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode MPToken: %v", err))
		}
		if violation := addBrokerForAccount(token.Account); violation != nil {
			return violation
		}
	}

	checkBroker := func(beforeData, afterData []byte) *InvariantViolation {
		after, err := decodeEntry(afterData)
		if err != nil {
			return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not decode LoanBroker: %v", err))
		}
		if numFieldIsNegative(after, "DebtTotal") {
			return lendingViolation("ValidLoanBroker", "debt total is negative")
		}
		if numFieldIsNegative(after, "CoverAvailable") {
			return lendingViolation("ValidLoanBroker", "cover available is negative")
		}
		if beforeData != nil {
			if before, berr := decodeEntry(beforeData); berr == nil {
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
		vaultData, rerr := view.Read(keylet.VaultByID(vid))
		if rerr != nil || vaultData == nil {
			return lendingViolation("ValidLoanBroker", "loan broker vault ID is invalid")
		}

		// CoverAvailable must match the pseudo-account's holdings of the vault
		// asset: a lower bound, plus (post-fixCleanup3_1_3) an exact upper bound.
		// A deleted broker has After==nil and is skipped above, matching rippled's
		// ttLOAN_BROKER_DELETE exclusion from the upper bound.
		pseudoAddr, aok := after["Account"].(string)
		if !aok {
			return lendingViolation("ValidLoanBroker", "loan broker has no account")
		}
		pseudoID, aerr := state.DecodeAccountID(pseudoAddr)
		if aerr != nil {
			return lendingViolation("ValidLoanBroker", "loan broker account is malformed")
		}
		pseudoBalance, phOK := vault.PseudoAssetHoldsWithNumberContext(view, pseudoID, vaultData, numberContext)
		if !phOK {
			return lendingViolation("ValidLoanBroker", "could not read pseudo-account asset balance")
		}
		coverAvailable := coverAvailableNumber(after, numberContext)
		if coverAvailable.Cmp(pseudoBalance) < 0 {
			return lendingViolation("ValidLoanBroker", "cover available is less than pseudo-account asset balance")
		}
		if rules.Enabled(amendment.FeatureFixCleanup3_1_3) && coverAvailable.Cmp(pseudoBalance) > 0 {
			return lendingViolation("ValidLoanBroker", "cover available is greater than pseudo-account asset balance")
		}
		return nil
	}

	for _, broker := range unkeyedBrokers {
		if violation := checkBroker(broker.before, broker.after); violation != nil {
			return violation
		}
	}
	for brokerID, broker := range brokers {
		after := broker.after
		if after == nil {
			var err error
			after, err = view.Read(keylet.LoanBrokerByID(brokerID))
			if err != nil {
				return lendingViolation("ValidLoanBroker", fmt.Sprintf("could not read LoanBroker: %v", err))
			}
			if after == nil {
				return lendingViolation("ValidLoanBroker", "loan broker is missing")
			}
		}
		if violation := checkBroker(broker.before, after); violation != nil {
			return violation
		}
	}
	return nil
}

// coverAvailableNumber parses the LoanBroker's CoverAvailable NUMBER field; an
// absent or malformed value is treated as zero.
func coverAvailableNumber(fields map[string]any, numberContext state.NumberContext) state.XRPLNumber {
	s, ok := fields["CoverAvailable"].(string)
	if !ok {
		return numberContext.Int(0)
	}
	n, _ := vault.ParseLedgerNumberWithNumberContext(s, numberContext)
	return n
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
