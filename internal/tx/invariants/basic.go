package invariants

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// checkXRPBalances verifies that every AccountRoot balance, in both its before
// and after image, is within [0, InitialXRP]. rippled's XRPBalanceChecks
// inspects both images of every touched AccountRoot, including deletes (where
// the erased SLE is passed as the "after").
// Reference: rippled InvariantCheck.cpp — XRPBalanceChecks (lines 178-201).
func checkXRPBalances(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "AccountRoot" {
			continue
		}
		// Inspect both images. For a delete, CollectEntries leaves After nil and
		// keeps the erased SLE in Before, which rippled's visit passes as the
		// "after" — so checking Before covers the deleted account's balance too.
		for _, data := range [][]byte{e.Before, e.After} {
			if data == nil {
				continue
			}
			if v := checkXRPBalanceImage(data); v != nil {
				return v
			}
		}
	}
	return nil
}

// checkXRPBalanceImage validates a single AccountRoot image's Balance. A parse
// failure on bytes go-xrpl serialized moments earlier signals a serialization
// round-trip bug and is a hard invariant failure, mirroring rippled where a bad
// field access in visitEntry throws and ApplyContext's catch-all converts it to
// tecINVARIANT_FAILED. Balance is uint64 so the negative-balance branch of
// rippled's isBad cannot trigger here.
func checkXRPBalanceImage(data []byte) *InvariantViolation {
	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		return &InvariantViolation{
			Name:    "XRPBalanceChecks",
			Message: fmt.Sprintf("could not parse AccountRoot SLE: %v", err),
		}
	}
	if acct.Balance > InitialXRP {
		return &InvariantViolation{
			Name:    "XRPBalanceChecks",
			Message: fmt.Sprintf("account balance %d exceeds InitialXRP (%d)", acct.Balance, InitialXRP),
		}
	}
	return nil
}

// checkXRPNotCreated verifies that the net XRP change across all touched entries
// equals at most -fee (XRP can only decrease, never increase, per transaction).
// Reference: rippled InvariantCheck.cpp — XRPNotCreated
func checkXRPNotCreated(result Result, fee uint64, entries []InvariantEntry) *InvariantViolation {
	// Sum of (after_balance - before_balance) across AccountRoot entries.
	// Using int64 arithmetic; values are at most ~10^17 drops which fits.
	var netChange int64

	for _, e := range entries {
		switch e.EntryType {
		case "AccountRoot":
			var before, after uint64
			if e.Before != nil {
				acct, err := state.ParseAccountRoot(e.Before)
				if err != nil {
					return xrpNotCreatedParseViolation("AccountRoot", err)
				}
				before = acct.Balance
			}
			if e.After != nil {
				acct, err := state.ParseAccountRoot(e.After)
				if err != nil {
					return xrpNotCreatedParseViolation("AccountRoot", err)
				}
				after = acct.Balance
			}
			netChange += int64(after) - int64(before)

		case "Escrow":
			// Escrow holds XRP in escrow — count as a balance change.
			// IOU escrows (TokenEscrow amendment) are skipped because they
			// don't hold XRP drops.
			// Reference: rippled InvariantCheck.cpp lines 111-113, 133-135:
			//   if (isXRP((*before)[sfAmount])) drops_ -= ...
			var before, after uint64
			if e.Before != nil {
				esc, err := state.ParseEscrow(e.Before)
				if err != nil {
					return xrpNotCreatedParseViolation("Escrow", err)
				}
				if esc.IsXRP {
					before = esc.Amount
				}
			}
			if e.After != nil {
				esc, err := state.ParseEscrow(e.After)
				if err != nil {
					return xrpNotCreatedParseViolation("Escrow", err)
				}
				if esc.IsXRP {
					after = esc.Amount
				}
			}
			netChange += int64(after) - int64(before)

		case "PayChannel":
			// PayChannel holds XRP as Amount - Balance (total minus claimed).
			// Reference: rippled InvariantCheck.cpp:107-131
			var before, after uint64
			if e.Before != nil {
				pc, err := state.ParsePayChannel(e.Before)
				if err != nil {
					return xrpNotCreatedParseViolation("PayChannel", err)
				}
				before = pc.Amount - pc.Balance
			}
			if e.After != nil && !e.IsDelete {
				pc, err := state.ParsePayChannel(e.After)
				if err != nil {
					return xrpNotCreatedParseViolation("PayChannel", err)
				}
				after = pc.Amount - pc.Balance
			}
			netChange += int64(after) - int64(before)
		}
	}

	// Net XRP change must equal exactly -fee. A positive net change means XRP
	// was created out of thin air. A net change more negative than -fee means
	// XRP was burned beyond what the fee accounts for — also a violation, since
	// only the fee should destroy XRP.
	// Reference: rippled InvariantCheck.cpp:153-166.
	if netChange > 0 {
		return &InvariantViolation{
			Name:    "XRPNotCreated",
			Message: fmt.Sprintf("net XRP change +%d drops: XRP was created (fee=%d)", netChange, fee),
		}
	}
	if -netChange != int64(fee) {
		return &InvariantViolation{
			Name:    "XRPNotCreated",
			Message: fmt.Sprintf("net XRP change of %d drops doesn't match fee %d", netChange, fee),
		}
	}
	return nil
}

