package state

import (
	"encoding/hex"
	"fmt"
	"strings"

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

	// DomainID is the permissioned domain for this offer (optional, requires PermissionedDEX amendment)
	DomainID [32]byte

	// AdditionalBookDirectory and AdditionalBookNode are for hybrid offers
	// that are placed in both domain and open books
	AdditionalBookDirectory [32]byte
	AdditionalBookNode      uint64
	decodedOptionals        map[string]any
}

type offerBookLink struct {
	directory [32]byte
	node      uint64
}

// SerializeLedgerOffer serializes a LedgerOffer to binary for storage
func SerializeLedgerOffer(offer *LedgerOffer) ([]byte, error) {
	amountValue := func(amt Amount) any {
		if amt.IsNative() {
			return amt.Value()
		}
		if amt.IsMPT() {
			return map[string]any{
				"value":           amt.Value(),
				"mpt_issuance_id": amt.MPTIssuanceID(),
			}
		}
		return map[string]any{
			"value":    amt.Value(),
			"currency": amt.Currency,
			"issuer":   amt.Issuer,
		}
	}

	entry := &ledgerfields.Offer{}
	entry.SetAccount(offer.Account)
	entry.SetFlags(offer.Flags)
	entry.SetSequence(offer.Sequence)
	entry.SetTakerPays(amountValue(offer.TakerPays))
	entry.SetTakerGets(amountValue(offer.TakerGets))
	entry.SetBookDirectory(strings.ToUpper(hex.EncodeToString(offer.BookDirectory[:])))
	entry.SetBookNode(fmt.Sprintf("%x", offer.BookNode))
	entry.SetOwnerNode(fmt.Sprintf("%x", offer.OwnerNode))
	entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(offer.PreviousTxnID[:])))
	entry.SetPreviousTxnLgrSeq(offer.PreviousTxnLgrSeq)

	if offer.Expiration > 0 || decodedFieldUnchanged(offer.decodedOptionals, "Expiration", offer.Expiration) {
		entry.SetExpiration(offer.Expiration)
	}
	var zeroDomainID [32]byte
	if offer.DomainID != zeroDomainID || decodedFieldUnchanged(offer.decodedOptionals, "DomainID", offer.DomainID) {
		entry.SetDomainID(strings.ToUpper(hex.EncodeToString(offer.DomainID[:])))
	}

	var zeroBookDir [32]byte
	additionalBookUnchanged := decodedFieldUnchanged(offer.decodedOptionals, "AdditionalBookDirectory", offer.AdditionalBookDirectory) &&
		decodedFieldUnchanged(offer.decodedOptionals, "AdditionalBookNode", offer.AdditionalBookNode)
	if raw, ok := offer.decodedOptionals["AdditionalBooks"].([]any); ok && additionalBookUnchanged {
		entry.SetAdditionalBooks(raw)
	} else if offer.AdditionalBookDirectory != zeroBookDir {
		entry.SetAdditionalBooks([]any{
			map[string]any{
				"Book": map[string]any{
					"BookDirectory": strings.ToUpper(hex.EncodeToString(offer.AdditionalBookDirectory[:])),
					"BookNode":      fmt.Sprintf("%x", offer.AdditionalBookNode),
				},
			},
		})
	}

	return entry.Encode()
}

