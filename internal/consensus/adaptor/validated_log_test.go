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
