package signerlist

import (
	"sort"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	tx.Register(tx.TypeSignerListSet, func() tx.Transaction {
		return &SignerListSet{BaseTx: *tx.NewBaseTx(tx.TypeSignerListSet, "")}
	})
	tx.Register(tx.TypeRegularKeySet, func() tx.Transaction {
		return &SetRegularKey{BaseTx: *tx.NewBaseTx(tx.TypeRegularKeySet, "")}
	})
}

// SignerListSet sets or clears a list of signers for multi-signing.
type SignerListSet struct {
	tx.BaseTx

	// SignerQuorum is the target number of signer weights (required)
	// Set to 0 to delete the signer list
	SignerQuorum uint32 `json:"SignerQuorum" xrpl:"SignerQuorum"`

	// SignerEntries is the list of signers (optional if deleting)
	SignerEntries []SignerEntry `json:"SignerEntries,omitempty" xrpl:"SignerEntries,omitempty"`
}

// SignerEntry represents an entry in a signer list
type SignerEntry struct {
	SignerEntry SignerEntryData `json:"SignerEntry"`
}

// SignerEntryData contains the signer entry fields
type SignerEntryData struct {
	Account       string `json:"Account"`
	SignerWeight  uint16 `json:"SignerWeight"`
	WalletLocator string `json:"WalletLocator,omitempty"`
}

// NewSignerListSet creates a new SignerListSet transaction
func NewSignerListSet(account string, quorum uint32) *SignerListSet {
	return &SignerListSet{
		BaseTx:       *tx.NewBaseTx(tx.TypeSignerListSet, account),
		SignerQuorum: quorum,
	}
}

// TxType returns the transaction type
func (s *SignerListSet) TxType() tx.Type {
	return tx.TypeSignerListSet
}

// Validate validates the SignerListSet transaction
func (s *SignerListSet) Validate() error {
	if err := s.BaseTx.Validate(); err != nil {
		return err
	}

	// If deleting (quorum = 0), no entries allowed
	// Reference: rippled SetSignerList.cpp preflight()
	if s.SignerQuorum == 0 {
		if len(s.SignerEntries) > 0 {
			return tx.Errorf(tx.TemMALFORMED, "cannot have SignerEntries when deleting signer list")
		}
		return nil
	}

	// Must have at least one signer, max 32
	// Reference: rippled SetSignerList.cpp:270-276
	if len(s.SignerEntries) == 0 || len(s.SignerEntries) > 32 {
		return tx.Errorf(tx.TemMALFORMED, "too many or too few signers in signer list")
	}

	// Check for duplicates, self-reference, and weight validity
	// Reference: rippled SetSignerList.cpp:279-328
	var totalWeight uint32
	seenAccounts := make(map[string]bool)

	for _, entry := range s.SignerEntries {
		// Weight must be positive
		// Reference: rippled SetSignerList.cpp:298-303
		if entry.SignerEntry.SignerWeight == 0 {
			return tx.Errorf(tx.TemBAD_WEIGHT, "every signer must have a positive weight")
		}

		totalWeight += uint32(entry.SignerEntry.SignerWeight)

		// Cannot include self
		// Reference: rippled SetSignerList.cpp:307-311
		if entry.SignerEntry.Account == s.Account {
			return tx.Errorf(tx.TemBAD_SIGNER, "a signer may not self reference account")
		}

		// No duplicate accounts
		// Reference: rippled SetSignerList.cpp:284-288
		if seenAccounts[entry.SignerEntry.Account] {
			return tx.Errorf(tx.TemBAD_SIGNER, "duplicate signers in signer list")
		}
		seenAccounts[entry.SignerEntry.Account] = true
	}

	// Total weight must be >= quorum, and quorum must be positive
	// Reference: rippled SetSignerList.cpp:324-328
	if s.SignerQuorum == 0 || totalWeight < s.SignerQuorum {
		return tx.Errorf(tx.TemBAD_QUORUM, "quorum is unreachable")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (s *SignerListSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(s)
}

// AddSigner adds a signer to the list
func (s *SignerListSet) AddSigner(account string, weight uint16) {
	s.SignerEntries = append(s.SignerEntries, SignerEntry{
		SignerEntry: SignerEntryData{
			Account:      account,
			SignerWeight: weight,
		},
	})
}

// SetRegularKey sets or clears an account's regular key.
type SetRegularKey struct {
	tx.BaseTx

	// RegularKey is the regular key to set (optional, omit to clear)
	RegularKey string `json:"RegularKey,omitempty" xrpl:"RegularKey,omitempty"`
}

// NewSetRegularKey creates a new SetRegularKey transaction
func NewSetRegularKey(account string) *SetRegularKey {
	return &SetRegularKey{
		BaseTx: *tx.NewBaseTx(tx.TypeRegularKeySet, account),
	}
}

// TxType returns the transaction type
func (s *SetRegularKey) TxType() tx.Type {
	return tx.TypeRegularKeySet
}

// Validate validates the SetRegularKey transaction
// Reference: rippled SetRegularKey.cpp preflight() — no type-specific flags allowed
func (s *SetRegularKey) Validate() error {
	if err := s.BaseTx.Validate(); err != nil {
		return err
	}
	// SetRegularKey has no type-specific flags.
	if s.GetFlags()&tx.TfUniversalMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for SetRegularKey")
	}
	return nil
}

// Flatten returns a flat map of all transaction fields
func (s *SetRegularKey) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(s)
}

