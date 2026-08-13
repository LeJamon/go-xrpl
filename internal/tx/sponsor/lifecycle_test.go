package sponsor

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func sponsorTestAccount(t *testing.T, seed byte) (string, [20]byte) {
	t.Helper()
	id := [20]byte{seed}
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatal(err)
	}
	return address, id
}

func validationResult(err error) ter.Result {
	if err == nil {
		return ter.TesSUCCESS
	}
	result, ok := ter.AsResultError(err)
	if !ok {
		return ter.TefINTERNAL
	}
	return result.Code
}

func TestSponsorshipSetPreflightMatrix(t *testing.T) {
	sponsor, _ := sponsorTestAccount(t, 1)
	sponsee, _ := sponsorTestAccount(t, 2)
	other, _ := sponsorTestAccount(t, 3)
	xrp := tx.NewXRPAmount(1)
	zero := tx.NewXRPAmount(0)
	negative := tx.NewXRPAmount(-1)
	iou := tx.NewIssuedAmountFromFloat64(1, "USD", other)

	tests := []struct {
		name string
		make func() *SponsorshipSet
		want ter.Result
	}{
		{"create", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			txn.FeeAmountDelta = &xrp
			return txn
		}, ter.TesSUCCESS},
		{"neither party", func() *SponsorshipSet { return NewSponsorshipSet(sponsor) }, ter.TemMALFORMED},
		{"both parties", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.CounterpartySponsor = other
			txn.Sponsee = sponsee
			return txn
		}, ter.TemMALFORMED},
		{"self", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsor
			return txn
		}, ter.TemMALFORMED},
		{"sponsee cannot update", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsee)
			txn.CounterpartySponsor = sponsor
			txn.FeeAmountDelta = &xrp
			return txn
		}, ter.TemMALFORMED},
		{"contradictory fee flags", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			txn.SetFlags(SponsorshipSetFlagRequireSignForFee | SponsorshipSetFlagClearRequireSignForFee)
			return txn
		}, ter.TemINVALID_FLAG},
		{"delete with budget", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			txn.FeeAmountDelta = &xrp
			txn.SetFlags(SponsorshipSetFlagDelete)
			return txn
		}, ter.TemMALFORMED},
		{"negative XRP delta", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			txn.FeeAmountDelta = &negative
			return txn
		}, ter.TesSUCCESS},
		{"zero XRP delta", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			txn.FeeAmountDelta = &zero
			return txn
		}, ter.TemBAD_AMOUNT},
		{"zero owner count delta", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			zero := int32(0)
			txn.RemainingOwnerCountDelta = &zero
			return txn
		}, ter.TemINVALID},
		{"redundant", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			return txn
		}, ter.TemREDUNDANT},
		{"flag-only update", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			txn.SetFlags(SponsorshipSetFlagRequireSignForFee)
			return txn
		}, ter.TesSUCCESS},
		{"issued amount", func() *SponsorshipSet {
			txn := NewSponsorshipSet(sponsor)
			txn.Sponsee = sponsee
			txn.MaxFee = &iou
			return txn
		}, ter.TemBAD_AMOUNT},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validationResult(test.make().Validate()); got != test.want {
				t.Fatalf("Validate = %s, want %s", got, test.want)
			}
		})
	}
}

func TestSponsorshipTransferPreflightMatrix(t *testing.T) {
	account, _ := sponsorTestAccount(t, 1)
	sponsor, _ := sponsorTestAccount(t, 2)
	objectID := "1111111111111111111111111111111111111111111111111111111111111111"
	reserve := tx.SpfSponsorReserve

	validObject := func(flag uint32) *SponsorshipTransfer {
		txn := NewSponsorshipTransfer(account)
		txn.ObjectID = objectID
		txn.SetFlags(flag)
		txn.Sponsor = sponsor
		txn.SponsorFlags = &reserve
		return txn
	}
	tests := []struct {
		name string
		make func() *SponsorshipTransfer
		want ter.Result
	}{
		{"object create", func() *SponsorshipTransfer { return validObject(SponsorshipTransferFlagCreate) }, ter.TesSUCCESS},
		{"no operation", func() *SponsorshipTransfer { return NewSponsorshipTransfer(account) }, ter.TemINVALID_FLAG},
		{"multiple operations", func() *SponsorshipTransfer {
			return validObject(SponsorshipTransferFlagCreate | SponsorshipTransferFlagEnd)
		}, ter.TemINVALID_FLAG},
		{"missing sponsor", func() *SponsorshipTransfer {
			txn := NewSponsorshipTransfer(account)
			txn.ObjectID = objectID
			txn.SetFlags(SponsorshipTransferFlagCreate)
			return txn
		}, ter.TemMALFORMED},
		{"missing reserve flag", func() *SponsorshipTransfer {
			txn := validObject(SponsorshipTransferFlagCreate)
			fee := tx.SpfSponsorFee
			txn.SponsorFlags = &fee
			return txn
		}, ter.TemINVALID_FLAG},
		{"account requires sponsor signature", func() *SponsorshipTransfer {
			txn := validObject(SponsorshipTransferFlagCreate)
			txn.ObjectID = ""
			return txn
		}, ter.TemMALFORMED},
		{"end self sponsee", func() *SponsorshipTransfer {
			txn := NewSponsorshipTransfer(account)
			txn.SetFlags(SponsorshipTransferFlagEnd)
			txn.Sponsee = account
			return txn
		}, ter.TemMALFORMED},
		{"bad object id", func() *SponsorshipTransfer {
			txn := validObject(SponsorshipTransferFlagCreate)
			txn.ObjectID = "ABC"
			return txn
		}, ter.TemMALFORMED},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validationResult(test.make().Validate()); got != test.want {
				t.Fatalf("Validate = %s, want %s", got, test.want)
			}
		})
	}
}