// xrpNotCreatedParseViolation reports a parse failure of an XRP-bearing SLE that
// XRPNotCreated must account for. A decode failure on bytes go-xrpl serialized
// moments earlier would corrupt netChange (defaulting the balance to 0), so it
// is treated as a hard invariant failure — mirroring rippled, where the field
// access throws and ApplyContext's catch-all converts it to tecINVARIANT_FAILED.
func xrpNotCreatedParseViolation(entryType string, err error) *InvariantViolation {
	return &InvariantViolation{
		Name:    "XRPNotCreated",
		Message: fmt.Sprintf("could not parse %s SLE: %v", entryType, err),
	}
}

// checkAccountRootsNotDeleted verifies that AccountRoot entries are only deleted
// by allowed transaction types.
// Reference: rippled InvariantCheck.cpp — AccountRootsNotDeleted (lines 370-412)
func checkAccountRootsNotDeleted(txType string, result Result, entries []InvariantEntry) *InvariantViolation {
	deletedCount := 0
	for _, e := range entries {
		if e.EntryType == "AccountRoot" && e.IsDelete {
			deletedCount++
		}
	}
	if deletedCount == 0 {
		return nil
	}

	if result == TesSUCCESS {
		// A successful AccountDelete/AMMDelete/VaultDelete MUST delete exactly
		// one account root. VaultDelete removes the vault's pseudo-account.
		// Reference: rippled InvariantCheck.cpp:382-385.
		switch txType {
		case "AccountDelete", "AMMDelete", "VaultDelete":
			if deletedCount == 1 {
				return nil
			}
			return &InvariantViolation{
				Name:    "AccountRootsNotDeleted",
				Message: fmt.Sprintf("%s must delete exactly 1 AccountRoot, got %d", txType, deletedCount),
			}
		// A successful AMMWithdraw/AMMClawback MAY delete one account root
		// (when total AMM LP Tokens balance goes to 0).
		case "AMMWithdraw", "AMMClawback":
			if deletedCount <= 1 {
				return nil
			}
			return &InvariantViolation{
				Name:    "AccountRootsNotDeleted",
				Message: fmt.Sprintf("%s may delete at most 1 AccountRoot, got %d", txType, deletedCount),
			}
		}
	}

	return &InvariantViolation{
		Name:    "AccountRootsNotDeleted",
		Message: fmt.Sprintf("AccountRoot deleted by %s (count=%d); not allowed", txType, deletedCount),
	}
}

