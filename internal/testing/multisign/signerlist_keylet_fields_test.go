// fixIncludeKeyletFields: a created SignerList records sfOwner (the account the
// list keylet is derived from) once the amendment is active, and omits it
// otherwise. Reference: rippled SetSignerList.cpp writeSignersToSLE() and
// MultiSign_test.cpp testSignerListSet (fixIncludeKeyletFields arm).
package multisign_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// signerListSLEFields decodes the raw SignerList SLE into a field map.
func signerListSLEFields(t *testing.T, env *jtx.TestEnv, owner *jtx.Account) map[string]any {
	t.Helper()
	data, err := env.LedgerEntry(keylet.SignerList(owner.ID))
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	return fields
}

// With the amendment enabled (test env default), the created signer list stores
// the owner account in sfOwner.
func TestSignerListSet_IncludeKeyletFields_StoresOwner(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bogie := jtx.NewAccount("bogie")
	env.Fund(alice)
	env.Fund(bogie)
	env.Close()

	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bogie, Weight: 1}})
	env.Close()

	fields := signerListSLEFields(t, env, alice)
	require.Equal(t, alice.Address, fields["Owner"], "signer list must store the owner account in sfOwner")
}

// With the amendment disabled, sfOwner is absent from the signer list SLE.
func TestSignerListSet_NoIncludeKeyletFields_OmitsOwner(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bogie := jtx.NewAccount("bogie")
	env.Fund(alice)
	env.Fund(bogie)
	env.DisableFeature("fixIncludeKeyletFields")
	env.Close()

	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bogie, Weight: 1}})
	env.Close()

	fields := signerListSLEFields(t, env, alice)
	_, present := fields["Owner"]
	require.False(t, present, "signer list must not store sfOwner when the amendment is off")
}
