package invariants

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// sponsorshipState contains the account sponsorship counters and the reserve
// units represented by a sponsored object.
type sponsorshipState struct {
	account          *state.AccountRoot
	objectOwnerCount int64
}

// checkSponsorship runs the two sponsorship conservation invariants. These
// checks are intentionally unconditional: the ledger schemas already carry
// sponsorship fields, and any transaction that changes them must conserve the
// corresponding counters.
func checkSponsorship(entries []InvariantEntry) *InvariantViolation {
	if violation := checkSponsorshipOwnerCounts(entries); violation != nil {
		return violation
	}
	return checkSponsorshipAccountCount(entries)
}

func checkSponsorshipOwnerCounts(entries []InvariantEntry) *InvariantViolation {
	var (
		sponsoredDelta       int64
		sponsoringDelta      int64
		sponsoredObjectDelta int64
		ownerCountBelow      int
	)

	for _, e := range entries {
		before, err := decodeSponsorshipState(e.EntryType, e.Before)
		if err != nil {
			return &InvariantViolation{
				Name:    "SponsorshipOwnerCountsMatch",
				Message: fmt.Sprintf("could not parse %s SLE: %v", e.EntryType, err),
			}
		}

		afterData := e.After
		if e.IsDelete {
			afterData = e.DeleteFinal
			if afterData == nil {
				afterData = e.Before
			}
		}
		after, err := decodeSponsorshipState(e.EntryType, afterData)
		if err != nil {
			return &InvariantViolation{
				Name:    "SponsorshipOwnerCountsMatch",
				Message: fmt.Sprintf("could not parse %s SLE: %v", e.EntryType, err),
			}
		}

		if before.account != nil {
			sponsoredDelta -= int64(before.account.SponsoredOwnerCount)
			sponsoringDelta -= int64(before.account.SponsoringOwnerCount)
		}
		if after.account != nil {
			sponsoredDelta += int64(after.account.SponsoredOwnerCount)
			sponsoringDelta += int64(after.account.SponsoringOwnerCount)
			if after.account.OwnerCount < after.account.SponsoredOwnerCount {
				ownerCountBelow++
			}
		}

		sponsoredObjectDelta -= before.objectOwnerCount
		if !e.IsDelete {
			sponsoredObjectDelta += after.objectOwnerCount
		}
	}

	if sponsoredDelta != sponsoringDelta {
		return &InvariantViolation{
			Name:    "SponsorshipOwnerCountsMatch",
			Message: "SponsoredOwnerCount does not equal SponsoringOwnerCount delta.",
		}
	}
	if ownerCountBelow != 0 {
		return &InvariantViolation{
			Name:    "SponsorshipOwnerCountsMatch",
			Message: "OwnerCount must be greater than or equal to SponsoredOwnerCount.",
		}
	}
	if sponsoredObjectDelta != sponsoredDelta {
		return &InvariantViolation{
			Name:    "SponsorshipOwnerCountsMatch",
			Message: "SponsoredObjectOwnerCount does not equal SponsoredOwnerCount delta.",
		}
	}
	return nil
}

func checkSponsorshipAccountCount(entries []InvariantEntry) *InvariantViolation {
	var (
		sponsoringAccountDelta int64
		sponsorFieldDelta      int64
	)

	for _, e := range entries {
		if e.EntryType != entry.TypeAccountRoot {
			continue
		}

		before, err := decodeAccountRoot(e.Before)
		if err != nil {
			return &InvariantViolation{
				Name:    "SponsorshipAccountCountMatchesField",
				Message: fmt.Sprintf("could not parse AccountRoot SLE: %v", err),
			}
		}

		afterData := e.After
		if e.IsDelete {
			afterData = e.DeleteFinal
			if afterData == nil {
				afterData = e.Before
			}
		}
		after, err := decodeAccountRoot(afterData)
		if err != nil {
			return &InvariantViolation{
				Name:    "SponsorshipAccountCountMatchesField",
				Message: fmt.Sprintf("could not parse AccountRoot SLE: %v", err),
			}
		}

		if before != nil {
			sponsoringAccountDelta -= int64(before.account.SponsoringAccountCount)
			if before.hasSponsor {
				sponsorFieldDelta--
			}
		}
		if after != nil {
			sponsoringAccountDelta += int64(after.account.SponsoringAccountCount)
			if after.hasSponsor {
				sponsorFieldDelta++
			}
		}
	}

	if sponsoringAccountDelta != sponsorFieldDelta {
		return &InvariantViolation{
			Name:    "SponsorshipAccountCountMatchesField",
			Message: "Net delta of SponsoringAccountCount does not match net delta of sfSponsor presence.",
		}
	}
	return nil
}

