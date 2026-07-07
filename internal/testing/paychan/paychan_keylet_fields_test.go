// fixIncludeKeyletFields: a created PayChannel records sfSequence (the tx or
// ticket sequence used to derive its keylet) once the amendment is active, and
// omits it otherwise. Reference: rippled PayChan.cpp PayChanCreate::doApply() and
// PayChan_test.cpp testMetaAndOwnership (fixIncludeKeyletFields arm).
package paychan

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// chanSLEFields decodes the raw PayChannel SLE into a field map.
func chanSLEFields(t *testing.T, env *jtx.TestEnv, k keylet.Keylet) map[string]any {
	t.Helper()
	data, err := env.LedgerEntry(k)
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	return fields
}

// With the amendment enabled (test env default), the created channel stores the
// creating account sequence in sfSequence.
func TestPayChanCreate_IncludeKeyletFields_StoresSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	seq := env.Seq(alice)
	k := chanKeylet(alice, bob, seq)
	jtx.RequireTxSuccess(t, env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, alice.PublicKeyHex()).Build()))

	fields := chanSLEFields(t, env, k)
	require.Equal(t, seq, fields["Sequence"], "channel must store the creating sequence in sfSequence")
}

// A ticket-based create stores the ticket sequence (getSeqValue), matching the
// value the channel keylet is derived from.
func TestPayChanCreate_IncludeKeyletFields_StoresTicketSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	ticketSeq := env.CreateTickets(alice, 1)
	env.Close()

	k := chanKeylet(alice, bob, ticketSeq)
	jtx.RequireTxSuccess(t, env.Submit(
		ChannelCreate(alice, bob, xrp(1000), 100, alice.PublicKeyHex()).Ticket(ticketSeq).Build()))

	fields := chanSLEFields(t, env, k)
	require.Equal(t, ticketSeq, fields["Sequence"], "channel must store the ticket sequence in sfSequence")
}

// With the amendment disabled, sfSequence is absent from the channel SLE.
func TestPayChanCreate_NoIncludeKeyletFields_OmitsSequence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.DisableFeature("fixIncludeKeyletFields")
	env.Close()

	seq := env.Seq(alice)
	k := chanKeylet(alice, bob, seq)
	jtx.RequireTxSuccess(t, env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, alice.PublicKeyHex()).Build()))

	fields := chanSLEFields(t, env, k)
	_, present := fields["Sequence"]
	require.False(t, present, "channel must not store sfSequence when the amendment is off")
}
