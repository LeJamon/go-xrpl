package testing

import (
	"fmt"
	"math"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	"github.com/stretchr/testify/require"
)

// RequireBalance asserts that an account has the expected XRP balance in drops.
// This is a convenience wrapper around require.Equal for balance checks.
func RequireBalance(t *testing.T, env *TestEnv, acc *Account, expected uint64) {
	t.Helper()
	actual := env.Balance(acc)
	require.Equal(t, expected, actual,
		"Account %s balance mismatch: expected %d drops, got %d drops",
		acc.Name, expected, actual)
}

// RequireBalanceXRP asserts that an account has the expected XRP balance.
// The expected amount is in whole XRP units (e.g., 100 XRP, not drops).
func RequireBalanceXRP(t *testing.T, env *TestEnv, acc *Account, expectedXRP int64) {
	t.Helper()
	expected := uint64(XRP(expectedXRP))
	RequireBalance(t, env, acc, expected)
}

// RequireBalanceApprox asserts that an account balance is within a tolerance of the expected value.
// This is useful when fees or other small adjustments affect the exact balance.
func RequireBalanceApprox(t *testing.T, env *TestEnv, acc *Account, expected uint64, tolerance uint64) {
	t.Helper()
	actual := env.Balance(acc)
	diff := int64(actual) - int64(expected)
	if diff < 0 {
		diff = -diff
	}
	require.LessOrEqual(t, uint64(diff), tolerance,
		"Account %s balance mismatch: expected %d +/- %d drops, got %d drops (diff: %d)",
		acc.Name, expected, tolerance, actual, diff)
}

// RequireTxSuccess asserts that a transaction result indicates success.
func RequireTxSuccess(t *testing.T, result TxResult) {
	t.Helper()
	require.True(t, result.Success,
		"Expected transaction success, got %s: %s", result.Code, result.Message)
	require.Equal(t, "tesSUCCESS", result.Code,
		"Expected tesSUCCESS, got %s: %s", result.Code, result.Message)
}

// RequireTxFail asserts that a transaction result indicates failure with a specific code.
func RequireTxFail(t *testing.T, result TxResult, expectedCode string) {
	t.Helper()
	require.False(t, result.Success,
		"Expected transaction failure with code %s, but transaction succeeded", expectedCode)
	require.Equal(t, expectedCode, result.Code,
		"Expected failure code %s, got %s: %s", expectedCode, result.Code, result.Message)
}

// RequireTxClaimed asserts that a transaction had its fee claimed but wasn't applied.
// This corresponds to "tec" result codes.
func RequireTxClaimed(t *testing.T, result TxResult, expectedCode string) {
	t.Helper()
	require.True(t, result.IsClaimed(),
		"Expected claimed transaction with code %s, got %s", expectedCode, result.Code)
	require.Equal(t, expectedCode, result.Code,
		"Expected claimed code %s, got %s: %s", expectedCode, result.Code, result.Message)
}

// RequireAccountExists asserts that an account exists in the ledger.
func RequireAccountExists(t *testing.T, env *TestEnv, acc *Account) {
	t.Helper()
	require.True(t, env.Exists(acc),
		"Expected account %s to exist, but it does not", acc.Name)
}

// RequireAccountNotExists asserts that an account does not exist in the ledger.
func RequireAccountNotExists(t *testing.T, env *TestEnv, acc *Account) {
	t.Helper()
	require.False(t, env.Exists(acc),
		"Expected account %s to not exist, but it does", acc.Name)
}

// RequireSequence asserts that an account has the expected sequence number.
func RequireSequence(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	actual := env.Seq(acc)
	require.Equal(t, expected, actual,
		"Account %s sequence mismatch: expected %d, got %d", acc.Name, expected, actual)
}

// RequireOwnerCount asserts that an account has the expected owner count.
func RequireOwnerCount(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	info := env.AccountInfo(acc)
	require.NotNil(t, info, "Account %s does not exist", acc.Name)
	require.Equal(t, expected, info.OwnerCount,
		"Account %s owner count mismatch: expected %d, got %d", acc.Name, expected, info.OwnerCount)
}

