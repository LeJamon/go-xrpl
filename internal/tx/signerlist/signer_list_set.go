package signerlist

import (
	"bytes"
	"sort"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

func (s *SignerListSet) TxType() tx.Type {
	return tx.TypeSignerListSet
}

func (s *SignerListSet) Validate() error {
	if err := s.BaseTx.Validate(); err != nil {
		return err
	}

	// determineOperation: a non-zero quorum with signer entries is a "set"; a
	// zero quorum with no entries is a "destroy". Any other combination is
	// malformed. The signer-entry validation (counts, weights, duplicates,
	// quorum) runs in PreflightRules via validateQuorumAndSignerEntries.
	// Reference: rippled SetSignerList.cpp determineOperation()
	hasEntries := len(s.SignerEntries) > 0
	switch {
	case s.SignerQuorum != 0 && hasEntries:
		return nil
	case s.SignerQuorum == 0 && !hasEntries:
		return nil
	default:
		return ter.Errorf(ter.TemMALFORMED, "invalid signer set list format")
	}
}

// GetFlagsMask adopts the engine FlagsMasker seam. rippled uses an
// amendment-conditional mask: with fixInvalidTxFlags any non-universal flag is
// rejected at preflight0; without it any flags are allowed.
// Reference: rippled SetSignerList.cpp getFlagsMask().
func (s *SignerListSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	if rules.Enabled(amendment.FeatureFixInvalidTxFlags) {
		return tx.TfUniversalMask
	}
	return 0
}

// PreflightRules runs the signer-entry validation for a set operation. rippled
// runs validateQuorumAndSignerEntries in the preflight body; Validate() only
// classifies set vs destroy, so a non-zero quorum reaching here is a set (the
// malformed combinations are already rejected).
func (s *SignerListSet) PreflightRules(rules *amendment.Rules) error {
	if s.SignerQuorum == 0 {
		return nil
	}
	if r := s.validateQuorumAndSignerEntries(); r != ter.TesSUCCESS {
		return ter.Errorf(r, "invalid signer entries")
	}
	return nil
}

// validateQuorumAndSignerEntries performs the signer-entry validation rippled
// runs in preflight: entry-count bounds (1..32), no duplicates, positive
// weights, no self-reference, and a reachable quorum. The check order matches
// rippled so a transaction malformed in more than one way reports the same TER.
func (s *SignerListSet) validateQuorumAndSignerEntries() ter.Result {
	if len(s.SignerEntries) < 1 || len(s.SignerEntries) > 32 {
		return ter.TemMALFORMED
	}

	seen := make(map[string]bool, len(s.SignerEntries))
	for _, e := range s.SignerEntries {
		if seen[e.SignerEntry.Account] {
			return ter.TemBAD_SIGNER
		}
		seen[e.SignerEntry.Account] = true
	}

	var totalWeight uint64
	for _, e := range s.SignerEntries {
		if e.SignerEntry.SignerWeight == 0 {
			return ter.TemBAD_WEIGHT
		}
		totalWeight += uint64(e.SignerEntry.SignerWeight)
		if e.SignerEntry.Account == s.Account {
			return ter.TemBAD_SIGNER
		}
	}

	if totalWeight < uint64(s.SignerQuorum) {
		return ter.TemBAD_QUORUM
	}
	return ter.TesSUCCESS
}

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

func (s *SetRegularKey) TxType() tx.Type {
	return tx.TypeRegularKeySet
}

func (s *SetRegularKey) Validate() error {
	return s.BaseTx.Validate()
}

// GetFlagsMask adopts the engine FlagsMasker seam. SetRegularKey defines no
// type-specific flags, so it uses the base universal mask (rippled does not
// override getFlagsMask for SetRegularKey).
// Reference: rippled Transactor.cpp getFlagsMask() = tfUniversalMask.
func (s *SetRegularKey) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// PreflightRules rejects setting the regular key to the account's own address,
// before any ledger-state check.
// Reference: rippled SetRegularKey.cpp preflight().
func (s *SetRegularKey) PreflightRules(rules *amendment.Rules) error {
	if s.RegularKey != "" && s.RegularKey == s.Account {
		return ter.Errorf(ter.TemBAD_REGKEY, "regular key cannot be the master key")
	}
	return nil
}

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

// Reference: rippled SetRegularKey.cpp doApply()
func (s *SetRegularKey) Apply(ctx *tx.ApplyContext) ter.Result {
	if s.RegularKey != "" {
		ctx.Log.Trace("set regular key apply",
			"account", s.Account,
			"regularKey", s.RegularKey,
		)
		// Setting a regular key
		if _, err := state.DecodeAccountID(s.RegularKey); err != nil {
			return ter.TemINVALID
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
				return ter.TecNO_ALTERNATIVE_KEY
			}
		}
		ctx.Account.RegularKey = ""
	}

	// Set lsfPasswordSpent iff this SetRegularKey received the free
	// password-change discount (its computed base fee was waived). rippled
	// binds the flag to !minimumFee(ctx_.baseFee) — the SAME base fee that
	// drives the preclaim fee floor — so the fee charged and the flag can
	// never disagree. Re-deriving "signed with master" here independently is
	// what let the two drift and fork account_hash (#732).
	// Reference: rippled SetRegularKey.cpp doApply lines 83-84.
	if tx.SetRegularKeyFeeWaived(ctx.Config.SkipSignatureVerification, s.GetCommon(), ctx.Account) {
		ctx.Account.Flags |= state.LsfPasswordSpent
	}

	return ter.TesSUCCESS
}

