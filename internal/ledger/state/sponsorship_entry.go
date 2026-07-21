package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// SponsorshipData is the serialized state of one directional sponsor/sponsee
// relationship. FeeAmount and MaxFee are native XRP drops; their Has* bits
// preserve the distinction between an absent optional field and present zero.
type SponsorshipData struct {
	Owner               [20]byte
	Sponsee             [20]byte
	FeeAmount           uint64
	HasFeeAmount        bool
	MaxFee              uint64
	HasMaxFee           bool
	RemainingOwnerCount uint32
	OwnerNode           uint64
	SponseeNode         uint64
	Flags               uint32
	Sponsor             [20]byte
	HasSponsor          bool
	PreviousTxnID       [32]byte
	PreviousTxnLgrSeq   uint32
}

// ParseSponsorship decodes a Sponsorship ledger entry.
func ParseSponsorship(data []byte) (*SponsorshipData, error) {
	var decoded ledgerfields.Sponsorship
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode Sponsorship: %w", err)
	}
	fields := decoded.ToMap()
	entry := &SponsorshipData{
		RemainingOwnerCount: decoded.RemainingOwnerCount,
		Flags:               decoded.Flags,
		PreviousTxnLgrSeq:   decoded.PreviousTxnLgrSeq,
		HasFeeAmount:        fields["FeeAmount"] != nil,
		HasMaxFee:           fields["MaxFee"] != nil,
		HasSponsor:          fields["Sponsor"] != nil,
	}

	var err error
	entry.Owner, err = decodeLedgerAccount("Sponsorship.Owner", decoded.Owner)
	if err != nil {
		return nil, err
	}
	entry.Sponsee, err = decodeLedgerAccount("Sponsorship.Sponsee", decoded.Sponsee)
	if err != nil {
		return nil, err
	}
	entry.OwnerNode, err = parseLedgerUint64("Sponsorship.OwnerNode", decoded.OwnerNode)
	if err != nil {
		return nil, err
	}
	entry.SponseeNode, err = parseLedgerUint64("Sponsorship.SponseeNode", decoded.SponseeNode)
	if err != nil {
		return nil, err
	}
	if entry.HasFeeAmount {
		entry.FeeAmount, err = decodeNativeLedgerBalance("Sponsorship.FeeAmount", decoded.FeeAmount)
		if err != nil {
			return nil, err
		}
	}
	if entry.HasMaxFee {
		entry.MaxFee, err = decodeNativeLedgerBalance("Sponsorship.MaxFee", decoded.MaxFee)
		if err != nil {
			return nil, err
		}
	}
	if entry.HasSponsor {
		entry.Sponsor, err = decodeLedgerAccount("Sponsorship.Sponsor", decoded.Sponsor)
		if err != nil {
			return nil, err
		}
	}
	if err := decodeLedgerHex("Sponsorship.PreviousTxnID", decoded.PreviousTxnID, entry.PreviousTxnID[:]); err != nil {
		return nil, err
	}
	return entry, nil
}

// SerializeSponsorship encodes a Sponsorship ledger entry using the canonical
// field definitions shared with transaction metadata.
func SerializeSponsorship(entry *SponsorshipData) ([]byte, error) {
	if entry == nil {
		return nil, errors.New("failed to encode Sponsorship: nil entry")
	}
	owner, err := addresscodec.EncodeAccountIDToClassicAddress(entry.Owner[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode Sponsorship owner: %w", err)
	}
	sponsee, err := addresscodec.EncodeAccountIDToClassicAddress(entry.Sponsee[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode Sponsorship sponsee: %w", err)
	}

	encoded := &ledgerfields.Sponsorship{}
	encoded.SetOwner(owner)
	encoded.SetSponsee(sponsee)
	encoded.SetOwnerNode(fmt.Sprintf("%x", entry.OwnerNode))
	encoded.SetSponseeNode(fmt.Sprintf("%x", entry.SponseeNode))
	encoded.SetFlags(entry.Flags)
	encoded.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(entry.PreviousTxnID[:])))
	encoded.SetPreviousTxnLgrSeq(entry.PreviousTxnLgrSeq)
	if entry.HasFeeAmount {
		encoded.SetFeeAmount(fmt.Sprintf("%d", entry.FeeAmount))
	}
	if entry.HasMaxFee {
		encoded.SetMaxFee(fmt.Sprintf("%d", entry.MaxFee))
	}
	if entry.RemainingOwnerCount != 0 {
		encoded.SetRemainingOwnerCount(entry.RemainingOwnerCount)
	}
	if entry.HasSponsor {
		sponsor, err := addresscodec.EncodeAccountIDToClassicAddress(entry.Sponsor[:])
		if err != nil {
			return nil, fmt.Errorf("failed to encode Sponsorship reserve sponsor: %w", err)
		}
		encoded.SetSponsor(sponsor)
	}

	data, err := encoded.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode Sponsorship: %w", err)
	}
	return data, nil
}
