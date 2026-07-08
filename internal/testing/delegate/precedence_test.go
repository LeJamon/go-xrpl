// Preflight TER-precedence pins for DelegateSet, covering the flags mask
// (preflight0), the unknown-granular delegatability rule (preflight body), and
// the round-trip of an unregistered sfPermissionValue.
//
// Reference: rippled DelegateSet.cpp / Permissions.cpp isDelegable().
package delegate_test

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/stretchr/testify/require"
)

// TestDelegateSet_FlagsMaskPrecedence pins DelegateSet finding-1: DelegateSet
// has no getFlagsMask override, so any non-universal flag bit is temINVALID_FLAG
// at preflight0 — before the (would-be-malformed) self-authorize check and every
// ledger-state TER.
func TestDelegateSet_FlagsMaskPrecedence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	ds := delegatetx.NewDelegateSet(gw.Address)
	ds.Authorize = alice.Address
	ds.Permissions = []delegatetx.Permission{
		{Permission: delegatetx.PermissionData{PermissionValue: "Payment"}},
	}
	ds.GetCommon().SetFlags(0x00000001) // non-universal bit
	jtx.RequireTxFail(t, env.SubmitSignedWith(ds, gw), "temINVALID_FLAG")
}

// TestDelegateSet_UnknownGranularNotDelegatable pins DelegateSet finding-3: a
// value in the granular range (>= 65536) that is NOT a registered granular
// permission is not delegatable. The check runs in preflight and rejects with
// temMALFORMED (rippled isDelegable falls through getGranularName to the
// tx-type path).
func TestDelegateSet_UnknownGranularNotDelegatable(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	ds := delegatetx.NewDelegateSet(gw.Address)
	ds.Authorize = alice.Address
	ds.Permissions = []delegatetx.Permission{
		{Permission: delegatetx.PermissionData{PermissionValue: "70000"}}, // unknown, >= 65536
	}
	jtx.RequireTxFail(t, env.SubmitSignedWith(ds, gw), "temMALFORMED")
}

// TestDelegateSet_UnknownPermissionValueRoundTrips pins DelegateSet finding-2:
// sfPermissionValue is a plain UINT32, so a value with no registered name must
// still decode. A binary blob carrying value 60000 round-trips through the
// string-typed field as its decimal form rather than failing to parse.
func TestDelegateSet_UnknownPermissionValueRoundTrips(t *testing.T) {
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")

	ds := delegatetx.NewDelegateSet(gw.Address)
	ds.Authorize = alice.Address
	ds.Fee = "10"
	seq := uint32(1)
	ds.GetCommon().Sequence = &seq
	ds.GetCommon().SigningPubKey = ""
	ds.Permissions = []delegatetx.Permission{
		{Permission: delegatetx.PermissionData{PermissionValue: "60000"}},
	}

	flat, err := ds.Flatten()
	require.NoError(t, err)
	blobHex, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(blobHex)
	require.NoError(t, err)

	parsed, err := txcore.ParseFromBinary(blob)
	require.NoError(t, err, "unknown sfPermissionValue must still parse")

	back, ok := parsed.(*delegatetx.DelegateSet)
	require.True(t, ok)
	require.Len(t, back.Permissions, 1)
	require.Equal(t, "60000", back.Permissions[0].Permission.PermissionValue)
}
