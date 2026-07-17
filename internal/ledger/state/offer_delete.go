package state

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/keylet"
)

// DeleteOffer removes an Offer from every directory that indexes it, then
// erases the Offer. A nil Offer is a successful no-op and reports removed=false.
// Directory removals are applied in ledger order and are not rolled back if a
// later removal fails; callers that require atomicity must provide a sandboxed
// view. OwnerCount adjustment remains the caller's responsibility.
func DeleteOffer(view LedgerView, offerKey keylet.Keylet, offer *LedgerOffer) (bool, error) {
	if offer == nil {
		return false, nil
	}

	owner, err := DecodeAccountID(offer.Account)
	if err != nil {
		return false, fmt.Errorf("decode offer owner: %w", err)
	}
	additionalBooks, err := offerAdditionalBookLinks(offer)
	if err != nil {
		return false, err
	}

	directories := make([]offerBookLink, 0, 2+len(additionalBooks))
	directories = append(directories,
		offerBookLink{directory: keylet.OwnerDir(owner).Key, node: offer.OwnerNode},
		offerBookLink{directory: offer.BookDirectory, node: offer.BookNode},
	)
	directories = append(directories, additionalBooks...)

	for _, directory := range directories {
		result, removeErr := DirRemove(
			view,
			keylet.Keylet{Key: directory.directory},
			directory.node,
			offerKey.Key,
			false,
		)
		if removeErr != nil {
			return false, removeErr
		}
		if result == nil || !result.Success {
			return false, nil
		}
	}

	if err := view.Erase(offerKey); err != nil {
		return false, err
	}
	return true, nil
}

func offerAdditionalBookLinks(offer *LedgerOffer) ([]offerBookLink, error) {
	additionalBookUnchanged := decodedFieldUnchanged(offer.decodedOptionals, "AdditionalBookDirectory", offer.AdditionalBookDirectory) &&
		decodedFieldUnchanged(offer.decodedOptionals, "AdditionalBookNode", offer.AdditionalBookNode)
	if raw, ok := offer.decodedOptionals["AdditionalBooks"].([]any); ok && additionalBookUnchanged {
		return decodeAdditionalBooks(raw)
	}
	if offer.AdditionalBookDirectory != ([32]byte{}) {
		return []offerBookLink{{
			directory: offer.AdditionalBookDirectory,
			node:      offer.AdditionalBookNode,
		}}, nil
	}
	return nil, nil
}