// checkLedgerEntryTypesMatch verifies two things:
//  1. If both before and after exist for an entry, their ledger entry types
//     must match.
//  2. Any entry carrying an "after" image must be a known valid type.
//
// Every go-xrpl SLE is serialized with LedgerEntryType first (header byte 0x11),
// so a missing type code (EntryTypeCode == 0) means the bytes are not a
// well-formed SLE — including NFTokenPage entries, whose *ledger keys* are
// unhashed but whose *serialized content* still leads with LedgerEntryType. A
// code of 0 is therefore a hard failure, mirroring rippled where after->getType()
// throwing is caught as tecINVARIANT_FAILED.
// Reference: rippled InvariantCheck.cpp — LedgerEntryTypesMatch (lines 505-576).
func checkLedgerEntryTypesMatch(entries []InvariantEntry) *InvariantViolation {
	typeMismatch := false
	invalidTypeAdded := false

	for _, e := range entries {
		// Check type mismatch between before and after.
		if e.Before != nil && e.After != nil {
			beforeCode := state.EntryTypeCode(e.Before)
			afterCode := state.EntryTypeCode(e.After)
			if beforeCode == 0 || afterCode == 0 {
				return &InvariantViolation{
					Name:    "LedgerEntryTypesMatch",
					Message: "could not extract ledger entry type from SLE",
				}
			}
			if beforeCode != afterCode {
				typeMismatch = true
			}
		}

		// Check that any entry with an "after" is a valid type.
		if e.After != nil {
			afterCode := state.EntryTypeCode(e.After)
			if afterCode == 0 {
				return &InvariantViolation{
					Name:    "LedgerEntryTypesMatch",
					Message: "could not extract ledger entry type from created SLE",
				}
			}
			afterName := state.EntryTypeName(afterCode)
			if !validLedgerEntryTypes[afterName] {
				invalidTypeAdded = true
			}
		}
	}

	if typeMismatch {
		return &InvariantViolation{
			Name:    "LedgerEntryTypesMatch",
			Message: "ledger entry type mismatch",
		}
	}

	if invalidTypeAdded {
		return &InvariantViolation{
			Name:    "LedgerEntryTypesMatch",
			Message: "invalid ledger entry type added",
		}
	}

	return nil
}

// checkValidNewAccountRoot verifies that new AccountRoot entries are only created
// by a permitted transaction type, that at most one is created per transaction,
// and that the new account has the expected starting Sequence (and Flags, for
// pseudo-accounts).
// Reference: rippled InvariantCheck.cpp — ValidNewAccountRoot (lines 930-1013).
func checkValidNewAccountRoot(txType string, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	createdCount := 0
	var newEntry []byte
	for _, e := range entries {
		if e.EntryType == "AccountRoot" && !e.IsDelete && e.Before == nil {
			createdCount++
			newEntry = e.After
		}
	}
	if createdCount == 0 {
		return nil
	}
	if createdCount > 1 {
		return &InvariantViolation{
			Name:    "ValidNewAccountRoot",
			Message: fmt.Sprintf("multiple AccountRoot entries created in one transaction (count=%d)", createdCount),
		}
	}

	// Only a successful transaction of a permitted type may create an
	// AccountRoot.
	permitted := false
	switch txType {
	case "Payment", "AMMCreate", "VaultCreate", "XChainAddClaimAttestation", "XChainAddAccountCreateAttestation":
		permitted = result == TesSUCCESS
	}
	if !permitted {
		return &InvariantViolation{
			Name:    "ValidNewAccountRoot",
			Message: fmt.Sprintf("account root created illegally by %s", txType),
		}
	}

	seq, flags, pseudo, ok := extractNewAccountRootFields(newEntry)
	if !ok {
		return &InvariantViolation{
			Name:    "ValidNewAccountRoot",
			Message: "could not parse newly created AccountRoot",
		}
	}

	// A pseudo-account (AMMID or VaultID set) may only be created by
	// AMMCreate or VaultCreate. The flag is gated on featureSingleAssetVault:
	// before that amendment, sfVaultID does not exist as a serialized field
	// and pseudo-account semantics are not enforced.
	if pseudo && rules != nil && rules.Enabled(amendment.FeatureSingleAssetVault) {
		if txType != "AMMCreate" && txType != "VaultCreate" {
			return &InvariantViolation{
				Name:    "ValidNewAccountRoot",
				Message: fmt.Sprintf("pseudo-account created by a wrong transaction type %s", txType),
			}
		}
	} else {
		pseudo = false
	}

	var startingSeq uint32
	switch {
	case pseudo:
		startingSeq = 0
	case rules != nil && rules.Enabled(amendment.FeatureDeletableAccounts):
		if view != nil {
			startingSeq = view.LedgerSeq()
		}
	default:
		startingSeq = 1
	}
	if seq != startingSeq {
		return &InvariantViolation{
			Name:    "ValidNewAccountRoot",
			Message: fmt.Sprintf("account created with wrong starting sequence %d (want %d)", seq, startingSeq),
		}
	}

	if pseudo {
		const expected = LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth
		if flags != expected {
			return &InvariantViolation{
				Name:    "ValidNewAccountRoot",
				Message: fmt.Sprintf("pseudo-account created with wrong flags 0x%08x", flags),
			}
		}
	}
	return nil
}

