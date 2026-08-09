package entry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func sponsorshipMetadataFixture() *Sponsorship {
	entry := &Sponsorship{}
	entry.SetPreviousTxnID(fxHash256)
	entry.SetPreviousTxnLgrSeq(9)
	entry.SetOwner(fxAccount)
	entry.SetSponsee(fxIssuer)
	entry.SetFeeAmount("1000")
	entry.SetMaxFee("100")
	entry.SetRemainingOwnerCount(2)
	entry.SetOwnerNode("1")
	entry.SetSponseeNode("2")
	entry.SetFlags(0x00030000)
	entry.SetSponsor(fxAccount)
	return entry
}

func TestSponsorshipMetadataFields(t *testing.T) {
	entry := sponsorshipMetadataFixture()
	wantFields := map[string]any{
		"Owner":               fxAccount,
		"Sponsee":             fxIssuer,
		"FeeAmount":           "1000",
		"MaxFee":              "100",
		"RemainingOwnerCount": uint32(2),
		"OwnerNode":           "1",
		"SponseeNode":         "2",
		"Flags":               uint32(0x00030000),
		"Sponsor":             fxAccount,
	}

	for _, test := range []struct {
		name string
		emit func(map[string]any)
		want map[string]any
	}{
		{name: "new", emit: entry.EmitNewFields, want: wantFields},
		{name: "final", emit: entry.EmitFinalFields, want: wantFields},
		{
			name: "delete final",
			emit: entry.EmitDeleteFinalFields,
			want: func() map[string]any {
				fields := make(map[string]any, len(wantFields)+2)
				for name, value := range wantFields {
					fields[name] = value
				}
				fields["PreviousTxnID"] = fxHash256
				fields["PreviousTxnLgrSeq"] = uint32(9)
				return fields
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := make(map[string]any)
			test.emit(got)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("metadata = %#v, want %#v", got, test.want)
			}
		})
	}

	current := *entry
	current.SetFeeAmount("900")
	current.SetSponsor(fxIssuer)
	previous := make(map[string]any)
	current.EmitPreviousFields(entry, previous)
	wantPrevious := map[string]any{"FeeAmount": "1000", "Sponsor": fxAccount}
	if !reflect.DeepEqual(previous, wantPrevious) {
		t.Fatalf("PreviousFields = %#v, want %#v", previous, wantPrevious)
	}
}

func TestSponsorshipSchemaRejectsMissingRequiredAndExplicitDefault(t *testing.T) {
	if _, err := (&Sponsorship{}).Encode(); err == nil || !strings.Contains(err.Error(), "required field Owner") {
		t.Fatalf("empty Sponsorship Encode error = %v, want missing Owner", err)
	}

	fields := sponsorshipMetadataFixture().ToMap()
	fields["RemainingOwnerCount"] = uint32(0)
	raw, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("EncodeBytes explicit default: %v", err)
	}
	var decoded Sponsorship
	if err := decoded.Decode(raw); err == nil || !strings.Contains(err.Error(), "default field RemainingOwnerCount is explicitly set") {
		t.Fatalf("Decode explicit default error = %v", err)
	}
}

func TestSponsorAccountAndTrustLineFieldsEmitPreviousMetadata(t *testing.T) {
	accountBefore := &AccountRoot{}
	accountBefore.SetSponsoredOwnerCount(1)
	accountAfter := *accountBefore
	accountAfter.SetSponsoredOwnerCount(2)
	accountPrevious := make(map[string]any)
	accountAfter.EmitPreviousFields(accountBefore, accountPrevious)
	if want := map[string]any{"SponsoredOwnerCount": uint32(1)}; !reflect.DeepEqual(accountPrevious, want) {
		t.Fatalf("AccountRoot PreviousFields = %#v, want %#v", accountPrevious, want)
	}

	lineBefore := &RippleState{}
	lineBefore.SetHighSponsor(fxAccount)
	lineAfter := *lineBefore
	lineAfter.SetHighSponsor(fxIssuer)
	linePrevious := make(map[string]any)
	lineAfter.EmitPreviousFields(lineBefore, linePrevious)
	if want := map[string]any{"HighSponsor": fxAccount}; !reflect.DeepEqual(linePrevious, want) {
		t.Fatalf("RippleState PreviousFields = %#v, want %#v", linePrevious, want)
	}
}
