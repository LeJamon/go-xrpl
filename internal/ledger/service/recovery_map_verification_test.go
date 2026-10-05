package service

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestRecoveryMapVerificationResumesTinyBudgetAndRejectsUnavailableNodes(t *testing.T) {
	for _, failure := range []string{"none", "missing", "corrupt", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			family := newVerifierFamily()
			t.Cleanup(func() { require.NoError(t, family.Close()) })
			source := newPersistedVerifierMap(t, family, shamap.TypeState, 96)
			child := verifierRootChild(t, family, source)
			root, err := source.Hash()
			require.NoError(t, err)
			cold, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, root, durableAdmissionFamily{Family: family, durable: family})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "missing":
				family.hide(child)
			case "corrupt":
				family.corruptNode(child)
			case "canceled":
				cancel()
			}
			err = verifyRecoveryMap(ctx, "state", cold, 3)
			switch failure {
			case "none":
				require.NoError(t, err)
				require.Greater(t, cold.BackedWalkStats().NodesDescended, uint64(3))
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
			default:
				require.Error(t, err)
			}
		})
	}
}
