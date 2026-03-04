package accountset

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	accounttx "github.com/LeJamon/goXRPLd/internal/core/tx/account"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
)

// asfToLsf maps an AccountSet flag (asf*) to the corresponding ledger flag (lsf*).
func asfToLsf(asfFlag uint32) uint32 {
	switch asfFlag {
	case accounttx.AccountSetFlagRequireDest:
		return sle.LsfRequireDestTag
	case accounttx.AccountSetFlagRequireAuth:
		return sle.LsfRequireAuth
	case accounttx.AccountSetFlagDisallowXRP:
		return sle.LsfDisallowXRP
	case accounttx.AccountSetFlagDisableMaster:
		return sle.LsfDisableMaster
	case accounttx.AccountSetFlagNoFreeze:
		return sle.LsfNoFreeze
	case accounttx.AccountSetFlagGlobalFreeze:
		return sle.LsfGlobalFreeze
	case accounttx.AccountSetFlagDefaultRipple:
		return sle.LsfDefaultRipple
	case accounttx.AccountSetFlagDepositAuth:
		return sle.LsfDepositAuth
	case accounttx.AccountSetFlagDisallowIncomingNFTokenOffer:
		return sle.LsfDisallowIncomingNFTokenOffer
	case accounttx.AccountSetFlagDisallowIncomingCheck:
		return sle.LsfDisallowIncomingCheck
	case accounttx.AccountSetFlagDisallowIncomingPayChan:
		return sle.LsfDisallowIncomingPayChan
	case accounttx.AccountSetFlagDisallowIncomingTrustline:
		return sle.LsfDisallowIncomingTrustline
	case accounttx.AccountSetFlagAllowTrustLineClawback:
		return sle.LsfAllowTrustLineClawback
	default:
		return 0
	}
}

// =========================================================================
// testNullAccountSet — rippled AccountSet_test.cpp lines 33-45
// =========================================================================
// Verifies that a freshly funded account (with noripple) has flags == 0.
func TestAccountSet_NullAccountSet(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)

	// After funding without DefaultRipple, account flags should be 0.
	info := env.AccountInfo(alice)
	require.NotNil(t, info)
	require.Equal(t, uint32(0), info.Flags,
		"Expected flags 0 for newly funded noripple account, got 0x%x", info.Flags)
}