// parseLedgerOffer parses a LedgerOffer from binary data
func parseLedgerOffer(data []byte) (*LedgerOffer, error) {
	var decoded ledgerfields.Offer
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode Offer: %w", err)
	}
	takerPays, err := decodeLedgerAmount("Offer.TakerPays", decoded.TakerPays)
	if err != nil {
		return nil, err
	}
	takerGets, err := decodeLedgerAmount("Offer.TakerGets", decoded.TakerGets)
	if err != nil {
		return nil, err
	}
	bookNode, err := parseLedgerUint64("Offer.BookNode", decoded.BookNode)
	if err != nil {
		return nil, err
	}
	ownerNode, err := parseLedgerUint64("Offer.OwnerNode", decoded.OwnerNode)
	if err != nil {
		return nil, err
	}

	fields := decoded.ToMap()
	offer := &LedgerOffer{
		Account:           decoded.Account,
		Sequence:          decoded.Sequence,
		TakerPays:         takerPays,
		TakerGets:         takerGets,
		BookNode:          bookNode,
		OwnerNode:         ownerNode,
		Expiration:        decoded.Expiration,
		Flags:             decoded.Flags,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
		decodedOptionals:  make(map[string]any),
	}
	for _, hash := range []struct {
		field string
		value string
		dst   []byte
	}{
		{"BookDirectory", decoded.BookDirectory, offer.BookDirectory[:]},
		{"PreviousTxnID", decoded.PreviousTxnID, offer.PreviousTxnID[:]},
		{"DomainID", decoded.DomainID, offer.DomainID[:]},
	} {
		if _, ok := fields[hash.field]; !ok {
			continue
		}
		if err := decodeLedgerHex("Offer."+hash.field, hash.value, hash.dst); err != nil {
			return nil, err
		}
	}
	if _, ok := fields["Expiration"]; ok {
		offer.decodedOptionals["Expiration"] = offer.Expiration
	}
	if _, ok := fields["DomainID"]; ok {
		offer.decodedOptionals["DomainID"] = offer.DomainID
	}
	if _, ok := fields["AdditionalBooks"]; ok {
		if err := decodeAdditionalBook(decoded.AdditionalBooks, offer); err != nil {
			return nil, err
		}
		offer.decodedOptionals["AdditionalBooks"] = decoded.AdditionalBooks
		offer.decodedOptionals["AdditionalBookDirectory"] = offer.AdditionalBookDirectory
		offer.decodedOptionals["AdditionalBookNode"] = offer.AdditionalBookNode
	}
	return offer, nil
}

func decodeAdditionalBook(books []any, offer *LedgerOffer) error {
	links, err := decodeAdditionalBooks(books)
	if err != nil || len(links) == 0 {
		return err
	}
	offer.AdditionalBookDirectory = links[0].directory
	offer.AdditionalBookNode = links[0].node
	return nil
}

func decodeAdditionalBooks(books []any) ([]offerBookLink, error) {
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

func decodeAdditionalBookEntry(value any, index int) (offerBookLink, error) {
	var link offerBookLink
	element, ok := value.(map[string]any)
	if !ok {
		return link, fmt.Errorf("Offer.AdditionalBooks[%d]: decoded element has type %T", index, value)
	}
	bookValue, ok := element["Book"]
	if !ok {
		return link, fmt.Errorf("Offer.AdditionalBooks[%d]: missing Book", index)
	}
	book, ok := bookValue.(map[string]any)
	if !ok {
		return link, fmt.Errorf("Offer.AdditionalBooks[%d].Book: decoded value has type %T", index, bookValue)
	}
	directory, ok := book["BookDirectory"].(string)
	if !ok {
		return link, fmt.Errorf("Offer.AdditionalBooks[%d].BookDirectory: decoded value has type %T", index, book["BookDirectory"])
	}
	if err := decodeLedgerHex(fmt.Sprintf("Offer.AdditionalBooks[%d].BookDirectory", index), directory, link.directory[:]); err != nil {
		return link, err
	}
	node, ok := book["BookNode"].(string)
	if !ok {
		return link, fmt.Errorf("Offer.AdditionalBooks[%d].BookNode: decoded value has type %T", index, book["BookNode"])
	}
	var err error
	link.node, err = parseLedgerUint64(fmt.Sprintf("Offer.AdditionalBooks[%d].BookNode", index), node)
	return link, err
}

// ParseLedgerOffer parses a LedgerOffer from binary data.
func ParseLedgerOffer(data []byte) (*LedgerOffer, error) {
	return parseLedgerOffer(data)
}
