package oracle_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	oracletest "github.com/LeJamon/go-xrpl/internal/testing/oracle"
)

// TestPrecedence_OracleSetTimeBeforePairs pins that the per-entry base==quote /
// scale / duplicate checks are preclaim checks ordered AFTER the
// tecINVALID_UPDATE_TIME window check, matching rippled SetOracle. A
// LastUpdateTime outside the window combined with a base==quote pair must be
// claimed (fee taken, included) as tecINVALID_UPDATE_TIME — NOT rejected at
// preflight as temMALFORMED. The tem-vs-tec class difference is a ledger-content
// fork in a mixed network, which is why the pair loop was removed from Validate.
func TestPrecedence_OracleSetTimeBeforePairs(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)
	env.Close()

	// LastUpdateTime = 0 is below the Ripple epoch, so preclaim returns
	// tecINVALID_UPDATE_TIME. BaseAsset == QuoteAsset would be a preflight
	// temMALFORMED if the pair loop still ran in Validate.
	result := env.Submit(oracletest.OracleSet(owner, 1, 0).
		ProviderHex(32).
		AssetClassHex(8).
		AddPrice("USD", "USD", 740, 1).
		Fee(10).
		Build())
	jtx.RequireTxClaimed(t, result, "tecINVALID_UPDATE_TIME")
}