// =========================================================================
// testMostFlags — rippled AccountSet_test.cpp lines 47-157
// =========================================================================
// Tests setting and clearing most account flags, with and without DepositAuth.
func TestAccountSet_MostFlags(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)

	// Set up a regular key so we can test DisableMaster
	// Reference: rippled's env.memoize("alie"); env(regkey(alice, "alie"));
	alie := jtx.NewAccount("alie")
	env.FundNoRipple(alie) // Register so env knows the account
	env.SetRegularKey(alice, alie)
	env.Close()

	// Flags that are tested elsewhere and should be skipped
	skipFlags := map[uint32]bool{
		accounttx.AccountSetFlagNoFreeze:                      true, // Can't be cleared
		accounttx.AccountSetFlagAuthorizedNFTokenMinter:       true, // Requires NFTokenMinter field
		accounttx.AccountSetFlagDisallowIncomingCheck:         true, // DisallowIncoming amendment
		accounttx.AccountSetFlagDisallowIncomingPayChan:       true, // DisallowIncoming amendment
		accounttx.AccountSetFlagDisallowIncomingNFTokenOffer:  true, // DisallowIncoming amendment
		accounttx.AccountSetFlagDisallowIncomingTrustline:     true, // DisallowIncoming amendment
		accounttx.AccountSetFlagAllowTrustLineClawback:        true, // Can't be cleared
	}

	testFlags := func(goodFlags []uint32) {
		origFlags := env.AccountInfo(alice).Flags

		goodFlagSet := make(map[uint32]bool)
		for _, f := range goodFlags {
			goodFlagSet[f] = true
		}

		// Test flags 1 through 31 (std::numeric_limits<uint32_t>::digits)
		for flag := uint32(1); flag < 32; flag++ {
			if skipFlags[flag] {
				continue
			}

			if goodFlagSet[flag] {
				// Good flag — should be settable and clearable
				lsfFlag := asfToLsf(flag)
				if lsfFlag == 0 {
					continue // Unknown mapping
				}

				jtx.RequireFlagNotSet(t, env, alice, lsfFlag)

				// Set the flag (using master key)
				result := env.Submit(AccountSet(alice).SetFlag(flag).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()
				jtx.RequireFlagSet(t, env, alice, lsfFlag)

				// Clear the flag (using regular key — env.Submit skips sig verification)
				result = env.Submit(AccountSet(alice).ClearFlag(flag).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()
				jtx.RequireFlagNotSet(t, env, alice, lsfFlag)

				nowFlags := env.AccountInfo(alice).Flags
				require.Equal(t, origFlags, nowFlags,
					"Flag %d: expected flags to return to 0x%x after clear, got 0x%x",
					flag, origFlags, nowFlags)
			} else {
				// Bad flag — setting/clearing should not change flags
				currentFlags := env.AccountInfo(alice).Flags
				require.Equal(t, origFlags, currentFlags)

				env.Submit(AccountSet(alice).SetFlag(flag).Build())
				env.Close()
				currentFlags = env.AccountInfo(alice).Flags
				require.Equal(t, origFlags, currentFlags,
					"Bad flag %d: expected flags unchanged 0x%x after set, got 0x%x",
					flag, origFlags, currentFlags)

				env.Submit(AccountSet(alice).ClearFlag(flag).Build())
				env.Close()
				currentFlags = env.AccountInfo(alice).Flags
				require.Equal(t, origFlags, currentFlags,
					"Bad flag %d: expected flags unchanged 0x%x after clear, got 0x%x",
					flag, origFlags, currentFlags)
			}
		}
	}

	// Test with featureDepositAuth disabled.
	// Reference: rippled AccountSet_test.cpp lines 137-144
	env.DisableFeature("DepositAuth")
	testFlags([]uint32{
		accounttx.AccountSetFlagRequireDest,
		accounttx.AccountSetFlagRequireAuth,
		accounttx.AccountSetFlagDisallowXRP,
		accounttx.AccountSetFlagGlobalFreeze,
		accounttx.AccountSetFlagDisableMaster,
		accounttx.AccountSetFlagDefaultRipple,
	})

	// Enable featureDepositAuth and retest.
	// Reference: rippled AccountSet_test.cpp lines 146-157
	env.EnableFeature("DepositAuth")
	env.Close()
	testFlags([]uint32{
		accounttx.AccountSetFlagRequireDest,
		accounttx.AccountSetFlagRequireAuth,
		accounttx.AccountSetFlagDisallowXRP,
		accounttx.AccountSetFlagGlobalFreeze,
		accounttx.AccountSetFlagDisableMaster,
		accounttx.AccountSetFlagDefaultRipple,
		accounttx.AccountSetFlagDepositAuth,
	})
}

// =========================================================================
// testSetAndResetAccountTxnID — rippled AccountSet_test.cpp lines 159-180
// =========================================================================
// asfAccountTxnID is special — it controls field presence, not a flag bit.
func TestAccountSet_SetAndResetAccountTxnID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)

	origFlags := env.AccountInfo(alice).Flags

	// AccountTxnID should not be present initially
	var zeroHash [32]byte
	info := env.AccountInfo(alice)
	require.Equal(t, zeroHash, info.AccountTxnID, "AccountTxnID should not be present initially")

	// Set asfAccountTxnID — field should become present
	result := env.Submit(AccountSet(alice).SetFlag(accounttx.AccountSetFlagAccountTxnID).Build())
	jtx.RequireTxSuccess(t, result)
	info = env.AccountInfo(alice)
	require.NotEqual(t, zeroHash, info.AccountTxnID, "AccountTxnID should be present after set")

	// Clear asfAccountTxnID — field should be removed
	result = env.Submit(AccountSet(alice).ClearFlag(accounttx.AccountSetFlagAccountTxnID).Build())
	jtx.RequireTxSuccess(t, result)
	info = env.AccountInfo(alice)
	require.Equal(t, zeroHash, info.AccountTxnID, "AccountTxnID should not be present after clear")

	// Flags should be unchanged
	nowFlags := env.AccountInfo(alice).Flags
	require.Equal(t, origFlags, nowFlags)
}

