package state

import (
	"fmt"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// LedgerOffer represents an offer stored in the ledger
type LedgerOffer struct {
	Account           string
	Sequence          uint32
	TakerPays         Amount // What the offer creator wants
	TakerGets         Amount // What the offer creator is selling
	BookDirectory     [32]byte
	BookNode          uint64
	OwnerNode         uint64
	Expiration        uint32
	HasExpiration     bool
	Flags             uint32
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
	Sponsor           string

	// DomainID is the permissioned domain for this offer (optional, requires PermissionedDEX amendment)
	DomainID [32]byte

	// AdditionalBookDirectory and AdditionalBookNode are for hybrid offers
	// that are placed in both domain and open books
	AdditionalBookDirectory [32]byte
	AdditionalBookNode      uint64
	HasDomainID             bool
	HasSponsor              bool
	decoded                 ledgerfields.Offer
}

type offerBookLink struct {
	directory [32]byte
	node      uint64
}

// SerializeLedgerOffer serializes a LedgerOffer to binary for storage
func SerializeLedgerOffer(offer *LedgerOffer) ([]byte, error) {
	entry := offer.decoded
	account, err := DecodeAccountID(offer.Account)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Offer.Account: %w", err)
	}
	if err := entry.SetAccountValue(account); err != nil {
		return nil, fmt.Errorf("failed to encode Offer.Account: %w", err)
	}
	entry.SetFlags(offer.Flags)
	entry.SetSequence(offer.Sequence)
	if err := entry.SetTakerPaysValue(offer.TakerPays.LedgerValue()); err != nil {
		return nil, err
	}
	if err := entry.SetTakerGetsValue(offer.TakerGets.LedgerValue()); err != nil {
		return nil, err
	}
	entry.SetBookDirectoryValue(offer.BookDirectory)
	entry.SetBookNodeValue(offer.BookNode)
	entry.SetOwnerNodeValue(offer.OwnerNode)
	entry.SetPreviousTxnIDValue(offer.PreviousTxnID)
	entry.SetPreviousTxnLgrSeq(offer.PreviousTxnLgrSeq)
	if offer.HasSponsor || offer.Sponsor != "" {
		sponsor, err := DecodeAccountID(offer.Sponsor)
		if err != nil {
			return nil, fmt.Errorf("failed to decode Offer.Sponsor: %w", err)
		}
		if err := entry.SetSponsorValue(sponsor); err != nil {
			return nil, fmt.Errorf("failed to encode Offer.Sponsor: %w", err)
		}
	} else {
		entry.ClearSponsor()
	}

	if offer.HasExpiration || offer.Expiration > 0 {
		entry.SetExpiration(offer.Expiration)
	} else {
		entry.ClearExpiration()
	}
	if offer.HasDomainID || offer.DomainID != [32]byte{} {
		entry.SetDomainIDValue(offer.DomainID)
	} else {
		entry.ClearDomainID()
	}

	if err := setOfferAdditionalBooks(&entry, offer); err != nil {
		return nil, err
	}

	return entry.Encode()
}

// parseLedgerOffer parses a LedgerOffer from binary data
func parseLedgerOffer(data []byte) (*LedgerOffer, error) {
	var decoded ledgerfields.Offer
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode Offer: %w", err)
	}
	takerPaysValue, err := decoded.GetTakerPays()
	if err != nil {
		return nil, err
	}
	takerPays, err := decodeLedgerAmount("Offer.TakerPays", takerPaysValue)
	if err != nil {
		return nil, err
	}
	takerGetsValue, err := decoded.GetTakerGets()
	if err != nil {
		return nil, err
	}
	takerGets, err := decodeLedgerAmount("Offer.TakerGets", takerGetsValue)
	if err != nil {
		return nil, err
	}
	bookNode, err := decoded.GetBookNode()
	if err != nil {
		return nil, err
	}
	ownerNode, err := decoded.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	bookDirectory, err := decoded.GetBookDirectory()
	if err != nil {
		return nil, err
	}
	offer := &LedgerOffer{
		Sequence:          decoded.Sequence,
		TakerPays:         takerPays,
		TakerGets:         takerGets,
		BookDirectory:     bookDirectory,
		BookNode:          bookNode,
		OwnerNode:         ownerNode,
		Expiration:        decoded.Expiration,
		HasExpiration:     decoded.HasExpiration(),
		Flags:             decoded.Flags,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
		HasDomainID:       decoded.HasDomainID(),
		HasSponsor:        decoded.HasSponsor(),
		decoded:           decoded,
	}
	if decoded.HasAccount() {
		account, err := decoded.GetAccount()
		if err != nil {
			return nil, err
		}
		offer.Account, err = EncodeAccountID(account)
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnID() {
		offer.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasDomainID() {
		offer.DomainID, err = decoded.GetDomainID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasSponsor() {
		sponsor, err := decoded.GetSponsor()
		if err != nil {
			return nil, err
		}
		offer.Sponsor, err = EncodeAccountID(sponsor)
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasAdditionalBooks() {
		books, err := decoded.GetAdditionalBooks()
		if err != nil {
			return nil, err
		}
		if err := decodeAdditionalBook(books, offer); err != nil {
			return nil, err
		}
	}
	return offer, nil
}

func setOfferAdditionalBooks(entry *ledgerfields.Offer, offer *LedgerOffer) error {
	if entry.HasAdditionalBooks() {
		books, err := entry.GetAdditionalBooks()
		if err != nil {
			return err
		}
		if len(books) > 0 {
			directory, directoryErr := books[0].GetBookDirectory()
			node, nodeErr := books[0].GetBookNode()
			if directoryErr != nil {
				return directoryErr
			}
			if nodeErr != nil {
				return nodeErr
			}
			if directory == offer.AdditionalBookDirectory && node == offer.AdditionalBookNode {
				return nil
			}
		}
	}
	if offer.AdditionalBookDirectory == [32]byte{} {
		entry.ClearAdditionalBooks()
		return nil
	}
	book := ledgerfields.BookValue{}
	book.SetBookDirectoryValue(offer.AdditionalBookDirectory)
	book.SetBookNodeValue(offer.AdditionalBookNode)
	return entry.SetAdditionalBooksValue([]ledgerfields.BookValue{book})
}

func decodeAdditionalBook(books []ledgerfields.BookValue, offer *LedgerOffer) error {
	links, err := decodeAdditionalBooks(books)
	if err != nil || len(links) == 0 {
		return err
	}
	offer.AdditionalBookDirectory = links[0].directory
	offer.AdditionalBookNode = links[0].node
	return nil
}

func decodeAdditionalBooks(books []ledgerfields.BookValue) ([]offerBookLink, error) {
	links := make([]offerBookLink, 0, len(books))
	for i, value := range books {
		link, err := decodeAdditionalBookEntry(value, i)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, nil
}

func decodeAdditionalBookEntry(value ledgerfields.BookValue, index int) (offerBookLink, error) {
	var link offerBookLink
	directory, err := value.GetBookDirectory()
	if err != nil {
		return link, fmt.Errorf("Offer.AdditionalBooks[%d].BookDirectory: %w", index, err)
	}
	link.directory = directory
	node, err := value.GetBookNode()
	if err != nil {
		return link, fmt.Errorf("Offer.AdditionalBooks[%d].BookNode: %w", index, err)
	}
	link.node = node
	return link, nil
}

// ParseLedgerOffer parses a LedgerOffer from binary data.
func ParseLedgerOffer(data []byte) (*LedgerOffer, error) {
	return parseLedgerOffer(data)
}
