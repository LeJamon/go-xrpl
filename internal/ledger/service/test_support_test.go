package service

import (
	"context"
	"strconv"

	appconfig "github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
)

const historyWindow = uint32(appconfig.DefaultLedgerCacheSize)

func formatRange(min, max uint32) string {
	return strconv.FormatUint(uint64(min), 10) + "-" + strconv.FormatUint(uint64(max), 10)
}

func DefaultConfig() Config {
	return Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
	}
}

func (s *Service) getLedgerForQuery(ledgerIndex string) (*ledger.Ledger, bool, error) {
	return s.resolveLedgerForQuery(context.Background(), ledgerIndex)
}

func (s *Service) persistValidatedTip(ctx context.Context, l *ledger.Ledger) error {
	return s.persistValidatedTipJob(ctx, l, false, nil)
}
