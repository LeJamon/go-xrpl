package state

import (
	"errors"
	"fmt"
	"strconv"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// SponsorshipData is the serialized state of one directional sponsor/sponsee
// relationship. FeeAmount and MaxFee are native XRP drops; their Has* bits
// preserve the distinction between an absent optional field and present zero.
type SponsorshipData struct {
	Owner                [20]byte
	Sponsee              [20]byte
	FeeAmount            uint64
	HasFeeAmount         bool
	MaxFee               uint64
	HasMaxFee            bool
	RemainingOwnerCount  uint32
	OwnerNode            uint64
	SponseeNode          uint64
	Flags                uint32
	Sponsor              [20]byte
	HasSponsor           bool
	PreviousTxnID        [32]byte
	PreviousTxnLgrSeq    uint32
	preserveEmptyOwner   bool
	preserveEmptySponsee bool
	preserveEmptySponsor bool
}

func ParseSponsorship(data []byte) (*SponsorshipData, error) {
	var decoded ledgerfields.Sponsorship
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode Sponsorship: %w", err)
	}
	entry := &SponsorshipData{
		HasFeeAmount:         decoded.HasFeeAmount(),
		HasMaxFee:            decoded.HasMaxFee(),
		HasSponsor:           decoded.HasSponsor(),
		preserveEmptyOwner:   decoded.Owner == "",
		preserveEmptySponsee: decoded.Sponsee == "",
		preserveEmptySponsor: decoded.Sponsor == "",
	}

	var err error
	entry.Owner, err = decoded.GetOwner()
	if err != nil {
		return nil, err
	}
	entry.Sponsee, err = decoded.GetSponsee()
	if err != nil {
		return nil, err
	}
	entry.OwnerNode, err = decoded.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	entry.SponseeNode, err = decoded.GetSponseeNode()
	if err != nil {
		return nil, err
	}
	entry.Flags, err = decoded.GetFlags()
	if err != nil {
		return nil, err
	}
	if entry.HasFeeAmount {
		value, err := decoded.GetFeeAmount()
		if err != nil {
			return nil, err
		}
		entry.FeeAmount, err = decodeNativeLedgerBalance("Sponsorship.FeeAmount", value)
		if err != nil {
			return nil, err
		}
	}
	if entry.HasMaxFee {
		value, err := decoded.GetMaxFee()
		if err != nil {
			return nil, err
		}
		entry.MaxFee, err = decodeNativeLedgerBalance("Sponsorship.MaxFee", value)
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasRemainingOwnerCount() {
		entry.RemainingOwnerCount, err = decoded.GetRemainingOwnerCount()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasSponsor {
		entry.Sponsor, err = decoded.GetSponsor()
		if err != nil {
			return nil, err
		}
	}
	entry.PreviousTxnID, err = decoded.GetPreviousTxnID()
	if err != nil {
		return nil, err
	}
	entry.PreviousTxnLgrSeq, err = decoded.GetPreviousTxnLgrSeq()
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func SerializeSponsorship(entry *SponsorshipData) ([]byte, error) {
	if entry == nil {
		return nil, errors.New("failed to encode Sponsorship: nil entry")
	}

	encoded := &ledgerfields.Sponsorship{}
	if entry.preserveEmptyOwner && entry.Owner == [20]byte{} {
		encoded.SetOwner("")
	} else if err := encoded.SetOwnerValue(entry.Owner); err != nil {
		return nil, fmt.Errorf("failed to encode Sponsorship.Owner: %w", err)
	}
	if entry.preserveEmptySponsee && entry.Sponsee == [20]byte{} {
		encoded.SetSponsee("")
	} else if err := encoded.SetSponseeValue(entry.Sponsee); err != nil {
		return nil, fmt.Errorf("failed to encode Sponsorship.Sponsee: %w", err)
	}
	encoded.SetOwnerNodeValue(entry.OwnerNode)
	encoded.SetSponseeNodeValue(entry.SponseeNode)
	encoded.SetFlagsValue(entry.Flags)
	encoded.SetPreviousTxnIDValue(entry.PreviousTxnID)
	encoded.SetPreviousTxnLgrSeqValue(entry.PreviousTxnLgrSeq)
	if entry.HasFeeAmount {
		if err := encoded.SetFeeAmountValue(ledgerfields.AmountValue{Value: strconv.FormatUint(entry.FeeAmount, 10)}); err != nil {
			return nil, fmt.Errorf("failed to encode Sponsorship.FeeAmount: %w", err)
		}
	} else {
		encoded.ClearFeeAmount()
	}
	if entry.HasMaxFee {
		if err := encoded.SetMaxFeeValue(ledgerfields.AmountValue{Value: strconv.FormatUint(entry.MaxFee, 10)}); err != nil {
			return nil, fmt.Errorf("failed to encode Sponsorship.MaxFee: %w", err)
		}
	} else {
		encoded.ClearMaxFee()
	}
	if entry.RemainingOwnerCount != 0 {
		encoded.SetRemainingOwnerCountValue(entry.RemainingOwnerCount)
	} else {
		encoded.ClearRemainingOwnerCount()
	}
	if entry.HasSponsor {
		if entry.preserveEmptySponsor && entry.Sponsor == [20]byte{} {
			encoded.SetSponsor("")
		} else if err := encoded.SetSponsorValue(entry.Sponsor); err != nil {
			return nil, fmt.Errorf("failed to encode Sponsorship.Sponsor: %w", err)
		}
	} else {
		encoded.ClearSponsor()
	}

	data, err := encoded.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode Sponsorship: %w", err)
	}
	return data, nil
}