// extractNewAccountRootFields scans the binary SLE of a newly created
// AccountRoot and returns its Sequence, Flags, and whether the entry is a
// pseudo-account (sfAMMID or sfVaultID set). Returns ok=false if the binary
// is malformed, if Sequence or Flags is missing, or if any UInt32 field code
// appears more than once (which the XRPL STObject codec disallows).
func extractNewAccountRootFields(data []byte) (seq, flags uint32, pseudo, ok bool) {
	var seqSeen, flagsSeen, dup bool
	seenUint32 := make(map[int]struct{}, 4)
	walkErr := state.WalkFields(data, func(f state.Field) error {
		switch f.TypeCode {
		case 2: // UInt32
			if _, seen := seenUint32[f.FieldCode]; seen {
				dup = true
				return errStopWalk
			}
			seenUint32[f.FieldCode] = struct{}{}
			value := binary.BigEndian.Uint32(f.Value)
			switch f.FieldCode {
			case 2: // Flags
				flags = value
				flagsSeen = true
			case 4: // Sequence
				seq = value
				seqSeen = true
			}
		case 5: // Hash256 — sfAMMID (14) or sfVaultID (35) marks a pseudo-account
			if f.FieldCode == 14 || f.FieldCode == 35 {
				for _, b := range f.Value {
					if b != 0 {
						pseudo = true
						break
					}
				}
			}
		}
		return nil
	})
	if dup || (walkErr != nil && walkErr != errStopWalk) {
		return 0, 0, false, false
	}
	if !seqSeen || !flagsSeen {
		return 0, 0, false, false
	}
	return seq, flags, pseudo, true
}

// errStopWalk is a sentinel returned from a WalkFields callback to halt
// iteration early once a checker has what it needs (or has detected a
// disqualifying condition). It is never treated as a hard parse failure.
var errStopWalk = errors.New("stop walk")

// AccountRoot flag bits used by ValidNewAccountRoot's pseudo-account check.
const (
	LsfDisableMaster = entry.LsfDisableMaster
	LsfDefaultRipple = entry.LsfDefaultRipple
	LsfDepositAuth   = entry.LsfDepositAuth
)

// checkTransactionFee verifies that the fee charged is non-negative, does not
// exceed the total XRP supply, and does not exceed what the transaction declared.
// Reference: rippled InvariantCheck.cpp — TransactionFeeCheck (lines 39-83)
func checkTransactionFee(fee uint64, txDeclaredFee uint64) *InvariantViolation {
	// fee is uint64 so always >= 0; skip the negative check.

	// Fee must not be greater than or equal to the entire XRP supply.
	if fee >= InitialXRP {
		return &InvariantViolation{
			Name:    "TransactionFeeCheck",
			Message: fmt.Sprintf("fee paid exceeds system limit: %d", fee),
		}
	}

	// Fee charged must not exceed what the transaction authorized.
	if fee > txDeclaredFee {
		return &InvariantViolation{
			Name:    "TransactionFeeCheck",
			Message: fmt.Sprintf("fee paid is %d exceeds fee specified in transaction", fee),
		}
	}

	return nil
}