// decodedAccountRoot retains the Sponsor presence bit, which is distinct from
// the decoded account ID's value.
type decodedAccountRoot struct {
	account    *state.AccountRoot
	hasSponsor bool
}

func decodeAccountRoot(data []byte) (*decodedAccountRoot, error) {
	if data == nil {
		return nil, nil
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return nil, err
	}
	return &decodedAccountRoot{account: account, hasSponsor: account.HasSponsor}, nil
}

func decodeSponsorshipState(typ entry.Type, data []byte) (sponsorshipState, error) {
	if data == nil {
		return sponsorshipState{}, nil
	}
	if typ == entry.TypeAccountRoot {
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return sponsorshipState{}, err
		}
		return sponsorshipState{account: account}, nil
	}
	if typ == entry.TypeRippleState {
		line := &entry.RippleState{}
		if err := line.Decode(data); err != nil {
			return sponsorshipState{}, err
		}
		fields := line.ToMap()
		var objectOwnerCount int64
		if _, present := fields["HighSponsor"]; present {
			objectOwnerCount++
		}
		if _, present := fields["LowSponsor"]; present {
			objectOwnerCount++
		}
		return sponsorshipState{objectOwnerCount: objectOwnerCount}, nil
	}

	decoded := entry.New(typ)
	if decoded == nil {
		return sponsorshipState{}, nil
	}
	if err := decoded.Decode(data); err != nil {
		if typ != entry.TypeSignerList || entry.DecodeLegacy(decoded, data) != nil {
			return sponsorshipState{}, err
		}
	}
	toMap, ok := decoded.(interface{ ToMap() map[string]any })
	if !ok {
		return sponsorshipState{}, fmt.Errorf("%T has no field map", decoded)
	}
	if _, sponsored := toMap.ToMap()["Sponsor"]; !sponsored {
		return sponsorshipState{}, nil
	}

	var objectOwnerCount int64 = 1
	switch value := decoded.(type) {
	case *entry.Oracle:
		if len(value.PriceDataSeries) > 5 {
			objectOwnerCount = 2
		}
	case *entry.Vault:
		objectOwnerCount = 2
	case *entry.SignerList:
		if value.Flags&entry.LsfOneOwnerCount != 0 {
			objectOwnerCount = 1
		} else {
			objectOwnerCount = int64(2 + len(value.SignerEntries))
		}
	}
	return sponsorshipState{objectOwnerCount: objectOwnerCount}, nil
}

// accountRootHasSponsorshipFields is used by deletion and pseudo-account
// checks. AccountRoot's parsed model intentionally exposes values, while this
// helper checks serialized field presence so an explicit zero is still caught.
func accountRootHasSponsorshipFields(data []byte) (bool, error) {
	if data == nil {
		return false, nil
	}
	decoded := entry.New(entry.TypeAccountRoot)
	if decoded == nil {
		return false, fmt.Errorf("AccountRoot decoder is not registered")
	}
	if err := decoded.Decode(data); err != nil {
		return false, err
	}
	fields := decoded.(interface{ ToMap() map[string]any }).ToMap()
	for _, name := range []string{
		"SponsoredOwnerCount",
		"SponsoringOwnerCount",
		"SponsoringAccountCount",
		"Sponsor",
	} {
		if _, present := fields[name]; present {
			return true, nil
		}
	}
	return false, nil
}