// SetKey sets the regular key
func (s *SetRegularKey) SetKey(key string) {
	s.RegularKey = key
}

// ClearKey clears the regular key
func (s *SetRegularKey) ClearKey() {
	s.RegularKey = ""
}

// Apply applies the SetRegularKey transaction to ledger state.
// Reference: rippled SetRegularKey.cpp preflight + doApply()
func (s *SetRegularKey) Apply(ctx *tx.ApplyContext) tx.Result {
	// Amendment-gated preflight check: reject setting RegularKey to own account.
	// Reference: rippled SetRegularKey.cpp preflight lines 66-71
	if ctx.Rules().Enabled(amendment.FeatureFixMasterKeyAsRegularKey) {
		if s.RegularKey != "" && s.RegularKey == s.Account {
			return tx.TemBAD_REGKEY
		}
	}

	if s.RegularKey != "" {
		ctx.Log.Trace("set regular key apply",
			"account", s.Account,
			"regularKey", s.RegularKey,
		)
		// Setting a regular key
		if _, err := state.DecodeAccountID(s.RegularKey); err != nil {
			return tx.TemINVALID
		}
		ctx.Account.RegularKey = s.RegularKey
	} else {
		ctx.Log.Trace("set regular key apply",
			"account", s.Account,
			"regularKey", "removed",
		)
		// Clearing the regular key — check that an alternative auth method exists.
		// Reference: rippled SetRegularKey.cpp lines 86-98
		isMasterDisabled := (ctx.Account.Flags & state.LsfDisableMaster) != 0
		if isMasterDisabled {
			signerListKey := keylet.SignerList(ctx.AccountID)
			hasSignerList, _ := ctx.View.Exists(signerListKey)
			if !hasSignerList {
				ctx.Log.Warn("set regular key: no alternative key available")
				return tx.TecNO_ALTERNATIVE_KEY
			}
		}
		ctx.Account.RegularKey = ""
	}

	// If this was a free password change (fee=0), mark the password as spent.
	// Reference: rippled SetRegularKey.cpp doApply — sets lsfPasswordSpent
	// when the open ledger fee was 0 (the one-time free password change).
	if ctx.Config.BaseFee > 0 {
		fee := s.GetCommon().Fee
		if fee == "0" {
			ctx.Account.Flags |= state.LsfPasswordSpent
		}
	}

	return tx.TesSUCCESS
}