// AssertBalanceChange runs a function and asserts the expected balance change.
// The change can be positive (increase) or negative (decrease).
func AssertBalanceChange(t *testing.T, env *TestEnv, acc *Account, expectedChange int64, fn func()) {
	t.Helper()
	before := env.Balance(acc)
	fn()
	after := env.Balance(acc)

	actualChange := int64(after) - int64(before)
	require.Equal(t, expectedChange, actualChange,
		"Account %s balance change mismatch: expected %d drops change, got %d drops change (before: %d, after: %d)",
		acc.Name, expectedChange, actualChange, before, after)
}

// AssertNoBalanceChange runs a function and asserts the balance stays the same.
func AssertNoBalanceChange(t *testing.T, env *TestEnv, acc *Account, fn func()) {
	t.Helper()
	AssertBalanceChange(t, env, acc, 0, fn)
}

// TxResultCode is a type alias for transaction result codes for better documentation.
type TxResultCode = string

// Transaction result codes for use in assertions.
const (
	// Success
	TesSUCCESS TxResultCode = "tesSUCCESS"

	// Claimed (tec) - fee claimed but transaction not applied
	TecCLAIM                 TxResultCode = "tecCLAIM"
	TecPATH_PARTIAL          TxResultCode = "tecPATH_PARTIAL"
	TecUNFUNDED_ADD          TxResultCode = "tecUNFUNDED_ADD"
	TecUNFUNDED_OFFER        TxResultCode = "tecUNFUNDED_OFFER"
	TecUNFUNDED_PAYMENT      TxResultCode = "tecUNFUNDED_PAYMENT"
	TecFAILED_PROCESSING     TxResultCode = "tecFAILED_PROCESSING"
	TecDIR_FULL              TxResultCode = "tecDIR_FULL"
	TecINSUF_RESERVE_LINE    TxResultCode = "tecINSUF_RESERVE_LINE"
	TecINSUF_RESERVE_OFFER   TxResultCode = "tecINSUF_RESERVE_OFFER"
	TecNO_DST                TxResultCode = "tecNO_DST"
	TecNO_DST_INSUF_XRP      TxResultCode = "tecNO_DST_INSUF_XRP"
	TecNO_LINE_INSUF_RESERVE TxResultCode = "tecNO_LINE_INSUF_RESERVE"
	TecNO_LINE_REDUNDANT     TxResultCode = "tecNO_LINE_REDUNDANT"
	TecPATH_DRY              TxResultCode = "tecPATH_DRY"
	TecUNFUNDED              TxResultCode = "tecUNFUNDED"
	TecNO_ALTERNATIVE_KEY    TxResultCode = "tecNO_ALTERNATIVE_KEY"
	TecNO_REGULAR_KEY        TxResultCode = "tecNO_REGULAR_KEY"
	TecOWNERS                TxResultCode = "tecOWNERS"
	TecNO_ISSUER             TxResultCode = "tecNO_ISSUER"
	TecNO_AUTH               TxResultCode = "tecNO_AUTH"
	TecNO_LINE               TxResultCode = "tecNO_LINE"
	TecINSUFF_FEE            TxResultCode = "tecINSUFF_FEE"
	TecFROZEN                TxResultCode = "tecFROZEN"
	TecNO_TARGET             TxResultCode = "tecNO_TARGET"
	TecNO_PERMISSION         TxResultCode = "tecNO_PERMISSION"
	TecNO_ENTRY              TxResultCode = "tecNO_ENTRY"
	TecINSUFFICIENT_RESERVE  TxResultCode = "tecINSUFFICIENT_RESERVE"
	TecNEED_MASTER_KEY       TxResultCode = "tecNEED_MASTER_KEY"
	TecDST_TAG_NEEDED        TxResultCode = "tecDST_TAG_NEEDED"
	TecINTERNAL              TxResultCode = "tecINTERNAL"
	TecOVERSIZE              TxResultCode = "tecOVERSIZE"
	TecCRYPTOCONDITION_ERROR TxResultCode = "tecCRYPTOCONDITION_ERROR"
	TecINVARIANT_FAILED      TxResultCode = "tecINVARIANT_FAILED"
	TecDUPLICATE             TxResultCode = "tecDUPLICATE"
	TecEXPIRED               TxResultCode = "tecEXPIRED"
	TecHAS_OBLIGATIONS       TxResultCode = "tecHAS_OBLIGATIONS"
	TecINSUFFICIENT_FUNDS    TxResultCode = "tecINSUFFICIENT_FUNDS"
	TecOBJECT_NOT_FOUND      TxResultCode = "tecOBJECT_NOT_FOUND"
	TecLOCKED                TxResultCode = "tecLOCKED"
	TecKILLED                TxResultCode = "tecKILLED"

	// Failure (tef) - transaction not applied, retry possible
	TefFAILURE          TxResultCode = "tefFAILURE"
	TefALREADY          TxResultCode = "tefALREADY"
	TefBAD_ADD_AUTH     TxResultCode = "tefBAD_ADD_AUTH"
	TefBAD_AUTH         TxResultCode = "tefBAD_AUTH"
	TefBAD_LEDGER       TxResultCode = "tefBAD_LEDGER"
	TefCREATED          TxResultCode = "tefCREATED"
	TefEXCEPTION        TxResultCode = "tefEXCEPTION"
	TefINTERNAL         TxResultCode = "tefINTERNAL"
	TefNO_AUTH_REQUIRED TxResultCode = "tefNO_AUTH_REQUIRED"
	TefPAST_SEQ         TxResultCode = "tefPAST_SEQ"
	TefWRONG_PRIOR      TxResultCode = "tefWRONG_PRIOR"
	TefMASTER_DISABLED  TxResultCode = "tefMASTER_DISABLED"
	TefMAX_LEDGER       TxResultCode = "tefMAX_LEDGER"
	TefBAD_SIGNATURE    TxResultCode = "tefBAD_SIGNATURE"
	TefBAD_QUORUM       TxResultCode = "tefBAD_QUORUM"
	TefINVARIANT_FAILED TxResultCode = "tefINVARIANT_FAILED"
	TefTOO_BIG          TxResultCode = "tefTOO_BIG"

	// Retry (ter) - not applied, retry later
	TerRETRY       TxResultCode = "terRETRY"
	TerFUNDS_SPENT TxResultCode = "terFUNDS_SPENT"
	TerINSUF_FEE_B TxResultCode = "terINSUF_FEE_B"
	TerNO_ACCOUNT  TxResultCode = "terNO_ACCOUNT"
	TerNO_AUTH     TxResultCode = "terNO_AUTH"
	TerNO_LINE     TxResultCode = "terNO_LINE"
	TerOWNERS      TxResultCode = "terOWNERS"
	TerPRE_SEQ     TxResultCode = "terPRE_SEQ"
	TerLAST        TxResultCode = "terLAST"
	TerNO_RIPPLE   TxResultCode = "terNO_RIPPLE"
	TerQUEUED      TxResultCode = "terQUEUED"

	// Malformed (tem) - invalid transaction format
	TemMALFORMED          TxResultCode = "temMALFORMED"
	TemBAD_AMOUNT         TxResultCode = "temBAD_AMOUNT"
	TemBAD_CURRENCY       TxResultCode = "temBAD_CURRENCY"
	TemBAD_EXPIRATION     TxResultCode = "temBAD_EXPIRATION"
	TemBAD_FEE            TxResultCode = "temBAD_FEE"
	TemBAD_ISSUER         TxResultCode = "temBAD_ISSUER"
	TemBAD_LIMIT          TxResultCode = "temBAD_LIMIT"
	TemBAD_OFFER          TxResultCode = "temBAD_OFFER"
	TemBAD_PATH           TxResultCode = "temBAD_PATH"
	TemBAD_PATH_LOOP      TxResultCode = "temBAD_PATH_LOOP"
	TemBAD_REGKEY         TxResultCode = "temBAD_REGKEY"
	TemBAD_SEQUENCE       TxResultCode = "temBAD_SEQUENCE"
	TemBAD_SIGNATURE      TxResultCode = "temBAD_SIGNATURE"
	TemBAD_SRC_ACCOUNT    TxResultCode = "temBAD_SRC_ACCOUNT"
	TemBAD_TRANSFER_RATE  TxResultCode = "temBAD_TRANSFER_RATE"
	TemBAD_TRANSFER_FEE   TxResultCode = "temBAD_TRANSFER_FEE"
	TemDST_IS_SRC         TxResultCode = "temDST_IS_SRC"
	TemDST_NEEDED         TxResultCode = "temDST_NEEDED"
	TemINVALID            TxResultCode = "temINVALID"
	TemINVALID_FLAG       TxResultCode = "temINVALID_FLAG"
	TemREDUNDANT          TxResultCode = "temREDUNDANT"
	TemRIPPLE_EMPTY       TxResultCode = "temRIPPLE_EMPTY"
	TemDISABLED           TxResultCode = "temDISABLED"
	TemBAD_SIGNER         TxResultCode = "temBAD_SIGNER"
	TemBAD_QUORUM         TxResultCode = "temBAD_QUORUM"
	TemBAD_WEIGHT         TxResultCode = "temBAD_WEIGHT"
	TemBAD_TICK_SIZE       TxResultCode = "temBAD_TICK_SIZE"
	TemINVALID_ACCOUNT_ID  TxResultCode = "temINVALID_ACCOUNT_ID"
	TemUNCERTAIN           TxResultCode = "temUNCERTAIN"
	TemUNKNOWN             TxResultCode = "temUNKNOWN"
	TemSEQ_AND_TICKET      TxResultCode = "temSEQ_AND_TICKET"
	TemBAD_SEND_XRP_PATHS  TxResultCode = "temBAD_SEND_XRP_PATHS"
	TemBAD_SEND_XRP_MAX    TxResultCode = "temBAD_SEND_XRP_MAX"
	TemBAD_SEND_XRP_PARTIAL TxResultCode = "temBAD_SEND_XRP_PARTIAL"
	TemBAD_SEND_XRP_NO_DIRECT TxResultCode = "temBAD_SEND_XRP_NO_DIRECT"
)