// =========================================================================
// testSetNoFreeze — rippled AccountSet_test.cpp lines 182-201
// =========================================================================
// NoFreeze requires master key to set and cannot be cleared once set.
func TestAccountSet_SetNoFreeze(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)

	// Set up a regular key
	eric := jtx.NewAccount("eric")
	env.FundNoRipple(eric)
	env.SetRegularKey(alice, eric)

	jtx.RequireFlagNotSet(t, env, alice, sle.LsfNoFreeze)

	// Setting NoFreeze with regular key should fail with tecNEED_MASTER_KEY
	// Reference: rippled AccountSet_test.cpp line 195
	result := env.SubmitSignedWith(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagNoFreeze).Build(),
		eric,
	)
	jtx.RequireTxFail(t, result, "tecNEED_MASTER_KEY")

	// Setting NoFreeze with master key should succeed
	result = env.SubmitSigned(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagNoFreeze).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	jtx.RequireFlagSet(t, env, alice, sle.LsfNoFreeze)

	// Clearing NoFreeze should have no effect (flag persists)
	result = env.Submit(AccountSet(alice).ClearFlag(accounttx.AccountSetFlagNoFreeze).Build())
	jtx.RequireTxSuccess(t, result)
	jtx.RequireFlagSet(t, env, alice, sle.LsfNoFreeze) // Still set
}

// =========================================================================
// testDomain — rippled AccountSet_test.cpp lines 203-251
// =========================================================================
// Tests setting and clearing the Domain field, plus length limits.
func TestAccountSet_Domain(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	// Set domain "example.com" (as hex)
	domain := "example.com"
	domainHex := hex.EncodeToString([]byte(domain))

	result := env.Submit(AccountSet(alice).Domain(domainHex).Build())
	jtx.RequireTxSuccess(t, result)
	info := env.AccountInfo(alice)
	require.Equal(t, domain, info.Domain, "Domain should be set to example.com")

	// Clear domain with empty string
	result = env.Submit(AccountSet(alice).Domain("").Build())
	jtx.RequireTxSuccess(t, result)
	info = env.AccountInfo(alice)
	require.Equal(t, "", info.Domain, "Domain should be cleared")

	// Test edge cases: 255, 256, 257 byte domains
	// MaxDomainLength = 256 bytes
	const maxLength = 256
	for _, length := range []int{maxLength - 1, maxLength, maxLength + 1} {
		// Build a domain of the specified length
		// e.g. "aaa...a.example.com"
		prefix := strings.Repeat("a", length-len(domain)-1)
		domain2 := prefix + "." + domain
		require.Equal(t, length, len(domain2))

		domain2Hex := hex.EncodeToString([]byte(domain2))

		if length <= maxLength {
			result = env.Submit(AccountSet(alice).Domain(domain2Hex).Build())
			jtx.RequireTxSuccess(t, result)
			info = env.AccountInfo(alice)
			require.Equal(t, domain2, info.Domain)
		} else {
			result = env.Submit(AccountSet(alice).Domain(domain2Hex).Build())
			jtx.RequireTxFail(t, result, "telBAD_DOMAIN")
		}
	}
}

