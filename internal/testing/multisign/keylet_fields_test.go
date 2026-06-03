package multisign_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// rippled records a SignerList owner only as sfOwner under fixIncludeKeyletFields,
// and never as a top-level sfAccount (the owner is encoded in keylet::signers).
// Reference: rippled SetSignerList.cpp:428-431.
func TestSignerListSet_IncludeKeyletFields_Owner(t *testing.T) {
	build := func(env *jtx.TestEnv) (string, map[string]any) {
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.Fund(alice, bob)
		env.Close()

		env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: bob, Weight: 1}})
		env.Close()

		data, err := env.LedgerEntry(keylet.SignerList(alice.ID))
		require.NoError(t, err)
		decoded, err := binarycodec.Decode(hex.EncodeToString(data))
		require.NoError(t, err)
		return alice.Address, decoded
	}

	t.Run("enabled stores sfOwner, never sfAccount", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		addr, decoded := build(env)
		_, hasAccount := decoded["Account"]
		require.False(t, hasAccount, "SignerList must never carry a top-level sfAccount")
		owner, ok := decoded["Owner"]
		require.True(t, ok, "Owner must be stored when fixIncludeKeyletFields is enabled")
		require.Equal(t, addr, owner)
		require.Contains(t, decoded, "SignerListID", "sfSignerListID is soeREQUIRED")
		require.EqualValues(t, 0, decoded["SignerListID"])
	})

	t.Run("disabled stores neither sfOwner nor sfAccount", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixIncludeKeyletFields")
		_, decoded := build(env)
		_, hasAccount := decoded["Account"]
		require.False(t, hasAccount, "SignerList must never carry a top-level sfAccount")
		_, hasOwner := decoded["Owner"]
		require.False(t, hasOwner, "Owner must be absent without fixIncludeKeyletFields")
		require.Contains(t, decoded, "SignerListID", "sfSignerListID is soeREQUIRED")
		require.EqualValues(t, 0, decoded["SignerListID"])
	})
}