// ResultCodeCategory returns the category of a result code.
func ResultCodeCategory(code string) string {
	if len(code) < 3 {
		return "unknown"
	}
	prefix := code[:3]
	switch prefix {
	case "tes":
		return "success"
	case "tec":
		return "claimed"
	case "tef":
		return "failure"
	case "ter":
		return "retry"
	case "tem":
		return "malformed"
	default:
		return "unknown"
	}
}

// FormatBalance formats a balance in drops as a human-readable string.
// For example, 1000000000 drops becomes "1000.000000 XRP".
func FormatBalance(drops uint64) string {
	xrp := float64(drops) / float64(DropsPerXRP)
	return fmt.Sprintf("%.6f XRP (%d drops)", xrp, drops)
}

// RequireLines asserts that an account has the expected number of trust lines.
// This counts RippleState entries in the account's owner directory.
func RequireLines(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	info := env.AccountInfo(acc)
	if info == nil {
		if expected == 0 {
			return // Account doesn't exist, 0 lines is correct
		}
		require.Fail(t, fmt.Sprintf("Account %s does not exist, expected %d lines", acc.Name, expected))
		return
	}
	// For now, we approximate using owner count
	// In a full implementation, we'd count only RippleState entries
	// This is a simplification - owner count includes all owned objects
	// TODO: Implement proper trust line counting by iterating owner directory
	require.Equal(t, expected, info.OwnerCount,
		"Account %s trust line count mismatch: expected %d, got %d (owner count)",
		acc.Name, expected, info.OwnerCount)
}