// removeSignersFromLedger removes the existing signer list from the ledger,
// adjusting the owner count based on whether lsfOneOwnerCount is set.
// Reference: rippled SetSignerList.cpp removeSignersFromLedger()
func removeSignersFromLedger(ctx *tx.ApplyContext, signerListKey, ownerDirKey keylet.Keylet) ter.Result {
	exists, _ := ctx.View.Exists(signerListKey)
	if !exists {
		// If the signer list doesn't exist we've already succeeded in deleting it.
		return ter.TesSUCCESS
	}

	// Read the existing signer list to determine the owner count adjustment.
	signerListData, err := ctx.View.Read(signerListKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	signerList, err := state.ParseSignerList(signerListData)
	if err != nil {
		return ter.TefINTERNAL
	}
	sponsorAddress, err := tx.LedgerEntrySponsor(signerListData, "Sponsor")
	if err != nil {
		return ctx.Internal("SignerListSet.Remove.Sponsor", err)
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

	// Remove the node from the account directory, using the page recorded in
	// sfOwnerNode (so a signer list on a paginated owner directory is correctly
	// unlinked) and keepRoot=false (so an empty owner-directory root is erased
	// when the signer list was the account's last owned object).
	// Reference: rippled SetSignerList.cpp:226-228
	//   hint = (*signers)[sfOwnerNode]; dirRemove(ownerDirKeylet, hint, key, false).
	removed, err := state.DirRemove(ctx.View, ownerDirKey, signerList.OwnerNode, signerListKey.Key, false)
	if err != nil || removed == nil || !removed.Success {
		return ter.TefBAD_LEDGER
	}

	// Adjust owner count.
	if result := tx.DecreaseOwnerCountFor(ctx, ctx.AccountID, sponsorAddress, removeFromOwnerCount); result != ter.TesSUCCESS {
		return result
	}

	// Erase the signer list.
	if err := ctx.View.Erase(signerListKey); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// Reference: rippled SetSignerList.cpp preflight() + doApply(), replaceSignerList(), destroySignerList()
func (s *SignerListSet) Apply(ctx *tx.ApplyContext) ter.Result {
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
			return ter.TecNO_ALTERNATIVE_KEY
		}

		return removeSignersFromLedger(ctx, signerListKey, ownerDirKey)
	}

	// --- Replace (or create) signer list ---
	// Reference: rippled SetSignerList.cpp replaceSignerList()

	// Signer-entry validity (counts, weights, duplicates, quorum) is enforced in
	// PreflightRules — the outer path and preflightInner both run it, so by the
	// time Apply runs the entries are known valid.

	// Preemptively remove any old signer list. May reduce the reserve,
	// so this is done before checking the reserve.
	if result := removeSignersFromLedger(ctx, signerListKey, ownerDirKey); result != ter.TesSUCCESS {
		return result
	}

	// Compute new reserve. Verify the account has funds to meet the reserve.
	// A signer list costs a flat one owner-reserve unit and carries
	// lsfOneOwnerCount.
	addedOwnerCount := 1
	flags := state.LsfOneOwnerCount

	// We check the reserve against the starting balance because we want to
	// allow dipping into the reserve to pay fees. This behavior is consistent
	// with CreateTicket.
	priorBalance := ctx.PriorBalance()
	if result := ctx.CheckReserveFor(ctx.AccountID, ctx.Account, priorBalance, addedOwnerCount, 0, ter.TecINSUFFICIENT_RESERVE); result != ter.TesSUCCESS {
		ctx.Log.Warn("signer list set: insufficient reserve",
			"balance", priorBalance,
		)
		return result
	}

	// Build the signer entries for serialization.
	// Sort by the decoded 20-byte AccountID, matching rippled's
	// determineOperation std::sort over SignerEntry (SetSignerList.cpp:66),
	// whose operator< compares AccountID (SignerEntries.h:67-70). Sorting by
	// the base58 string instead reorders entries — base58 order does not match
	// AccountID byte order — diverging the SLE bytes from rippled.
	sleEntries := make([]state.SignerEntry, len(s.SignerEntries))
	for i, e := range s.SignerEntries {
		sleEntries[i] = state.SignerEntry{
			Account:       e.SignerEntry.Account,
			SignerWeight:  e.SignerEntry.SignerWeight,
			WalletLocator: e.SignerEntry.WalletLocator,
		}
	}
	sort.Slice(sleEntries, func(i, j int) bool {
		a, _ := state.DecodeAccountID(sleEntries[i].Account)
		b, _ := state.DecodeAccountID(sleEntries[j].Account)
		return bytes.Compare(a[:], b[:]) < 0
	})

	// Add the signer list to the account's directory first so sfOwnerNode
	// records the actual page (and so the directory's sfOwner is set).
	// Reference: rippled SetSignerList.cpp:384-393.
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, signerListKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}

	// fixIncludeKeyletFields: store sfOwner (a keylet input) on the list.
	var owner *[20]byte
	if ctx.Rules().Enabled(amendment.FeatureFixIncludeKeyletFields) {
		owner = &ctx.AccountID
	}

	signerListData, err := state.SerializeSignerList(s.SignerQuorum, sleEntries, flags, true, dirResult.Page, owner)
	if err != nil {
		ctx.Log.Error("signer list set: failed to serialize signer list", "error", err)
		return ter.TefINTERNAL
	}
	sponsorAddress, result := tx.IncreaseOwnerCount(ctx, ctx.AccountID, ctx.Account, uint32(addedOwnerCount))
	if result != ter.TesSUCCESS {
		return result
	}
	signerListData, err = tx.SetLedgerEntrySponsor(signerListData, "Sponsor", sponsorAddress)
	if err != nil {
		return ctx.Internal("SignerListSet.SetSponsor", err)
	}

	if err := ctx.View.Insert(signerListKey, signerListData); err != nil {
		ctx.Log.Error("signer list set: failed to insert signer list", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}
