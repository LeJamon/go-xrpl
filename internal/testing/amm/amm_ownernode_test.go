// Regression test for the sfOwnerNode directory bug class (see issue #729).
// AMMCreate must link the AMM object into the AMM pseudo-account's owner
// directory and record the page in sfOwnerNode (rippled dirLink). Previously
// goXRPL skipped the directory insert entirely: the AMM object was never linked
// into the owner directory and sfOwnerNode stayed unset, diverging account_hash
// from rippled on every AMMCreate.
// Reference: rippled AMMCreate.cpp:270 (dirLink) → View.cpp:1056-1064.
package amm_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAMMCreate_LinksToOwnerDirectory(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, ammAcc *jtx.Account) {
		// The AMM pseudo-account's owner directory must record sfOwner and link
		// the AMM object (the directory insert was missing entirely before).
		dirData, err := env.LedgerEntry(keylet.OwnerDir(ammAcc.ID))
		require.NoError(t, err)
		require.NotEmpty(t, dirData, "AMM account owner directory must exist")

		dir, err := binarycodec.Decode(hex.EncodeToString(dirData))
		require.NoError(t, err)
		require.Equal(t, ammAcc.Address, dir["Owner"], "owner directory must record sfOwner")

		var indexes []string
		switch v := dir["Indexes"].(type) {
		case []string:
			indexes = v
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					indexes = append(indexes, s)
				}
			}
		}
		require.NotEmpty(t, indexes, "owner dir must list entries")

		ammEntries := 0
		for _, s := range indexes {
			raw, err := hex.DecodeString(s)
			require.NoError(t, err)
			var k [32]byte
			copy(k[:], raw)
			obj, err := env.LedgerEntry(keylet.Keylet{Key: k})
			require.NoError(t, err)
			fields, err := binarycodec.Decode(hex.EncodeToString(obj))
			require.NoError(t, err)
			if fields["LedgerEntryType"] != "AMM" {
				continue
			}
			ammEntries++
			require.Equal(t, ammAcc.Address, fields["Account"])
			require.Equal(t, "0", fields["OwnerNode"], "AMM records owner-dir page 0")
		}
		require.Equal(t, 1, ammEntries, "AMM object must be linked into the AMM account's owner directory exactly once")
	})
}
