package signerlist

import (
	"errors"
	"github.com/LeJamon/goXRPLd/internal/core/tx"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
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
			return errors.New("temMALFORMED: cannot have SignerEntries when deleting signer list")
		}
		return nil
	}

	// Must have at least one signer, max 32
	// Reference: rippled SetSignerList.cpp:270-276
	if len(s.SignerEntries) == 0 || len(s.SignerEntries) > 32 {
		return errors.New("temMALFORMED: too many or too few signers in signer list")
	}

	// Check for duplicates, self-reference, and weight validity
	// Reference: rippled SetSignerList.cpp:279-328
	var totalWeight uint32
	seenAccounts := make(map[string]bool)

	for _, entry := range s.SignerEntries {
		// Weight must be positive
		// Reference: rippled SetSignerList.cpp:298-303
		if entry.SignerEntry.SignerWeight == 0 {
			return errors.New("temBAD_WEIGHT: every signer must have a positive weight")
		}

		totalWeight += uint32(entry.SignerEntry.SignerWeight)

		// Cannot include self
		// Reference: rippled SetSignerList.cpp:307-311
		if entry.SignerEntry.Account == s.Account {
			return errors.New("temBAD_SIGNER: a signer may not self reference account")
		}

		// No duplicate accounts
		// Reference: rippled SetSignerList.cpp:284-288
		if seenAccounts[entry.SignerEntry.Account] {
			return errors.New("temBAD_SIGNER: duplicate signers in signer list")
		}
		seenAccounts[entry.SignerEntry.Account] = true
	}

	// Total weight must be >= quorum, and quorum must be positive
	// Reference: rippled SetSignerList.cpp:324-328
	if s.SignerQuorum == 0 || totalWeight < s.SignerQuorum {
		return errors.New("temBAD_QUORUM: quorum is unreachable")
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
func (s *SetRegularKey) Validate() error {
	return s.BaseTx.Validate()
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
// Reference: rippled SetRegularKey.cpp doApply()
func (s *SetRegularKey) Apply(ctx *tx.ApplyContext) tx.Result {
	if s.RegularKey != "" {
		// Setting a regular key
		if _, err := sle.DecodeAccountID(s.RegularKey); err != nil {
			return tx.TemINVALID
		}
		ctx.Account.RegularKey = s.RegularKey
	} else {
		// Clearing the regular key — check that an alternative auth method exists.
		// Reference: rippled SetRegularKey.cpp lines 86-98
		isMasterDisabled := (ctx.Account.Flags & sle.LsfDisableMaster) != 0
		if isMasterDisabled {
			signerListKey := keylet.SignerList(ctx.AccountID)
			hasSignerList, _ := ctx.View.Exists(signerListKey)
			if !hasSignerList {
				return tx.TecNO_ALTERNATIVE_KEY
			}
		}
		ctx.Account.RegularKey = ""
	}

	return tx.TesSUCCESS
}

// Apply applies the SignerListSet transaction to ledger state.
func (sl *SignerListSet) Apply(ctx *tx.ApplyContext) tx.Result {
	signerListKey := keylet.SignerList(ctx.AccountID)

	ownerDirKey := keylet.OwnerDir(ctx.AccountID)

	if sl.SignerQuorum == 0 {
		// Remove signer list
		// Reference: rippled SetSignerList.cpp destroySignerList()
		// Destroying the signer list is only allowed if either the master key
		// is enabled or there is a regular key.
		// Reference: rippled SetSignerList.cpp:411-413
		isMasterDisabled := (ctx.Account.Flags & sle.LsfDisableMaster) != 0
		hasRegularKey := ctx.Account.RegularKey != ""
		if isMasterDisabled && !hasRegularKey {
			return tx.TecNO_ALTERNATIVE_KEY
		}

		exists, _ := ctx.View.Exists(signerListKey)
		if exists {
			// Remove from owner directory
			// Reference: rippled SetSignerList.cpp removeSignersFromLedger
			sle.DirRemove(ctx.View, ownerDirKey, 0, signerListKey.Key, true)
			if err := ctx.View.Erase(signerListKey); err != nil {
				return tx.TefINTERNAL
			}
			if ctx.Account.OwnerCount > 0 {
				ctx.Account.OwnerCount--
			}
		}
	} else {
		sleEntries := make([]sle.SignerEntry, len(sl.SignerEntries))
		for i, e := range sl.SignerEntries {
			sleEntries[i] = sle.SignerEntry{
				Account:      e.SignerEntry.Account,
				SignerWeight: e.SignerEntry.SignerWeight,
			}
		}
		signerListData, err := sle.SerializeSignerList(sl.SignerQuorum, sleEntries, ctx.AccountID)
		if err != nil {
			return tx.TefINTERNAL
		}

		exists, _ := ctx.View.Exists(signerListKey)
		if exists {
			if err := ctx.View.Update(signerListKey, signerListData); err != nil {
				return tx.TefINTERNAL
			}
		} else {
			if err := ctx.View.Insert(signerListKey, signerListData); err != nil {
				return tx.TefINTERNAL
			}
			// Add to owner directory
			// Reference: rippled SetSignerList.cpp applySignerEntries
			sle.DirInsert(ctx.View, ownerDirKey, signerListKey.Key, nil)
			ctx.Account.OwnerCount++
		}
	}

	return tx.TesSUCCESS
}