func TestSponsoredTargetOwnershipMatrix(t *testing.T) {
	owner, ownerID := sponsorTestAccount(t, 1)
	other, _ := sponsorTestAccount(t, 2)

	tests := []struct {
		name      string
		target    sponsoredTarget
		wantOwner bool
		wantField string
		wantCount uint32
	}{
		{"check", sponsoredTarget{entryType: entry.TypeCheck, fields: map[string]any{"Account": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"check wrong owner", sponsoredTarget{entryType: entry.TypeCheck, fields: map[string]any{"Account": other}, ownerCount: 1, sponsorField: "Sponsor"}, false, "Sponsor", 1},
		{"escrow", sponsoredTarget{entryType: entry.TypeEscrow, fields: map[string]any{"Account": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"payment channel", sponsoredTarget{entryType: entry.TypePayChannel, fields: map[string]any{"Account": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"mptoken", sponsoredTarget{entryType: entry.TypeMPToken, fields: map[string]any{"Account": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"delegate", sponsoredTarget{entryType: entry.TypeDelegate, fields: map[string]any{"Account": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"deposit preauth", sponsoredTarget{entryType: entry.TypeDepositPreauth, fields: map[string]any{"Account": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"issuance", sponsoredTarget{entryType: entry.TypeMPTokenIssuance, fields: map[string]any{"Issuer": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"accepted credential", sponsoredTarget{entryType: entry.TypeCredential, fields: map[string]any{"Flags": entry.LsfAccepted, "Subject": owner, "Issuer": other}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"unaccepted credential", sponsoredTarget{entryType: entry.TypeCredential, fields: map[string]any{"Flags": uint32(0), "Subject": other, "Issuer": owner}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"modern signer list", sponsoredTarget{key: keylet.SignerList(ownerID), entryType: entry.TypeSignerList, fields: map[string]any{"Flags": entry.LsfOneOwnerCount}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 1},
		{"legacy signer list", sponsoredTarget{key: keylet.SignerList(ownerID), entryType: entry.TypeSignerList, fields: map[string]any{"SignerEntries": []any{1, 2, 3}}, ownerCount: 1, sponsorField: "Sponsor"}, true, "Sponsor", 5},
		{"high trust line", sponsoredTarget{entryType: entry.TypeRippleState, fields: map[string]any{"Flags": entry.LsfHighReserve, "HighLimit": map[string]any{"issuer": owner}}, ownerCount: 1, sponsorField: "Sponsor"}, true, "HighSponsor", 1},
		{"low trust line", sponsoredTarget{entryType: entry.TypeRippleState, fields: map[string]any{"Flags": entry.LsfLowReserve, "LowLimit": map[string]any{"issuer": owner}}, ownerCount: 1, sponsorField: "Sponsor"}, true, "LowSponsor", 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := test.target
			if got := target.resolveOwner(ownerID, owner); got != test.wantOwner {
				t.Fatalf("resolveOwner = %t, want %t", got, test.wantOwner)
			}
			if target.sponsorField != test.wantField {
				t.Fatalf("sponsor field = %q, want %q", target.sponsorField, test.wantField)
			}
			if target.ownerCount != test.wantCount {
				t.Fatalf("owner count = %d, want %d", target.ownerCount, test.wantCount)
			}
		})
	}
}

func TestSponsorReserveUsesEffectiveCounts(t *testing.T) {
	account := &state.AccountRoot{
		OwnerCount:             10,
		SponsoredOwnerCount:    3,
		SponsoringOwnerCount:   2,
		SponsoringAccountCount: 1,
		HasSponsor:             true,
	}
	config := tx.EngineConfig{ReserveBase: 200, ReserveIncrement: 50}
	reserve, ok := reserveRequired(config, account, 1, 1)
	if !ok {
		t.Fatal("reserveRequired rejected valid counts")
	}
	// owner count: 10 - 3 + 2 + 1 = 10; account count: 0 + 1 + 1 = 2.
	if reserve != 900 {
		t.Fatalf("reserve = %d, want 900", reserve)
	}

	account.SponsoredOwnerCount = 11
	if _, ok := reserveRequired(config, account, 0, 0); ok {
		t.Fatal("reserveRequired accepted SponsoredOwnerCount > OwnerCount")
	}

	account.OwnerCount = 10
	account.SponsoredOwnerCount = 0
	account.SponsoringOwnerCount = math.MaxUint32
	effective, ok := effectiveOwnerCount(account, 0)
	if !ok {
		t.Fatal("effectiveOwnerCount rejected valid boundary counts")
	}
	if want := uint32(math.MaxInt32) + account.OwnerCount; effective != want {
		t.Fatalf("effective owner count = %d, want %d", effective, want)
	}
}
