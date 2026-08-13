package invariants

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func checkObjectsHavePseudoAccounts(entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureFixCleanup3_3_0) || view == nil {
		return nil
	}

	for _, e := range entries {
		if !e.IsDelete || !isPseudoObjectType(e.EntryType) {
			continue
		}
		before := e.Before
		if before == nil {
			before = e.DeleteFinal
		}
		if before == nil {
			return &InvariantViolation{
				Name:    "ObjectHasPseudoAccount",
				Message: fmt.Sprintf("deleted %s is missing before state", e.EntryType),
			}
		}
		accountID, err := pseudoObjectAccountID(before)
		if err != nil {
			return &InvariantViolation{
				Name:    "ObjectHasPseudoAccount",
				Message: fmt.Sprintf("deleted %s has invalid pseudo-account field: %v", e.EntryType, err),
			}
		}
		exists, err := view.Exists(keylet.Account(accountID))
		if err != nil {
			return &InvariantViolation{
				Name:    "ObjectHasPseudoAccount",
				Message: fmt.Sprintf("could not inspect pseudo-account for deleted %s: %v", e.EntryType, err),
			}
		}
		if exists {
			return &InvariantViolation{
				Name:    "ObjectHasPseudoAccount",
				Message: fmt.Sprintf("deleted %s without deleting its pseudo-account", e.EntryType),
			}
		}
	}
	return nil
}

func isPseudoObjectType(t entry.Type) bool {
	return t == entry.TypeAMM || t == entry.TypeVault || t == entry.TypeLoanBroker
}

func pseudoObjectAccountID(data []byte) ([20]byte, error) {
	var zero [20]byte
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		return zero, err
	}
	account, ok := fields["Account"].(string)
	if !ok || account == "" {
		return zero, fmt.Errorf("missing Account")
	}
	return state.DecodeAccountID(account)
}
