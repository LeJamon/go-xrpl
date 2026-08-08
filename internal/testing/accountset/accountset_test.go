package accountset

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

// Tests reversible account flags and unknown no-op flag values.
func TestAccountSet_MostFlags(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)

	// Set up a regular key so we can test DisableMaster
	alie := jtx.NewAccount("alie")
	env.FundNoRipple(alie) // Register so env knows the account
	env.SetRegularKey(alice, alie)
	env.Close()

	tests := []struct {
		name        string
		accountFlag uint32
		ledgerFlag  uint32
	}{
		{name: "RequireDest", accountFlag: accounttx.AccountSetFlagRequireDest, ledgerFlag: state.LsfRequireDestTag},
		{name: "RequireAuth", accountFlag: accounttx.AccountSetFlagRequireAuth, ledgerFlag: state.LsfRequireAuth},
		{name: "DisallowXRP", accountFlag: accounttx.AccountSetFlagDisallowXRP, ledgerFlag: state.LsfDisallowXRP},
		{name: "GlobalFreeze", accountFlag: accounttx.AccountSetFlagGlobalFreeze, ledgerFlag: state.LsfGlobalFreeze},
		{name: "DisableMaster", accountFlag: accounttx.AccountSetFlagDisableMaster, ledgerFlag: state.LsfDisableMaster},
		{name: "DefaultRipple", accountFlag: accounttx.AccountSetFlagDefaultRipple, ledgerFlag: state.LsfDefaultRipple},
		{name: "DepositAuth", accountFlag: accounttx.AccountSetFlagDepositAuth, ledgerFlag: state.LsfDepositAuth},
	}

	origFlags := env.AccountInfo(alice).Flags
	handled := map[uint32]bool{
		accounttx.AccountSetFlagAccountTxnID:                 true,
		accounttx.AccountSetFlagNoFreeze:                     true,
		accounttx.AccountSetFlagAuthorizedNFTokenMinter:      true,
		accounttx.AccountSetFlagDisallowIncomingCheck:        true,
		accounttx.AccountSetFlagDisallowIncomingPayChan:      true,
		accounttx.AccountSetFlagDisallowIncomingNFTokenOffer: true,
		accounttx.AccountSetFlagDisallowIncomingTrustline:    true,
		accounttx.AccountSetFlagAllowTrustLineClawback:       true,
		accounttx.AccountSetFlagAllowTrustLineLocking:        true,
	}

	for _, tt := range tests {
		handled[tt.accountFlag] = true
		t.Run(tt.name, func(t *testing.T) {
			jtx.RequireFlagNotSet(t, env, alice, tt.ledgerFlag)

			result := env.SubmitSigned(AccountSet(alice).SetFlag(tt.accountFlag).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			jtx.RequireFlagSet(t, env, alice, tt.ledgerFlag)

			result = env.SubmitSignedWith(AccountSet(alice).ClearFlag(tt.accountFlag).Build(), alie)
			jtx.RequireTxSuccess(t, result)
			env.Close()
			jtx.RequireFlagNotSet(t, env, alice, tt.ledgerFlag)
			require.Equal(t, origFlags, env.AccountInfo(alice).Flags)
		})
	}

	for flag := uint32(1); flag < 32; flag++ {
		if handled[flag] {
			continue
		}
		t.Run(fmt.Sprintf("unknown-%d", flag), func(t *testing.T) {
			result := env.SubmitSigned(AccountSet(alice).SetFlag(flag).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

			result = env.SubmitSignedWith(AccountSet(alice).ClearFlag(flag).Build(), alie)
			jtx.RequireTxSuccess(t, result)
			env.Close()
			require.Equal(t, origFlags, env.AccountInfo(alice).Flags)
		})
	}
}

// NoFreeze requires master key to set and cannot be cleared once set.
func TestAccountSet_SetNoFreeze(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundNoRipple(alice)

	// Set up a regular key
	eric := jtx.NewAccount("eric")
	env.FundNoRipple(eric)
	env.SetRegularKey(alice, eric)

	jtx.RequireFlagNotSet(t, env, alice, state.LsfNoFreeze)

	// Setting NoFreeze with regular key should fail with tecNEED_MASTER_KEY
	result := env.SubmitSignedWith(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagNoFreeze).Build(),
		eric,
	)
	jtx.RequireTxFail(t, result, "tecNEED_MASTER_KEY")
	jtx.RequireFlagNotSet(t, env, alice, state.LsfNoFreeze)

	// Setting NoFreeze with master key should succeed
	result = env.SubmitSigned(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagNoFreeze).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	jtx.RequireFlagSet(t, env, alice, state.LsfNoFreeze)

	// Clearing NoFreeze should have no effect (flag persists)
	result = env.Submit(AccountSet(alice).ClearFlag(accounttx.AccountSetFlagNoFreeze).Build())
	jtx.RequireTxSuccess(t, result)
	jtx.RequireFlagSet(t, env, alice, state.LsfNoFreeze) // Still set
}

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

	for _, malformed := range []string{"0", "zz"} {
		result = env.Submit(AccountSet(alice).Domain(malformed).Build())
		jtx.RequireTxFail(t, result, "telBAD_DOMAIN")
		require.Equal(t, domain, env.AccountInfo(alice).Domain)
	}

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
			before := env.AccountInfo(alice).Domain
			result = env.Submit(AccountSet(alice).Domain(domain2Hex).Build())
			jtx.RequireTxFail(t, result, "telBAD_DOMAIN")
			require.Equal(t, before, env.AccountInfo(alice).Domain)
		}
	}
}

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
	invalidKeyHex := hex.EncodeToString([]byte("NOT_REALLY_A_PUBKEY"))
	result = env.Submit(AccountSet(alice).MessageKey(invalidKeyHex).Build())
	jtx.RequireTxFail(t, result, "telBAD_PUBLIC_KEY")
	require.Empty(t, env.AccountInfo(alice).MessageKey)
}

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

