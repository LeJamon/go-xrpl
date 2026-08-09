package adaptor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/require"
)

func TestFullyValidatedLogDistinguishesLocalTipFromQuorum(t *testing.T) {
	svc, err := service.New(service.Config{GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	t.Cleanup(svc.Stop)

	a := New(Config{LedgerService: svc})
	var output bytes.Buffer
	a.logger = slog.New(slog.NewTextHandler(&output, nil))

	hash := [32]byte{0x42}
	a.onValidatedLedger(2, hash, [32]byte{0x41})
	require.Contains(t, output.String(), "Ledger fully validated")

	output.Reset()
	a.OnLedgerFullyValidated(consensus.LedgerID{0xff}, 3)
	require.Contains(t, output.String(), "trusted validation quorum observed")
	require.False(t, strings.Contains(output.String(), "Ledger fully validated"),
		"a missing local ledger must not be logged as fully validated")
}

func TestOnLedgerFullyValidatedIgnoresCurrentTip(t *testing.T) {
	svc, err := service.New(service.Config{GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	svc.SetValidatedLedger(closed.Sequence(), closed.Hash())
	require.NoError(t, svc.SwitchToPreferredLedger(closed))
	require.False(t, svc.NeedsInitialSync())

	a := New(Config{LedgerService: svc})
	calls := 0
	a.onLedgerFullyValidated = func(uint32, [32]byte) { calls++ }
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)

	a.OnLedgerFullyValidated(consensus.LedgerID(validated.Hash()), validated.Sequence())
	require.Zero(t, calls)
	require.Equal(t, validated.Sequence(), a.networkValidatedSeq.Load())
}

func TestValidatedLedgerNotifiesValidationConfigChange(t *testing.T) {
	svc, err := service.New(service.Config{GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	svc.SetValidatedLedger(closed.Sequence(), closed.Hash())

	a := New(Config{LedgerService: svc})
	calls := 0
	a.OnValidationConfigChanged(func() { calls++ })
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)

	a.onValidatedLedger(validated.Sequence(), validated.Hash(), validated.ParentHash())
	require.Equal(t, 1, calls)
}