// =========================================================================
// testMessageKey — rippled AccountSet_test.cpp lines 253-278
// =========================================================================
// Tests setting and clearing the MessageKey field, plus invalid key validation.
func TestAccountSet_MessageKey(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	// Generate a valid ed25519 public key for MessageKey
	rkp := jtx.NewAccountWithKeyType("messagekey_account", jtx.KeyTypeEd25519)
	messageKeyHex := hex.EncodeToString(rkp.PublicKey)

	// Set MessageKey to a valid public key
	result := env.Submit(AccountSet(alice).MessageKey(messageKeyHex).Build())
	jtx.RequireTxSuccess(t, result)
	info := env.AccountInfo(alice)
	require.Equal(t, strings.ToLower(messageKeyHex), strings.ToLower(info.MessageKey),
		"MessageKey should match the set value")

	// Clear MessageKey with empty string
	result = env.Submit(AccountSet(alice).MessageKey("").Build())
	jtx.RequireTxSuccess(t, result)
	info = env.AccountInfo(alice)
	require.Equal(t, "", info.MessageKey, "MessageKey should be cleared")

	// Set invalid message key — should fail
	// Reference: rippled AccountSet_test.cpp line 277
	invalidKeyHex := hex.EncodeToString([]byte("NOT_REALLY_A_PUBKEY"))
	result = env.Submit(AccountSet(alice).MessageKey(invalidKeyHex).Build())
	jtx.RequireTxFail(t, result, "telBAD_PUBLIC_KEY")
}

// =========================================================================
// testWalletID — rippled AccountSet_test.cpp lines 280-300
// =========================================================================
// Tests setting and clearing the WalletLocator field.
func TestAccountSet_WalletID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	locator := "9633EC8AF54F16B5286DB1D7B519EF49EEFC050C0C8AC4384F1D88ACD1BFDF05"

	// Set WalletLocator
	result := env.Submit(AccountSet(alice).WalletLocator(locator).Build())
	jtx.RequireTxSuccess(t, result)
	info := env.AccountInfo(alice)
	require.Equal(t, strings.ToLower(locator), strings.ToLower(info.WalletLocator),
		"WalletLocator should match the set value")

	// Clear WalletLocator with empty string
	result = env.Submit(AccountSet(alice).WalletLocator("").Build())
	jtx.RequireTxSuccess(t, result)
	info = env.AccountInfo(alice)
	require.Equal(t, "", info.WalletLocator, "WalletLocator should be cleared")
}

// =========================================================================
// testEmailHash — rippled AccountSet_test.cpp lines 302-321
// =========================================================================
// Tests setting and clearing the EmailHash field.
func TestAccountSet_EmailHash(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	mh := "5F31A79367DC3137FADA860C05742EE6"

	// Set EmailHash
	result := env.Submit(AccountSet(alice).EmailHash(mh).Build())
	jtx.RequireTxSuccess(t, result)
	info := env.AccountInfo(alice)
	require.Equal(t, strings.ToLower(mh), strings.ToLower(info.EmailHash),
		"EmailHash should match the set value")

	// Clear EmailHash with empty string
	// In rippled, clearing EmailHash sends the all-zeros hash.
	// The Go AccountSet Apply maps "00000000000000000000000000000000" → ""
	result = env.Submit(AccountSet(alice).EmailHash("").Build())
	jtx.RequireTxSuccess(t, result)
	info = env.AccountInfo(alice)
	require.Equal(t, "", info.EmailHash, "EmailHash should be cleared")
}

// =========================================================================
// testTransferRate — rippled AccountSet_test.cpp lines 323-368
// =========================================================================
// Tests transfer rate validation: valid range is 1.0-2.0 (or 0 to clear).
func TestAccountSet_TransferRate(t *testing.T) {
	const qualityOne = uint32(1_000_000_000)

	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	type testCase struct {
		set  float64
		code string
		get  float64
	}

	tests := []testCase{
		{1.0, "tesSUCCESS", 1.0},
		{1.1, "tesSUCCESS", 1.1},
		{2.0, "tesSUCCESS", 2.0},
		{2.1, "temBAD_TRANSFER_RATE", 2.0}, // Rejected, previous rate kept
		{0.0, "tesSUCCESS", 1.0},           // 0 clears the field (defaults to 1.0)
		{2.0, "tesSUCCESS", 2.0},
		{0.9, "temBAD_TRANSFER_RATE", 2.0}, // Rejected, previous rate kept
	}

	for _, tc := range tests {
		rate := uint32(tc.set * float64(qualityOne))
		result := env.Submit(AccountSet(alice).TransferRate(rate).Build())
		env.Close()

		if tc.code == "tesSUCCESS" {
			require.Equal(t, "tesSUCCESS", result.Code,
				"TransferRate %.1f: expected tesSUCCESS, got %s", tc.set, result.Code)
		} else {
			require.Equal(t, tc.code, result.Code,
				"TransferRate %.1f: expected %s, got %s", tc.set, tc.code, result.Code)
		}

		// Verify the stored rate
		info := env.AccountInfo(alice)
		if info.TransferRate == 0 {
			// Field not present → default 1.0
			require.InDelta(t, 1.0, tc.get, 0.001,
				"TransferRate %.1f: expected get=%.1f, got 1.0 (default)", tc.set, tc.get)
		} else {
			actualRate := float64(info.TransferRate) / float64(qualityOne)
			require.InDelta(t, tc.get, actualRate, 0.001,
				"TransferRate %.1f: expected get=%.1f, got %.6f", tc.set, tc.get, actualRate)
		}
	}
}