// RequireOffers asserts that an account has the expected number of offers.
// This counts Offer entries in the account's owner directory.
func RequireOffers(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	info := env.AccountInfo(acc)
	if info == nil {
		if expected == 0 {
			return // Account doesn't exist, 0 offers is correct
		}
		require.Fail(t, fmt.Sprintf("Account %s does not exist, expected %d offers", acc.Name, expected))
		return
	}
	dirKey := keylet.OwnerDir(acc.ID)
	var count uint32
	err := sle.DirForEach(env.ledger, dirKey, func(itemKey [32]byte) error {
		entryKey := keylet.Keylet{Key: itemKey}
		data, readErr := env.ledger.Read(entryKey)
		if readErr != nil || len(data) == 0 {
			return nil
		}
		entryType, typeErr := sle.GetLedgerEntryType(data)
		if typeErr != nil {
			return nil
		}
		// Offer type = 0x006f
		if entryType == 0x006f {
			count++
		}
		return nil
	})
	require.NoError(t, err, "Account %s: failed to iterate owner directory", acc.Name)
	require.Equal(t, expected, count,
		"Account %s offer count mismatch: expected %d, got %d",
		acc.Name, expected, count)
}

// RequireFlags asserts that an account has the expected flags set.
func RequireFlags(t *testing.T, env *TestEnv, acc *Account, expectedFlags uint32) {
	t.Helper()
	info := env.AccountInfo(acc)
	require.NotNil(t, info, "Account %s does not exist", acc.Name)
	require.Equal(t, expectedFlags, info.Flags,
		"Account %s flags mismatch: expected 0x%x, got 0x%x",
		acc.Name, expectedFlags, info.Flags)
}