// removeSignersFromLedger removes the existing signer list from the ledger,
// adjusting the owner count based on whether lsfOneOwnerCount is set.
// Reference: rippled SetSignerList.cpp removeSignersFromLedger()
func removeSignersFromLedger(ctx *tx.ApplyContext, signerListKey, ownerDirKey keylet.Keylet) tx.Result {
	exists, _ := ctx.View.Exists(signerListKey)
	if !exists {
		// If the signer list doesn't exist we've already succeeded in deleting it.
		return tx.TesSUCCESS
	}

	// Read the existing signer list to determine the owner count adjustment.
	signerListData, err := ctx.View.Read(signerListKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	signerList, err := state.ParseSignerList(signerListData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// There are two different ways that the OwnerCount could be managed.
	// If lsfOneOwnerCount is set, remove just one owner count.
	// Otherwise use the pre-MultiSignReserve amendment calculation.
	// Reference: rippled SetSignerList.cpp:216-223
	removeFromOwnerCount := uint32(1)
	if (signerList.Flags & state.LsfOneOwnerCount) == 0 {
		// Old formula: 2 + entryCount
		removeFromOwnerCount = 2 + uint32(len(signerList.SignerEntries))
	}

	// Remove the node from the account directory.
	state.DirRemove(ctx.View, ownerDirKey, 0, signerListKey.Key, true)

	// Adjust owner count.
	if ctx.Account.OwnerCount >= removeFromOwnerCount {
		ctx.Account.OwnerCount -= removeFromOwnerCount
	} else {
		ctx.Account.OwnerCount = 0
	}

	// Erase the signer list.
	if err := ctx.View.Erase(signerListKey); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// signerCountBasedOwnerCountDelta computes the OwnerCount cost for a signer list
// when featureMultiSignReserve is NOT enabled.
// The rule is: 2 base + 1 per signer entry.
// Reference: rippled SetSignerList.cpp signerCountBasedOwnerCountDelta()
func signerCountBasedOwnerCountDelta(entryCount int) int {
	return 2 + entryCount
}

// Apply applies the SignerListSet transaction to ledger state.
// Reference: rippled SetSignerList.cpp preflight() + doApply(), replaceSignerList(), destroySignerList()
func (s *SignerListSet) Apply(ctx *tx.ApplyContext) tx.Result {
	// Check for invalid flags, gated behind fixInvalidTxFlags.
	// Reference: rippled SetSignerList.cpp preflight() lines 86-91
	if ctx.Rules().Enabled(amendment.FeatureFixInvalidTxFlags) {
		if s.GetFlags()&tx.TfUniversalMask != 0 {
			return tx.TemINVALID_FLAG
		}
	}

	ctx.Log.Trace("signer list set apply",
		"account", s.Account,
		"signerQuorum", s.SignerQuorum,
		"signerCount", len(s.SignerEntries),
	)

	signerListKey := keylet.SignerList(ctx.AccountID)
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)

	if s.SignerQuorum == 0 {
		// --- Destroy signer list ---
		// Reference: rippled SetSignerList.cpp destroySignerList()
		ctx.Log.Debug("signer list set: deleting signer list")

		// Destroying the signer list is only allowed if either the master key
		// is enabled or there is a regular key.
		// Reference: rippled SetSignerList.cpp:411-413
		isMasterDisabled := (ctx.Account.Flags & state.LsfDisableMaster) != 0
		hasRegularKey := ctx.Account.RegularKey != ""
		if isMasterDisabled && !hasRegularKey {
			ctx.Log.Warn("signer list set: no alternative key available")
			return tx.TecNO_ALTERNATIVE_KEY
		}

		return removeSignersFromLedger(ctx, signerListKey, ownerDirKey)
	}

	// --- Replace (or create) signer list ---
	// Reference: rippled SetSignerList.cpp replaceSignerList()

	// Preemptively remove any old signer list. May reduce the reserve,
	// so this is done before checking the reserve.
	if result := removeSignersFromLedger(ctx, signerListKey, ownerDirKey); result != tx.TesSUCCESS {
		return result
	}

	// Compute new reserve. Verify the account has funds to meet the reserve.
	oldOwnerCount := ctx.Account.OwnerCount

	// The required reserve changes based on featureMultiSignReserve.
	// Reference: rippled SetSignerList.cpp:359-366
	addedOwnerCount := 1
	flags := state.LsfOneOwnerCount
	if !ctx.Rules().Enabled(amendment.FeatureMultiSignReserve) {
		addedOwnerCount = signerCountBasedOwnerCountDelta(len(s.SignerEntries))
		flags = 0
	}

	newReserve := ctx.AccountReserve(oldOwnerCount + uint32(addedOwnerCount))

	// We check the reserve against the starting balance because we want to
	// allow dipping into the reserve to pay fees. This behavior is consistent
	// with CreateTicket.
	// Reference: rippled SetSignerList.cpp:374-375
	priorBalance := ctx.Account.Balance + ctx.Config.BaseFee
	if priorBalance < newReserve {
		ctx.Log.Warn("signer list set: insufficient reserve",
			"balance", priorBalance,
			"reserve", newReserve,
		)
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Build the signer entries for serialization.
	// Sort by account address, matching rippled's SetSignerList.cpp preflight() (line 66).
	sleEntries := make([]state.SignerEntry, len(s.SignerEntries))
	for i, e := range s.SignerEntries {
		sleEntries[i] = state.SignerEntry{
			Account:      e.SignerEntry.Account,
			SignerWeight: e.SignerEntry.SignerWeight,
		}
	}
	sort.Slice(sleEntries, func(i, j int) bool {
		return sleEntries[i].Account < sleEntries[j].Account
	})

	// Serialize and insert the new signer list.
	signerListData, err := state.SerializeSignerList(s.SignerQuorum, sleEntries, ctx.AccountID, flags)
	if err != nil {
		ctx.Log.Error("signer list set: failed to serialize signer list", "error", err)
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(signerListKey, signerListData); err != nil {
		ctx.Log.Error("signer list set: failed to insert signer list", "error", err)
		return tx.TefINTERNAL
	}

	// Add the signer list to the account's directory.
	state.DirInsert(ctx.View, ownerDirKey, signerListKey.Key, nil)

	// Adjust owner count.
	ctx.Account.OwnerCount += uint32(addedOwnerCount)

	return tx.TesSUCCESS
}