// Tests setting and clearing the EmailHash field.
func TestAccountSet_EmailHash(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	mh := "5F31A79367DC3137FADA860C05742EE6"

	readEmailHash := func() (string, bool) {
		t.Helper()
		data, err := env.LedgerEntry(keylet.Account(alice.ID))
		require.NoError(t, err)
		fields, err := binarycodec.Decode(hex.EncodeToString(data))
		require.NoError(t, err)
		value, present := fields["EmailHash"].(string)
		return value, present
	}

	result := env.Submit(AccountSet(alice).EmailHash(mh).Build())
	jtx.RequireTxSuccess(t, result)
	stored, present := readEmailHash()
	require.True(t, present)
	require.Equal(t, strings.ToUpper(mh), stored)

	for _, malformed := range []string{
		"5F31A79367DC3137FADA860C05742E",
		"5F31A79367DC3137FADA860C05742EE600",
		"GGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG",
	} {
		result = env.Submit(AccountSet(alice).EmailHash(malformed).Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
		stored, present = readEmailHash()
		require.True(t, present)
		require.Equal(t, strings.ToUpper(mh), stored)
	}

	result = env.Submit(AccountSet(alice).EmailHash(strings.Repeat("0", 32)).Build())
	jtx.RequireTxSuccess(t, result)
	stored, present = readEmailHash()
	require.False(t, present)
	require.Empty(t, stored)

	result = env.Submit(AccountSet(alice).EmailHash(mh).Build())
	jtx.RequireTxSuccess(t, result)
	parsed, err := tx.FromJSON([]byte(fmt.Sprintf(
		`{"TransactionType":"AccountSet","Account":%q,"EmailHash":""}`,
		alice.Address,
	)))
	require.NoError(t, err)
	emptyHash, ok := parsed.(*accounttx.AccountSet)
	require.True(t, ok)
	require.True(t, emptyHash.HasField("EmailHash"))
	result = env.Submit(emptyHash)
	jtx.RequireTxSuccess(t, result)
	stored, present = readEmailHash()
	require.False(t, present)
	require.Empty(t, stored)
}

// Tests transfer rate validation: valid range is 1.0-2.0 (or 0 to clear).
func TestAccountSet_TransferRate(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	tests := []struct {
		name   string
		set    uint32
		code   string
		stored uint32
	}{
		{name: "maximum", set: 2_000_000_000, code: "tesSUCCESS", stored: 2_000_000_000},
		{name: "above maximum", set: 2_000_000_001, code: "temBAD_TRANSFER_RATE", stored: 2_000_000_000},
		{name: "below minimum", set: 999_999_999, code: "temBAD_TRANSFER_RATE", stored: 2_000_000_000},
		{name: "minimum clears", set: 1_000_000_000, code: "tesSUCCESS", stored: 0},
		{name: "above minimum", set: 1_000_000_001, code: "tesSUCCESS", stored: 1_000_000_001},
		{name: "zero clears", set: 0, code: "tesSUCCESS", stored: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := env.Submit(AccountSet(alice).TransferRate(tc.set).Build())
			require.Equal(t, tc.code, result.Code)
			env.Close()
			require.Equal(t, tc.stored, env.AccountInfo(alice).TransferRate)
		})
	}
}

