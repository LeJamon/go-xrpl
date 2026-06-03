package oracle_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	oracletest "github.com/LeJamon/go-xrpl/internal/testing/oracle"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// fixIncludeKeyletFields stores sfOracleDocumentID (the value used in
// keylet::oracle) on the Oracle SLE. Reference: rippled SetOracle.cpp:282-286.
func TestOracleSet_IncludeKeyletFields_DocumentID(t *testing.T) {
	const docID = uint32(7)

	create := func(env *jtx.TestEnv) *state.OracleData {
		owner := jtx.NewAccount("owner")
		env.FundAmount(owner, env.ReserveBase()+env.ReserveIncrement()+2*baseFee)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, docID, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		data, err := env.LedgerEntry(keylet.Oracle(owner.ID, docID))
		require.NoError(t, err)
		o, err := state.ParseOracle(data)
		require.NoError(t, err)
		return o
	}

	t.Run("enabled stores sfOracleDocumentID", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		o := create(env)
		require.True(t, o.HasOracleDocumentID, "OracleDocumentID must be stored when fixIncludeKeyletFields is enabled")
		require.Equal(t, docID, o.OracleDocumentID)
	})

	t.Run("disabled omits sfOracleDocumentID", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixIncludeKeyletFields")
		o := create(env)
		require.False(t, o.HasOracleDocumentID, "OracleDocumentID must be absent without fixIncludeKeyletFields")
	})
}