// RequireFlagSet asserts that a specific flag is set on an account.
func RequireFlagSet(t *testing.T, env *TestEnv, acc *Account, flag uint32) {
	t.Helper()
	info := env.AccountInfo(acc)
	require.NotNil(t, info, "Account %s does not exist", acc.Name)
	require.True(t, (info.Flags&flag) != 0,
		"Account %s expected flag 0x%x to be set, but flags are 0x%x",
		acc.Name, flag, info.Flags)
}

// RequireFlagNotSet asserts that a specific flag is NOT set on an account.
func RequireFlagNotSet(t *testing.T, env *TestEnv, acc *Account, flag uint32) {
	t.Helper()
	info := env.AccountInfo(acc)
	require.NotNil(t, info, "Account %s does not exist", acc.Name)
	require.True(t, (info.Flags&flag) == 0,
		"Account %s expected flag 0x%x to NOT be set, but flags are 0x%x",
		acc.Name, flag, info.Flags)
}

// XRPMinusFee calculates expected XRP balance after paying fees.
// amount is in drops, fee defaults to env's base fee.
func XRPMinusFee(env *TestEnv, amountXRP int64) uint64 {
	return uint64(XRP(amountXRP)) - env.BaseFee()
}

// XRPMinusFees calculates expected XRP balance after paying multiple fees.
func XRPMinusFees(env *TestEnv, amountXRP int64, numFees int) uint64 {
	return uint64(XRP(amountXRP)) - uint64(numFees)*env.BaseFee()
}

// RequireIOUBalance asserts that an account has the expected IOU balance.
// The balance is from the holder's perspective.
// Reference: rippled's require(env.balance(alice, USD) == USD(100))
func RequireIOUBalance(t *testing.T, env *TestEnv, holder, issuer *Account, currency string, expected float64) {
	t.Helper()
	actual := env.BalanceIOU(holder, currency, issuer)
	require.InDelta(t, expected, actual, 1e-10,
		"Account %s IOU balance mismatch for %s/%s: expected %f, got %f",
		holder.Name, currency, issuer.Name, expected, actual)
}

// RequireIOUBalanceApprox asserts that an account IOU balance is within a tolerance of the expected value.
func RequireIOUBalanceApprox(t *testing.T, env *TestEnv, holder, issuer *Account, currency string, expected, tolerance float64) {
	t.Helper()
	actual := env.BalanceIOU(holder, currency, issuer)
	diff := math.Abs(actual - expected)
	require.LessOrEqual(t, diff, tolerance,
		"Account %s IOU balance mismatch for %s/%s: expected %f +/- %f, got %f (diff: %f)",
		holder.Name, currency, issuer.Name, expected, tolerance, actual, diff)
}