// Tests transfer rate application in real payment scenarios with IOUs.
func TestAccountSet_Gateway(t *testing.T) {
	const qualityOne = uint32(1_000_000_000)

	// Test gateway with a variety of allowed transfer rates (1.0 to 2.0)
	for rate := 1.0; rate <= 2.0; rate += 0.03125 {
		rateU32 := uint32(rate * float64(qualityOne))
		t.Run(fmt.Sprintf("rate-%d", rateU32), func(t *testing.T) {
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
	// Two out-of-bounds values currently in the MainNet ledger: 4.0 and 4.294967295
	for _, rate := range []float64{4.0, 4.294967295} {
		rateU32 := uint32(rate * float64(qualityOne))
		t.Run(fmt.Sprintf("legacy-%d", rateU32), func(t *testing.T) {
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

// Tests conflicting flag combinations and missing prerequisites.
func TestAccountSet_BadInputs(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	origFlags := env.AccountInfo(alice).Flags

	// Setting and clearing the same flag → temINVALID_FLAG
	result := env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagDisallowXRP).
		ClearFlag(accounttx.AccountSetFlagDisallowXRP).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).
		ClearFlag(accounttx.AccountSetFlagRequireAuth).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireDest).
		ClearFlag(accounttx.AccountSetFlagRequireDest).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

	// Setting a flag while transaction flags contradict → temINVALID_FLAG
	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagDisallowXRP).
		TxFlags(accounttx.AccountSetTxFlagAllowXRP).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).
		TxFlags(accounttx.AccountSetTxFlagOptionalAuth).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireDest).
		TxFlags(accounttx.AccountSetTxFlagOptionalDestTag).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

	// Using the mask value for transaction flags → temINVALID_FLAG
	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireDest).
		TxFlags(accounttx.AccountSetTxFlagMask).Build())
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)

	// Disabling master key without an alternative → tecNO_ALTERNATIVE_KEY
	result = env.SubmitSigned(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagDisableMaster).Build(),
	)
	jtx.RequireTxFail(t, result, "tecNO_ALTERNATIVE_KEY")
	require.Equal(t, origFlags, env.AccountInfo(alice).Flags)
}

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
	result := env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).Build())
	jtx.RequireTxFail(t, result, "tecOWNERS")
	jtx.RequireFlagNotSet(t, env, alice, state.LsfRequireAuth)

	// Remove the signer list.
	env.RemoveSignerList(alice)
	env.Close()

	// Now RequireAuth should succeed.
	result = env.Submit(AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagRequireAuth).Build())
	jtx.RequireTxSuccess(t, result)
	jtx.RequireFlagSet(t, env, alice, state.LsfRequireAuth)
}

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
	result := env.Submit(jtx.WithTicketSeq(AccountSet(alice).Build(), ticketSeq+1))
	env.Close()
	jtx.RequireTxFail(t, result, "terPRE_TICKET")
	jtx.RequireOwnerCount(t, env, alice, 1)
	jtx.RequireTicketCount(t, env, alice, 1)

	// Use the actual ticket. Sequence should NOT advance.
	aliceSeq := env.Seq(alice)
	result = env.Submit(jtx.WithTicketSeq(AccountSet(alice).Build(), ticketSeq))
	env.Close()
	jtx.RequireTxSuccess(t, result)
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireTicketCount(t, env, alice, 0)
	require.Equal(t, aliceSeq, env.Seq(alice), "Sequence should not advance when using a ticket")

	// Try re-using the consumed ticket.
	result = env.Submit(jtx.WithTicketSeq(AccountSet(alice).Build(), ticketSeq))
	env.Close()
	jtx.RequireTxFail(t, result, "tefNO_TICKET")
}

