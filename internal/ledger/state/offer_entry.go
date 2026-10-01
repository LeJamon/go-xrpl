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
	decoded                 ledgerfields.Offer
}

type offerBookLink struct {
	directory [32]byte
	node      uint64
}

// SerializeLedgerOffer serializes a LedgerOffer to binary for storage
func SerializeLedgerOffer(offer *LedgerOffer) ([]byte, error) {
	entry := offer.decoded
	entry.SetAccount(offer.Account)
	entry.SetFlagsValue(offer.Flags)
	entry.SetSequenceValue(offer.Sequence)
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
	entry.SetPreviousTxnLgrSeqValue(offer.PreviousTxnLgrSeq)
	if offer.Sponsor != "" || (entry.HasSponsor() && offer.Sponsor == entry.Sponsor) {
		entry.SetSponsor(offer.Sponsor)
	} else {
		entry.ClearSponsor()
	}

	if offer.Expiration > 0 || (entry.HasExpiration() && offer.Expiration == entry.Expiration) {
		entry.SetExpirationValue(offer.Expiration)
	} else {
		entry.ClearExpiration()
	}
	domainIDUnchanged := false
	if entry.HasDomainID() {
		original, err := entry.GetDomainID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode Offer.DomainID: %w", err)
		}
		domainIDUnchanged = original == offer.DomainID
	}
	if offer.DomainID != [32]byte{} || domainIDUnchanged {
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
		Flags:             decoded.Flags,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
		decoded:           decoded,
	}
	if decoded.HasAccount() {
		offer.Account = decoded.Account
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
		offer.Sponsor = decoded.Sponsor
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
		} else if offer.AdditionalBookDirectory == [32]byte{} {
			// Preserve an explicitly present empty array when the modeled
			// values are unchanged.
			return nil
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
