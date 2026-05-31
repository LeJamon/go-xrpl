// Tests for the fixTrustLinesToSelf activation side-effect of the
// EnableAmendment pseudo-tx.
// Reference: rippled/src/xrpld/app/tx/detail/Change.cpp activateTrustLinesToSelfFix()
package pseudotx_test

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// The two RippleState ledger keys fixTrustLinesToSelf deletes on mainnet.
const (
	selfTrustLineKey1 = "2F8F21EFCAFD7ACFB07D5BB04F0D2E18587820C7611305BB674A64EAB0FA71E1"
	selfTrustLineKey2 = "326035D5C0560A9DA8636545DD5A1B0DFCFF63E68D491B5522B767BB00564B1A"
)

func mustKeylet(t *testing.T, hexKey string) keylet.Keylet {
	t.Helper()
	b, err := hex.DecodeString(hexKey)
	require.NoError(t, err)
	require.Len(t, b, 32)
	var k [32]byte
	copy(k[:], b)
	return keylet.Keylet{Key: k}
}

func ownerCount(t *testing.T, env *jtx.TestEnv, id [20]byte) uint32 {
	t.Helper()
	data, err := env.Ledger().Read(keylet.Account(id))
	require.NoError(t, err)
	require.NotNil(t, data)
	ar, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	return ar.OwnerCount
}

func setOwnerCount(t *testing.T, env *jtx.TestEnv, id [20]byte, n uint32) {
	t.Helper()
	data, err := env.Ledger().Read(keylet.Account(id))
	require.NoError(t, err)
	ar, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	ar.OwnerCount = n
	out, err := state.SerializeAccountRoot(ar)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(keylet.Account(id), out))
}

// plantTrustLine writes a RippleState at an arbitrary ledger key whose limits
// are issued by lowIssuer/highIssuer (classic addresses).
func plantTrustLine(t *testing.T, env *jtx.TestEnv, key keylet.Keylet, lowIssuer, highIssuer string, flags uint32, lowNode, highNode uint64) {
	t.Helper()
	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, 0, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, 0, "USD", lowIssuer),
		HighLimit: state.NewIssuedAmountFromValue(0, 0, "USD", highIssuer),
		Flags:     flags,
		LowNode:   lowNode,
		HighNode:  highNode,
	}
	data, err := state.SerializeRippleState(rs)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Insert(key, data))
}

// TestEnableAmendment_FixTrustLinesToSelf_Removes verifies that enabling
// fixTrustLinesToSelf deletes a trust line to self at the known key, removes it
// from the owner directory, and releases both reserves.
// Reference: rippled Change.cpp activateTrustLinesToSelfFix lines 166-246.
func TestEnableAmendment_FixTrustLinesToSelf_Removes(t *testing.T) {
	env := newAmendmentTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	key := mustKeylet(t, selfTrustLineKey1)
	ownerDir := keylet.OwnerDir(alice.ID)
	res, err := state.DirInsert(env.Ledger(), ownerDir, key.Key, false, nil)
	require.NoError(t, err)

	plantTrustLine(t, env, key, alice.Address, alice.Address,
		state.LsfLowReserve|state.LsfHighReserve, res.Page, res.Page)

	base := ownerCount(t, env, alice.ID)
	setOwnerCount(t, env, alice.ID, base+2)

	result := env.SubmitPseudo(newEnableAmendment(makeAmendmentHash("fixTrustLinesToSelf"), 0))
	jtx.RequireTxSuccess(t, result)

	require.False(t, env.LedgerEntryExists(key), "self trust line should be erased")
	require.Equal(t, base, ownerCount(t, env, alice.ID), "both reserves should be released")
	require.False(t, env.LedgerEntryExists(ownerDir), "emptied owner dir root should be deleted")
}

// TestEnableAmendment_FixTrustLinesToSelf_SkipsNonSelf verifies that a normal
// trust line (low account != high account) parked at the known key is NOT
// touched. Mirrors rippled's `lo != hi` guard returning success without delete.
func TestEnableAmendment_FixTrustLinesToSelf_SkipsNonSelf(t *testing.T) {
	env := newAmendmentTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)

	key := mustKeylet(t, selfTrustLineKey2)
	plantTrustLine(t, env, key, alice.Address, bob.Address, 0, 0, 0)

	result := env.SubmitPseudo(newEnableAmendment(makeAmendmentHash("fixTrustLinesToSelf"), 0))
	jtx.RequireTxSuccess(t, result)

	require.True(t, env.LedgerEntryExists(key), "non-self trust line must survive")
}

// TestEnableAmendment_FixTrustLinesToSelf_OnlyOnThatAmendment verifies the
// migration runs only for fixTrustLinesToSelf, not for other amendments.
func TestEnableAmendment_FixTrustLinesToSelf_OnlyOnThatAmendment(t *testing.T) {
	env := newAmendmentTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)

	key := mustKeylet(t, selfTrustLineKey1)
	plantTrustLine(t, env, key, alice.Address, alice.Address, 0, 0, 0)

	result := env.SubmitPseudo(newEnableAmendment(makeAmendmentHash("fixNFTokenPageLinks"), 0))
	jtx.RequireTxSuccess(t, result)

	require.True(t, env.LedgerEntryExists(key), "unrelated amendment must not run the migration")
}

// TestEnableAmendment_FixTrustLinesToSelf_NoEntries verifies enabling the
// amendment with neither known key present still succeeds (best-effort).
func TestEnableAmendment_FixTrustLinesToSelf_NoEntries(t *testing.T) {
	env := newAmendmentTestEnv(t)

	result := env.SubmitPseudo(newEnableAmendment(makeAmendmentHash("fixTrustLinesToSelf"), 0))
	jtx.RequireTxSuccess(t, result)
}
