// Preflight TER-precedence pins for DelegateSet, covering the flags mask
// (preflight0), the unknown-granular delegatability rule (preflight body), and
// the round-trip of an unregistered sfPermissionValue.
//
// Reference: rippled DelegateSet.cpp / Permissions.cpp isDelegable().
package delegate_test

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

// TestDelegateSet_FlagsMaskPrecedence verifies that any non-universal flag bit is temINVALID_FLAG
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
	ds.Permissions = []delegatetx.Permission{delegatetx.NewPermission("Payment")}
	ds.GetCommon().SetFlags(0x00000001) // non-universal bit
	jtx.RequireTxFail(t, env.SubmitSignedWith(ds, gw), "temINVALID_FLAG")
}

func TestDelegateSet_EmptyPermissionsPresenceRoundTrips(t *testing.T) {
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")

	ds := delegatetx.NewDelegateSet(gw.Address)
	ds.Authorize = alice.Address
	ds.Fee = "10"
	seq := uint32(1)
	ds.Sequence = &seq
	ds.SigningPubKey = ""

	jsonBytes, err := txcore.ToJSON(ds)
	require.NoError(t, err)
	var jsonMap map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &jsonMap))
	permissions, present := jsonMap["Permissions"]
	require.True(t, present)
	require.Empty(t, permissions)

	blob, err := txcore.SerializeTransaction(ds)
	require.NoError(t, err)
	parsed, err := txcore.ParseFromBinary(blob)
	require.NoError(t, err)
	roundTripped := parsed.(*delegatetx.DelegateSet)
	require.NotNil(t, roundTripped.Permissions)
	require.Empty(t, roundTripped.Permissions)
}

func TestDelegateSet_MissingPermissionsIsMalformed(t *testing.T) {
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	raw := []byte(`{"Account":"` + gw.Address + `","Authorize":"` + alice.Address + `","TransactionType":"DelegateSet"}`)

	parsed, err := txcore.FromJSON(raw)
	require.NoError(t, err)
	ds := parsed.(*delegatetx.DelegateSet)
	require.Nil(t, ds.Permissions)

	err = ds.Validate()
	require.Error(t, err)
	resultErr, ok := ter.AsResultError(err)
	require.True(t, ok)
	require.Equal(t, ter.TemMALFORMED, resultErr.Code)

	ds.Fee = "10"
	sequence := uint32(1)
	ds.Sequence = &sequence
	blob, err := txcore.SerializeTransaction(ds)
	require.NoError(t, err)
	_, err = txcore.ParseFromBinary(blob)
	require.ErrorContains(t, err, "Permissions")
}

func TestDelegateSet_DuplicateResolvedPermissionIsMalformed(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
	}{
		{name: "transaction alias", permissions: []string{"Payment", "1"}},
		{name: "granular alias", permissions: []string{"TrustlineAuthorize", "65537"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := delegatetx.NewDelegateSet(jtx.NewAccount("gw").Address)
			ds.Authorize = jtx.NewAccount("alice").Address
			for _, permission := range tt.permissions {
				ds.Permissions = append(ds.Permissions, delegatetx.NewPermission(permission))
			}

			err := ds.Validate()
			require.Error(t, err)
			resultErr, ok := ter.AsResultError(err)
			require.True(t, ok)
			require.Equal(t, ter.TemMALFORMED, resultErr.Code)
		})
	}
}

// TestDelegateSet_UnknownGranularNotDelegatable verifies that a value in the
// granular range (>= 65536) that is not a registered granular
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
	ds.Permissions = []delegatetx.Permission{delegatetx.NewPermission("70000")}
	jtx.RequireTxFail(t, env.SubmitSignedWith(ds, gw), "temMALFORMED")
}

// TestDelegateSet_UnknownPermissionValueRoundTrips verifies that a permission
// value with no registered name must
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
	ds.Permissions = []delegatetx.Permission{delegatetx.NewPermission("60000")}

	flat, err := ds.Flatten()
	require.NoError(t, err)
	txcore.PopulateRequiredWireFields(flat, ds.GetCommon())
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
