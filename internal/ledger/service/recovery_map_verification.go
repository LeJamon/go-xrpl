package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/shamap"
)

const recoveryVerificationVisits = 4096

// Keep the same cold map across bounded walks so a budget yield retains its
// cursor without trusting completeness proofs from the abandoned frontier.
func verifyRecoveryMap(ctx context.Context, name string, snapshot *shamap.SHAMap, visits int64) error {
	if err := snapshot.StartSync(); err != nil {
		return fmt.Errorf("start recovery %s completeness check: %w", name, err)
	}
	for {
		missing, err := snapshot.GetMissingNodesContext(shamap.WithTraversalBudget(ctx, visits), 1, nil)
		if len(missing) != 0 {
			return fmt.Errorf("incomplete recovery %s tree at depth %d: %w", name, missing[0].Depth, &shamap.MissingNodeError{Hash: missing[0].Hash})
		}
		if errors.Is(err, shamap.ErrTraversalBudget) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("walk recovery %s tree: %w", name, err)
		}
		break
	}
	for {
		err := snapshot.FinishSyncContext(shamap.WithTraversalBudget(ctx, visits))
		if errors.Is(err, shamap.ErrTraversalBudget) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("finish recovery %s completeness check: %w", name, err)
		}
		return nil
	}
}
