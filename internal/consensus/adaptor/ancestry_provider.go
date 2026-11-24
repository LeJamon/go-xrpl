package adaptor

import (
	"context"

	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
)

func newAncestryProvider(svc ledgerLookupContext) *rcl.AncestryProvider {
	if svc == nil {
		return rcl.NewAncestryProvider(nil)
	}
	return rcl.NewAncestryProvider(func(ctx context.Context, hash [32]byte) (rcl.LedgerHeader, error) {
		l, err := svc.GetLedgerByHashContext(ctx, hash)
		if err != nil {
			return nil, err
		}
		return l, nil
	})
}