// RequireLedgerEntryExists asserts that a ledger entry exists at the given keylet.
func RequireLedgerEntryExists(t *testing.T, env *TestEnv, key keylet.Keylet) {
	t.Helper()
	require.True(t, env.LedgerEntryExists(key),
		"Expected ledger entry to exist at keylet %v", key)
}

// RequireLedgerEntryNotExists asserts that a ledger entry does NOT exist at the given keylet.
func RequireLedgerEntryNotExists(t *testing.T, env *TestEnv, key keylet.Keylet) {
	t.Helper()
	require.False(t, env.LedgerEntryExists(key),
		"Expected ledger entry to NOT exist at keylet %v", key)
}

// RequireTicketCount asserts that an account has the expected number of tickets.
// This iterates the owner directory and counts entries of type Ticket (0x0054),
// matching rippled's owner_count<ltTICKET> behavior.
func RequireTicketCount(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	dirKey := keylet.OwnerDir(acc.ID)
	var count uint32
	err := sle.DirForEach(env.ledger, dirKey, func(itemKey [32]byte) error {
		// Read the entry and check its LedgerEntryType
		entryKey := keylet.Keylet{Key: itemKey}
		data, readErr := env.ledger.Read(entryKey)
		if readErr != nil || len(data) == 0 {
			return nil
		}
		entryType, typeErr := sle.GetLedgerEntryType(data)
		if typeErr != nil {
			return nil
		}
		// Ticket type = 0x0054
		if entryType == 0x0054 {
			count++
		}
		return nil
	})
	require.NoError(t, err, "Account %s: failed to iterate owner directory", acc.Name)
	require.Equal(t, expected, count,
		"Account %s ticket count mismatch: expected %d, got %d",
		acc.Name, expected, count)
}

// RequireSignerListCount asserts that an account has the expected number of signer lists.
// On the XRPL, an account can have at most one signer list, so expected is 0 or 1.
func RequireSignerListCount(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	key := keylet.SignerList(acc.ID)
	exists := env.LedgerEntryExists(key)
	if expected == 0 {
		require.False(t, exists,
			"Account %s expected no signer list, but one exists", acc.Name)
	} else {
		require.True(t, exists,
			"Account %s expected signer list to exist, but it doesn't", acc.Name)
	}
}

// RequireTrustLineExists asserts that a trust line exists between two accounts for a currency.
func RequireTrustLineExists(t *testing.T, env *TestEnv, acc1, acc2 *Account, currency string) {
	t.Helper()
	require.True(t, env.TrustLineExists(acc1, acc2, currency),
		"Expected trust line to exist between %s and %s for %s",
		acc1.Name, acc2.Name, currency)
}

// RequireTrustLineNotExists asserts that a trust line does NOT exist between two accounts for a currency.
func RequireTrustLineNotExists(t *testing.T, env *TestEnv, acc1, acc2 *Account, currency string) {
	t.Helper()
	require.False(t, env.TrustLineExists(acc1, acc2, currency),
		"Expected no trust line between %s and %s for %s",
		acc1.Name, acc2.Name, currency)
}

// RequireMintedCount asserts that an account has the expected number of minted NFTokens.
// Reference: rippled's mintedCount() test helper.
func RequireMintedCount(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	actual := env.MintedCount(acc)
	require.Equal(t, expected, actual,
		"Account %s minted NFToken count mismatch: expected %d, got %d",
		acc.Name, expected, actual)
}

// RequireBurnedCount asserts that an account has the expected number of burned NFTokens.
// Reference: rippled's burnedCount() test helper.
func RequireBurnedCount(t *testing.T, env *TestEnv, acc *Account, expected uint32) {
	t.Helper()
	actual := env.BurnedCount(acc)
	require.Equal(t, expected, actual,
		"Account %s burned NFToken count mismatch: expected %d, got %d",
		acc.Name, expected, actual)
}