// TestAccountSet_DirIsEmpty_AnchorEmptyWithContinuation guards rippled's
// dirIsEmpty() semantics: the anchor (root) page may have an empty Indexes
// slice while continuation pages still hold entries. In that case the
// directory is NOT empty and asfRequireAuth / asfAllowTrustLineClawback must
// be rejected with tecOWNERS.
func TestAccountSet_DirIsEmpty_AnchorEmptyWithContinuation(t *testing.T) {
	t.Run("RequireAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)
		env.Close()

		// Seed the anchor page so it exists, then force it to look empty
		// while IndexNext points at a (fictitious) continuation page.
		// ownerDirIsEmpty must consult IndexNext and report the directory
		// as non-empty.
		env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bob, Weight: 1}})
		env.Close()
		require.NoError(t, env.ForceOwnerDirEmptyAnchorWithNext(alice, 1))

		result := env.Submit(AccountSet(alice).
			SetFlag(accounttx.AccountSetFlagRequireAuth).Build())
		jtx.RequireTxFail(t, result, "tecOWNERS")
	})

	t.Run("AllowTrustLineClawback", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice)
		env.Fund(bob)
		env.Close()

		env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bob, Weight: 1}})
		env.Close()
		require.NoError(t, env.ForceOwnerDirEmptyAnchorWithNext(alice, 1))

		result := env.Submit(AccountSet(alice).AllowClawback().Build())
		jtx.RequireTxFail(t, result, "tecOWNERS")
	})
}

// TestAccountSet_PreclaimOrder_RequireAuthBeforeClawback pins rippled's
// AccountSet::preclaim order: the RequireAuth owner-directory gate runs before
// the Clawback / NoFreeze mutual-exclusion gate. An account that already carries
// lsfNoFreeze and a non-empty owner directory, submitting a legacy tfRequireAuth
// together with SetFlag=asfAllowTrustLineClawback, must fail tecOWNERS (the
// RequireAuth gate) rather than tecNO_PERMISSION (the Clawback gate). go-xrpl
// previously folded both into Apply Clawback-first and returned tecNO_PERMISSION.
func TestAccountSet_PreclaimOrder_RequireAuthBeforeClawback(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)
	env.Close()

	// Set NoFreeze on alice (this alone would make an AllowTrustLineClawback
	// SetFlag fail tecNO_PERMISSION under the Clawback gate).
	res := env.Submit(AccountSet(alice).SetFlag(accounttx.AccountSetFlagNoFreeze).Build())
	jtx.RequireTxSuccess(t, res)
	env.Close()

	// Give alice a non-empty owner directory so the RequireAuth gate fires.
	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bob, Weight: 1}})
	env.Close()

	// Legacy tfRequireAuth + SetFlag=AllowTrustLineClawback: the RequireAuth gate
	// runs first, so the result is tecOWNERS, not tecNO_PERMISSION.
	res = env.Submit(AccountSet(alice).
		TxFlags(accounttx.AccountSetTxFlagRequireAuth).
		SetFlag(accounttx.AccountSetFlagAllowTrustLineClawback).Build())
	jtx.RequireTxFail(t, res, "tecOWNERS")
}

// DisableMaster requires the master key — issue 734.
// asfDisableMaster may be set only by a transaction signed with the account's
// own master key. A regular-key-signed or multi-signed AccountSet must be
// rejected with tecNEED_MASTER_KEY, matching rippled SetAccount.cpp sigWithMaster.
func TestAccountSet_DisableMaster_RequiresMasterKey(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	eric := jtx.NewAccount("eric")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, eric, bob)
	env.Close()

	// Give alice both alternate signing methods so disabling the master key is
	// otherwise permitted (no tecNO_ALTERNATIVE_KEY): a regular key and a signer
	// list. This isolates the master-key requirement as the only gate under test.
	env.SetRegularKey(alice, eric)
	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bob, Weight: 1}})
	env.Close()

	jtx.RequireFlagNotSet(t, env, alice, state.LsfDisableMaster)

	// Multi-signed: an empty SigningPubKey is never the master key.
	result := env.SubmitMultiSigned(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagDisableMaster).Build(),
		[]*jtx.Account{bob},
	)
	jtx.RequireTxFail(t, result, "tecNEED_MASTER_KEY")
	jtx.RequireFlagNotSet(t, env, alice, state.LsfDisableMaster)

	// Regular-key signed: a valid but non-master key.
	result = env.SubmitSignedWith(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagDisableMaster).Build(),
		eric,
	)
	jtx.RequireTxFail(t, result, "tecNEED_MASTER_KEY")
	jtx.RequireFlagNotSet(t, env, alice, state.LsfDisableMaster)

	// Master-key signed: succeeds and sets lsfDisableMaster.
	result = env.SubmitSigned(
		AccountSet(alice).SetFlag(accounttx.AccountSetFlagDisableMaster).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	jtx.RequireFlagSet(t, env, alice, state.LsfDisableMaster)
}
