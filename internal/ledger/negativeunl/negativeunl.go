// Package negativeunl applies flag-ledger NegativeUNL transitions (pending
// ValidatorToDisable / ValidatorToReEnable → DisabledValidators) on a state map.
package negativeunl

import (
	"bytes"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Apply materializes pending ValidatorToDisable / ValidatorToReEnable on the
// NegativeUNL SLE, stamping ledgerIndex as FirstLedgerSequence of any newly
// disabled validator. No-op when the SLE or both transition fields are absent;
// erases the SLE when DisabledValidators becomes empty.
func Apply(stateMap *shamap.SHAMap, ledgerIndex uint32) error {
	key := keylet.NegativeUNL().Key
	item, exists, err := stateMap.Get(key)
	if err != nil || !exists || item == nil {
		return nil
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

	// Append ValidatorToDisable as a new entry, stamping the flag-ledger seq.
	if hasToDisable {
		sle.DisabledValidators = append(sle.DisabledValidators, pseudo.DisabledValidator{
			PublicKey:           sle.ValidatorToDisable,
			FirstLedgerSequence: ledgerIndex,
		})
	}

	sle.ValidatorToDisable = nil
	sle.ValidatorToReEnable = nil

	if len(sle.DisabledValidators) == 0 {
		return stateMap.Delete(key)
	}

	newData, err := pseudo.SerializeNegativeUNLSLE(sle)
	if err != nil {
		return fmt.Errorf("serialize updated NegativeUNL SLE: %w", err)
	}
	return stateMap.Put(key, newData)
}