// =========================================================================
// testGateway — rippled AccountSet_test.cpp lines 370-472
// =========================================================================
// Tests transfer rate application in real payment scenarios with IOUs.
func TestAccountSet_Gateway(t *testing.T) {
	const qualityOne = uint32(1_000_000_000)

	// Test gateway with a variety of allowed transfer rates (1.0 to 2.0)
	// Reference: rippled AccountSet_test.cpp lines 382-406
	for rate := 1.0; rate <= 2.0; rate += 0.03125 {
		t.Run("", func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			gw := jtx.NewAccount("gateway")
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.Fund(gw, alice, bob)
			env.Close()

			// Set up trust lines: alice and bob trust gw for USD
			env.Trust(alice, jtx.IssuedCurrency(gw, "USD", 10))
			env.Trust(bob, jtx.IssuedCurrency(gw, "USD", 10))
			env.Close()

			// Set transfer rate on gateway
			rateU32 := uint32(rate * float64(qualityOne))
			env.SetTransferRate(gw, rateU32)
			env.Close()

			// gw pays alice 10 USD
			env.PayIOU(gw, alice, gw, "USD", 10)
			env.Close()

			// alice pays bob 1 USD (with sendmax 10 USD)
			env.PayIOUWithSendMax(alice, bob, gw, "USD", 1, 10)
			env.Close()

			// Calculate expected balance after transfer fee
			amountWithRate := 1.0 * rate
			expectedAlice := 10.0 - amountWithRate

			jtx.RequireIOUBalanceApprox(t, env, alice, gw, "USD", expectedAlice, 1e-6)
			jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1.0)
		})
	}

	// Test legacy out-of-bounds transfer rates by modifying ledger directly.
	// Reference: rippled AccountSet_test.cpp lines 408-471
	// Two out-of-bounds values currently in the MainNet ledger: 4.0 and 4.294967295
	for _, rate := range []float64{4.0, 4.294967295} {
		t.Run("", func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			gw := jtx.NewAccount("gateway")
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.Fund(gw, alice, bob)
			env.Close()

			env.Trust(alice, jtx.IssuedCurrency(gw, "USD", 10))
			env.Trust(bob, jtx.IssuedCurrency(gw, "USD", 10))
			env.Close()

			// Set a valid transfer rate first
			env.SetTransferRate(gw, 2*qualityOne)
			env.Close()

			// Hack the ledger to set the out-of-bounds transfer rate.
			// Reference: rippled AccountSet_test.cpp lines 446-460
			env.SetTransferRateDirect(gw, uint32(rate*float64(qualityOne)))

			// gw pays alice 10 USD, alice pays bob 1 USD
			env.PayIOU(gw, alice, gw, "USD", 10)
			env.PayIOUWithSendMax(alice, bob, gw, "USD", 1, 10)

			amountWithRate := 1.0 * rate
			expectedAlice := 10.0 - amountWithRate

			jtx.RequireIOUBalanceApprox(t, env, alice, gw, "USD", expectedAlice, 1e-6)
			jtx.RequireIOUBalance(t, env, bob, gw, "USD", 1.0)
		})
	}
}

