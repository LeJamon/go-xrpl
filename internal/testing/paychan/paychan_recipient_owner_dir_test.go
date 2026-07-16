package paychan

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/stretchr/testify/require"
)

func TestPayChanCreateRecipientOwnerDirectoryCompatibility(t *testing.T) {
	tests := []struct {
		name             string
		replayPreFix     bool
		wantRecipientDir bool
	}{
		{name: "v3.2.0", wantRecipientDir: true},
		{name: "pre-fix replay", replayPreFix: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.Close()

			sequence := env.Seq(alice)
			channelKey := chanKeylet(alice, bob, sequence)
			transaction := ChannelCreate(alice, bob, xrp(1000), 100, alice.PublicKeyHex()).
				Sequence(sequence).
				Build()

			result := txengine.NewEngine(env.Ledger(), tx.EngineConfig{
				BaseFee:                              env.BaseFee(),
				ReserveBase:                          env.ReserveBase(),
				ReserveIncrement:                     env.ReserveIncrement(),
				LedgerSequence:                       env.LedgerSeq(),
				SkipSignatureVerification:            true,
				ReplayPreFixPayChanRecipientOwnerDir: test.replayPreFix,
				ParentCloseTime:                      env.NowRipple(),
				ParentHash:                           env.Ledger().ParentHash(),
				Rules:                                amendment.AllSupportedRules(),
				OpenLedger:                           true,
			}).Apply(transaction)
			require.Equal(t, "tesSUCCESS", result.Result.String())
			require.True(t, result.Applied)

			data, err := env.LedgerEntry(channelKey)
			require.NoError(t, err)
			channel, err := state.ParsePayChannel(data)
			require.NoError(t, err)

			require.True(t, inOwnerDir(env, alice, channelKey.Key))
			require.Equal(t, test.wantRecipientDir, inOwnerDir(env, bob, channelKey.Key))
			require.Equal(t, test.wantRecipientDir, channel.HasDestNode)
			if test.wantRecipientDir {
				require.Equal(t, uint64(0), channel.DestinationNode)
			}
		})
	}
}
