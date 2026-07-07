// fixIncludeKeyletFields: a created Oracle records sfOracleDocumentID (a keylet
// input) once the amendment is active, and omits it otherwise. A zero document id
// is valid and must still be stored (presence, not value, gates emission).
// Reference: rippled SetOracle.cpp doApply() and Oracle_test.cpp testCreate
// (fixIncludeKeyletFields arm).
package oracle_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	oracletest "github.com/LeJamon/go-xrpl/internal/testing/oracle"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// oracleSLEFields decodes the raw Oracle SLE into a field map.
func oracleSLEFields(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, docID uint32) map[string]any {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Oracle(owner.ID, docID))
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	return fields
}

// With the amendment enabled (test env default), a created oracle stores the
// document id in sfOracleDocumentID.
func TestOracleSet_IncludeKeyletFields_StoresDocumentID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)
	env.Close()

	const docID = uint32(7)
	jtx.RequireTxSuccess(t, env.Submit(oracletest.OracleSet(owner, docID, oracletest.DefaultLastUpdateTime(env)).
		ProviderHex(32).AssetClassHex(8).AddPrice("XRP", "USD", 740, 1).Build()))

	fields := oracleSLEFields(t, env, owner, docID)
	require.Equal(t, docID, fields["OracleDocumentID"], "oracle must store the document id in sfOracleDocumentID")
}

// A zero document id is a valid keylet input and must be stored — emission is
// gated on presence, not on a non-zero value.
func TestOracleSet_IncludeKeyletFields_StoresZeroDocumentID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)
	env.Close()

	const docID = uint32(0)
	jtx.RequireTxSuccess(t, env.Submit(oracletest.OracleSet(owner, docID, oracletest.DefaultLastUpdateTime(env)).
		ProviderHex(32).AssetClassHex(8).AddPrice("XRP", "USD", 740, 1).Build()))

	fields := oracleSLEFields(t, env, owner, docID)
	v, present := fields["OracleDocumentID"]
	require.True(t, present, "a zero document id must still be stored in sfOracleDocumentID")
	require.Equal(t, docID, v)
}

// An update to an oracle created under the amendment preserves sfOracleDocumentID
// across the parse→serialize round-trip.
func TestOracleSet_IncludeKeyletFields_UpdatePreservesDocumentID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)
	env.Close()

	const docID = uint32(7)
	lut := oracletest.DefaultLastUpdateTime(env)
	jtx.RequireTxSuccess(t, env.Submit(oracletest.OracleSet(owner, docID, lut).
		ProviderHex(32).AssetClassHex(8).AddPrice("XRP", "USD", 740, 1).Build()))
	env.Close()

	// Update the same pair's price; sfOracleDocumentID must survive the write-back.
	jtx.RequireTxSuccess(t, env.Submit(oracletest.OracleSet(owner, docID, lut+1).
		AddPrice("XRP", "USD", 750, 1).Build()))

	fields := oracleSLEFields(t, env, owner, docID)
	require.Equal(t, docID, fields["OracleDocumentID"], "update must preserve sfOracleDocumentID")
}

// With the amendment disabled, sfOracleDocumentID is absent from the oracle SLE.
func TestOracleSet_NoIncludeKeyletFields_OmitsDocumentID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)
	env.DisableFeature("fixIncludeKeyletFields")
	env.Close()

	const docID = uint32(7)
	jtx.RequireTxSuccess(t, env.Submit(oracletest.OracleSet(owner, docID, oracletest.DefaultLastUpdateTime(env)).
		ProviderHex(32).AssetClassHex(8).AddPrice("XRP", "USD", 740, 1).Build()))

	fields := oracleSLEFields(t, env, owner, docID)
	_, present := fields["OracleDocumentID"]
	require.False(t, present, "oracle must not store sfOracleDocumentID when the amendment is off")
}