// =========================================================================
// testBadInputs — rippled AccountSet_test.cpp lines 474-515
// =========================================================================
// Tests conflicting flag combinations and missing prerequisites.
func TestAccountSet_BadInputs(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	// Setting and clearing the same flag → temINVALID_FLAG
	// Reference: rippled AccountSet_test.cpp lines 484-495
	result := env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagDisallowXRP).
		ClearFlag(accounttx.AccountSetFlagDisallowXRP).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).
		ClearFlag(accounttx.AccountSetFlagRequireAuth).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireDest).
		ClearFlag(accounttx.AccountSetFlagRequireDest).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")

	// Setting a flag while transaction flags contradict → temINVALID_FLAG
	// Reference: rippled AccountSet_test.cpp lines 496-510
	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagDisallowXRP).
		TxFlags(accounttx.AccountSetTxFlagAllowXRP).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).
		TxFlags(accounttx.AccountSetTxFlagOptionalAuth).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireDest).
		TxFlags(accounttx.AccountSetTxFlagOptionalDestTag).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")

	// Using the mask value for transaction flags → temINVALID_FLAG
	// Reference: rippled AccountSet_test.cpp lines 508-510
	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireDest).
		TxFlags(accounttx.AccountSetTxFlagMask).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")

	// Disabling master key without an alternative → tecNO_ALTERNATIVE_KEY
	// Reference: rippled AccountSet_test.cpp lines 512-514
	result = env.SubmitSigned(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagDisableMaster).Build(),
	)
	jtx.RequireTxFail(t, result, "tecNO_ALTERNATIVE_KEY")
}

// =========================================================================
// testRequireAuthWithDir — rippled AccountSet_test.cpp lines 517-546
// =========================================================================
// RequireAuth cannot be set when the account has owned objects.
func TestAccountSet_RequireAuth(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Close()

	// alice should have an empty directory (Fund enables DefaultRipple but
	// the owner directory for that is empty — no objects like signer lists).
	// Give alice a signer list so the directory is non-empty.
	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bob, Weight: 1}})
	env.Close()

	// RequireAuth should fail with tecOWNERS because the directory is not empty.
	// Reference: rippled AccountSet_test.cpp line 538
	result := env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).Build())
	jtx.RequireTxFail(t, result, "tecOWNERS")

	// Remove the signer list.
	env.RemoveSignerList(alice)
	env.Close()

	// Now RequireAuth should succeed.
	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).Build())
	jtx.RequireTxSuccess(t, result)
}

// =========================================================================
// testTicket — rippled AccountSet_test.cpp lines 548-579
// =========================================================================
// Tests ticket creation and consumption via AccountSet (noop) transactions.
func TestAccountSet_Ticket(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// Create 1 ticket. The first ticket sequence is seq+1 (TicketCreate consumes seq).
	ticketSeq := env.CreateTickets(alice, 1)
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 1)

	// Try using a ticket that alice doesn't have (ticketSeq + 1).
	// Reference: rippled AccountSet_test.cpp line 564
	result := env.Submit(jtx.WithTicketSeq(AccountSet(alice).Build(), ticketSeq+1))
	env.Close()
	jtx.RequireTxFail(t, result, "terPRE_TICKET")
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 1)

	// Use the actual ticket. Sequence should NOT advance.
	// Reference: rippled AccountSet_test.cpp lines 570-574
	aliceSeq := env.Seq(alice)
	result = env.Submit(jtx.WithTicketSeq(AccountSet(alice).Build(), ticketSeq))
	env.Close()
	jtx.RequireTxSuccess(t, result)
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireTicketCount(t, env, alice, 0)
	require.Equal(t, aliceSeq, env.Seq(alice), "Sequence should not advance when using a ticket")

	// Try re-using the consumed ticket.
	// Reference: rippled AccountSet_test.cpp lines 577-578
	result = env.Submit(jtx.WithTicketSeq(AccountSet(alice).Build(), ticketSeq))
	env.Close()
	jtx.RequireTxFail(t, result, "tefNO_TICKET")
}
