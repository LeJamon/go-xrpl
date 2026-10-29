package escrow_test

import (
	"encoding/hex"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestEscrow_LongFormLengths(t *testing.T) {
	const emptyBody = "8020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100"
	const empty = "A025" + emptyBody
	const oneByte = "A0258020CA978112CA1BBDCAFAC231B39A23DC4DA786EFF8147C4E72B9807785AFEE48BB810101"
	for _, tc := range []struct{ name, condition, fulfillment string }{
		{"condition4", "A08400000025" + emptyBody, "A0028000"},
		{"condition5", "A0850000000025" + emptyBody, "A0028000"},
		{"condition8", "A0880000000000000025" + emptyBody, "A0028000"},
		{"fulfillment5", empty, "A08500000000028000"},
		{"fulfillment8", empty, "A08800000000000000028000"},
		{"preimage5", oneByte, "A0088085000000000161"},
		{"preimage8", oneByte, "A00B8088000000000000000161"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice, bob, carol := jtx.NewAccount("alice"), jtx.NewAccount("bob"), jtx.NewAccount("carol")
			fund5000(env, alice, bob, carol)
			condition, err := hex.DecodeString(tc.condition)
			require.NoError(t, err)
			fulfillment, err := hex.DecodeString(tc.fulfillment)
			require.NoError(t, err)
			seq := env.Seq(alice)
			jtx.RequireTxSuccess(t, env.Submit(escrow.EscrowCreate(alice, carol, xrp(1000)).
				Condition(condition).CancelTime(env.Now().Add(100*time.Second)).Build()))
			env.Close()
			jtx.RequireOwnerCount(t, env, alice, 1)
			jtx.RequireTxSuccess(t, env.Submit(escrow.EscrowFinish(bob, alice, seq).
				Condition(condition).Fulfillment(fulfillment).Fee(150*env.BaseFee()).Build()))
			jtx.RequireBalance(t, env, carol, uint64(xrp(6000)))
			jtx.RequireOwnerCount(t, env, alice, 0)
			require.False(t, env.LedgerEntryExists(keylet.Escrow(alice.ID, seq)))
		})
	}
}
