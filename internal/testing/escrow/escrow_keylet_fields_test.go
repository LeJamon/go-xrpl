// fixIncludeKeyletFields: a created Escrow records sfSequence (the tx or ticket
// sequence used to derive its keylet) once the amendment is active, and omits it
// otherwise. Reference: rippled Escrow.cpp EscrowCreate::doApply() and
// Escrow_test.cpp testTags (fixIncludeKeyletFields arm).
package escrow_test

import (
	"encoding/hex"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// escrowSLEFields decodes the raw Escrow SLE into a field map.
func escrowSLEFields(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, seq uint32) map[string]any {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Escrow(owner.ID, seq))
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	return fields
}

// With the amendment enabled (test env default), the created escrow stores the
// creating account sequence in sfSequence.
func TestEscrowCreate_IncludeKeyletFields_StoresSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	seq := env.Seq(alice)
	r := env.Submit(escrow.EscrowCreate(alice, bob, xrp(1000)).
		FinishTime(env.Now().Add(1 * time.Second)).Build())
	jtx.RequireTxSuccess(t, r)

	fields := escrowSLEFields(t, env, alice, seq)
	require.Equal(t, seq, fields["Sequence"], "escrow must store the creating sequence in sfSequence")
}

// A ticket-based create stores the ticket sequence (getSeqValue), matching the
// value the escrow keylet is derived from.
func TestEscrowCreate_IncludeKeyletFields_StoresTicketSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.Close()

	ticketSeq := env.CreateTickets(alice, 1)
	env.Close()

	txn := escrow.EscrowCreate(alice, bob, xrp(1000)).
		FinishTime(env.Now().Add(1 * time.Second)).Build()
	jtx.WithTicketSeq(txn, ticketSeq)
	jtx.RequireTxSuccess(t, env.Submit(txn))

	fields := escrowSLEFields(t, env, alice, ticketSeq)
	require.Equal(t, ticketSeq, fields["Sequence"], "escrow must store the ticket sequence in sfSequence")
}

// With the amendment disabled, sfSequence is absent from the escrow SLE.
func TestEscrowCreate_NoIncludeKeyletFields_OmitsSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	fund5000(env, alice, bob)
	env.DisableFeature("fixIncludeKeyletFields")
	env.Close()

	seq := env.Seq(alice)
	r := env.Submit(escrow.EscrowCreate(alice, bob, xrp(1000)).
		FinishTime(env.Now().Add(1 * time.Second)).Build())
	jtx.RequireTxSuccess(t, r)

	fields := escrowSLEFields(t, env, alice, seq)
	_, present := fields["Sequence"]
	require.False(t, present, "escrow must not store sfSequence when the amendment is off")
}
