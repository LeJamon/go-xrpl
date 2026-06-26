// Package negativeunl applies the flag-ledger NegativeUNL transitions
// (materializing pending ValidatorToDisable / ValidatorToReEnable into the
// DisabledValidators set) directly on a ledger's state map.
package negativeunl

import (
	"bytes"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Apply materializes the pending ValidatorToDisable / ValidatorToReEnable
// transitions on the NegativeUNL SLE in stateMap, stamping ledgerIndex as the
// FirstLedgerSequence of any newly disabled validator.
//
// It is a no-op when there is no NegativeUNL SLE, or when neither
// ValidatorToDisable nor ValidatorToReEnable is present. When the resulting
// DisabledValidators set is empty the SLE is erased.
func Apply(stateMap *shamap.SHAMap, ledgerIndex uint32) error {
	key := keylet.NegativeUNL().Key
	item, exists, err := stateMap.Get(key)
	if err != nil || !exists || item == nil {
		return nil // no SLE → nothing to do
	}
	data := item.Data()
	if len(data) == 0 {
		return nil
	}

	sle, err := pseudo.ParseNegativeUNLSLE(data)
	if err != nil {
		return fmt.Errorf("parse NegativeUNL SLE: %w", err)
	}

	hasToDisable := len(sle.ValidatorToDisable) > 0
	hasToReEnable := len(sle.ValidatorToReEnable) > 0
	if !hasToDisable && !hasToReEnable {
		return nil
	}

	// Filter DisabledValidators: drop any entry matching ValidatorToReEnable.
	if hasToReEnable {
		filtered := sle.DisabledValidators[:0]
		for _, dv := range sle.DisabledValidators {
			if bytes.Equal(dv.PublicKey, sle.ValidatorToReEnable) {
				continue
			}
			filtered = append(filtered, dv)
		}
		sle.DisabledValidators = filtered
	}

	// Append ValidatorToDisable (if any) as a new DisabledValidators entry,
	// stamping the flag-ledger sequence as sfFirstLedgerSequence.
	if hasToDisable {
		sle.DisabledValidators = append(sle.DisabledValidators, pseudo.DisabledValidator{
			PublicKey:           sle.ValidatorToDisable,
			FirstLedgerSequence: ledgerIndex,
		})
	}

	// Clear the transition fields.
	sle.ValidatorToDisable = nil
	sle.ValidatorToReEnable = nil

	// Serialize + write back (or erase if the SLE is now empty).
	if len(sle.DisabledValidators) == 0 {
		return stateMap.Delete(key)
	}

	newData, err := pseudo.SerializeNegativeUNLSLE(sle)
	if err != nil {
		return fmt.Errorf("serialize updated NegativeUNL SLE: %w", err)
	}
	return stateMap.Put(key, newData)
}
