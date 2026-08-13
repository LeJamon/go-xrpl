package sponsor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestRegisterSponsorTransactions(t *testing.T) {
	Register()
	for _, test := range []struct {
		txType tx.Type
		want   any
	}{
		{tx.TypeSponsorshipSet, (*SponsorshipSet)(nil)},
		{tx.TypeSponsorshipTransfer, (*SponsorshipTransfer)(nil)},
	} {
		got, err := tx.NewFromType(test.txType)
		if err != nil {
			t.Fatalf("NewFromType(%s): %v", test.txType, err)
		}
		switch test.want.(type) {
		case *SponsorshipSet:
			if _, ok := got.(*SponsorshipSet); !ok {
				t.Fatalf("NewFromType(%s) = %T, want *SponsorshipSet", test.txType, got)
			}
		case *SponsorshipTransfer:
			if _, ok := got.(*SponsorshipTransfer); !ok {
				t.Fatalf("NewFromType(%s) = %T, want *SponsorshipTransfer", test.txType, got)
			}
		}
	}
}
