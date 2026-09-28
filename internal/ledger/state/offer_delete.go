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

	owner, err := offerOwnerAccount(offer)
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

func offerOwnerAccount(offer *LedgerOffer) ([20]byte, error) {
	if offer.decoded.HasAccount() {
		return offer.decoded.GetAccount()
	}
	return DecodeAccountID(offer.Account)
}

func offerAdditionalBookLinks(offer *LedgerOffer) ([]offerBookLink, error) {
	if offer.decoded.HasAdditionalBooks() {
		books, err := offer.decoded.GetAdditionalBooks()
		if err != nil {
			return nil, err
		}
		if len(books) > 0 {
			directory, directoryErr := books[0].GetBookDirectory()
			node, nodeErr := books[0].GetBookNode()
			if directoryErr != nil {
				return nil, directoryErr
			}
			if nodeErr != nil {
				return nil, nodeErr
			}
			if directory == offer.AdditionalBookDirectory && node == offer.AdditionalBookNode {
				return decodeAdditionalBooks(books)
			}
		}
	}
	if offer.AdditionalBookDirectory != ([32]byte{}) {
		return []offerBookLink{{
			directory: offer.AdditionalBookDirectory,
			node:      offer.AdditionalBookNode,
		}}, nil
	}
	return nil, nil
}
