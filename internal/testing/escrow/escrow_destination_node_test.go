// Regression tests for issue #729: EscrowCreate must record sfDestinationNode
// on the Escrow ledger object for cross-account escrows, and sfOwnerNode must
// reflect the actual owner-directory page (not a hardcoded 0). Omitting
// sfDestinationNode changes the Escrow SLE serialization, diverging account_hash
// from rippled while transaction_hash still matches — a silent consensus fork.
// Reference: rippled src/xrpld/app/tx/detail/Escrow.cpp:548-584.
package escrow_test

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func parseEscrowSLE(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, seq uint32) *state.EscrowData {
	t.Helper()
	key := keylet.Escrow(owner.ID, seq)
	require.True(t, env.LedgerEntryExists(key), "escrow entry should exist")
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	esc, err := state.ParseEscrow(data)
	require.NoError(t, err)
	return esc
}

// Cross-account escrow records sfDestinationNode (page 0 for a fresh dir).
func TestEscrowCreate_SetsDestinationNode_CrossAccount(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	seq := env.Seq(alice)
	r := env.Submit(escrow.EscrowCreate(alice, bob, xrp(1000)).
		FinishTime(env.Now().Add(1 * time.Second)).Build())
	jtx.RequireTxSuccess(t, r)

	esc := parseEscrowSLE(t, env, alice, seq)
	require.True(t, esc.HasDestNode, "cross-account escrow must carry sfDestinationNode")
	require.Equal(t, uint64(0), esc.DestinationNode, "fresh destination dir → page 0")
	require.Equal(t, uint64(0), esc.OwnerNode, "fresh owner dir → page 0")
}

// Self-escrow (destination == account) must NOT carry sfDestinationNode,
// mirroring rippled's `if (dest != account_)` guard.
func TestEscrowCreate_NoDestinationNode_SelfEscrow(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	fund5000(env, alice)
	env.Close()

	seq := env.Seq(alice)
	r := env.Submit(escrow.EscrowCreate(alice, alice, xrp(1000)).
		FinishTime(env.Now().Add(1 * time.Second)).Build())
	jtx.RequireTxSuccess(t, r)

	esc := parseEscrowSLE(t, env, alice, seq)
	require.False(t, esc.HasDestNode, "self-escrow must not carry sfDestinationNode")
}

// When the destination's owner directory paginates, sfDestinationNode must hold
// the actual page index (proving it is captured from DirInsert, not hardcoded).
func TestEscrowCreate_DestinationNode_Pagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(xrp(100000)))
	env.FundAmount(bob, uint64(xrp(100000)))
	env.Close()

	// A directory page holds 32 entries; the 33rd escrow lands on page 1 of
	// both alice's and bob's owner directories.
	const n = 33
	var lastSeq uint32
	for range n {
		lastSeq = env.Seq(alice)
		r := env.Submit(escrow.EscrowCreate(alice, bob, xrp(1)).
			FinishTime(env.Now().Add(1 * time.Second)).Build())
		jtx.RequireTxSuccess(t, r)
		env.Close()
	}

	esc := parseEscrowSLE(t, env, alice, lastSeq)
	require.True(t, esc.HasDestNode)
	require.Equal(t, uint64(1), esc.DestinationNode, "33rd escrow → destination dir page 1")
	require.Equal(t, uint64(1), esc.OwnerNode, "33rd escrow → owner dir page 1")
}

// A cross-account escrow created with sfDestinationNode must finish cleanly:
// EscrowFinish reads sfDestinationNode to remove the escrow from the
// destination's owner directory.
func TestEscrowCreate_CrossAccount_FinishesCleanly(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	seq := env.Seq(alice)
	r := env.Submit(escrow.EscrowCreate(alice, bob, xrp(1000)).
		FinishTime(env.Now().Add(1 * time.Second)).Build())
	jtx.RequireTxSuccess(t, r)
	require.True(t, parseEscrowSLE(t, env, alice, seq).HasDestNode)
	env.Close()

	// Advance past FinishAfter.
	env.Close()
	fin := env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
	jtx.RequireTxSuccess(t, fin)
	require.False(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)),
		"escrow object should be removed after finish")
}

// An IOU escrow with a third-party issuer records sfIssuerNode (the page in the
// issuer's owner directory) alongside sfOwnerNode and sfDestinationNode. Omitting
// it diverges account_hash exactly as the missing sfDestinationNode did.
// Reference: rippled Escrow.cpp:576-583 (issuer != account_ && issuer != dest).
func TestEscrowCreate_SetsIssuerNode_IOU(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	seq := env.Seq(alice)
	r := env.Submit(escrow.EscrowCreate(alice, bob, 0).
		IOUAmount(usd(1000, gw)).
		FinishTime(env.Now().Add(1 * time.Second)).Build())
	jtx.RequireTxSuccess(t, r)

	esc := parseEscrowSLE(t, env, alice, seq)
	require.True(t, esc.HasIssuerNode, "IOU escrow with a third-party issuer must carry sfIssuerNode")
	require.Equal(t, uint64(0), esc.IssuerNode, "fresh issuer dir → page 0")
	require.True(t, esc.HasDestNode, "cross-account IOU escrow must also carry sfDestinationNode")
	require.Equal(t, uint64(0), esc.DestinationNode, "fresh destination dir → page 0")
	require.Equal(t, uint64(0), esc.OwnerNode, "fresh owner dir → page 0")
}

// A cross-account IOU escrow finishes cleanly: EscrowFinish reads sfIssuerNode and
// sfDestinationNode to remove the escrow from the issuer's and destination's owner
// directories. Reference: rippled Escrow.cpp:1132-1140 (dest), 1175-1183 (issuer).
func TestEscrowCreate_IOU_FinishesCleanly(t *testing.T) {
	env, gw, alice, bob := setupIOUEscrowEnv(t)

	seq := env.Seq(alice)
	r := env.Submit(escrow.EscrowCreate(alice, bob, 0).
		IOUAmount(usd(1000, gw)).
		FinishTime(env.Now().Add(1 * time.Second)).Build())
	jtx.RequireTxSuccess(t, r)
	require.True(t, parseEscrowSLE(t, env, alice, seq).HasIssuerNode)
	env.Close()

	// Advance past FinishAfter.
	env.Close()
	fin := env.Submit(escrow.EscrowFinish(bob, alice, seq).Build())
	jtx.RequireTxSuccess(t, fin)
	require.False(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)),
		"IOU escrow object should be removed after finish")
}
